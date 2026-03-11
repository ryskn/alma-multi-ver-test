package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pb "github.com/ryosuke/alma/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/yaml.v3"
)

// ANSI colors
const (
	colorReset  = "\033[0m"
	colorBold   = "\033[1m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorDim    = "\033[2m"
)

// Verbosity level: 0=quiet, 1=summary(3 lines), 2=full stdout, 3=full stdout+stderr
var verbosity int

var targets = []struct {
	Name string
	Addr string
}{
	{"alma8", "192.168.200.10:50051"},
	{"alma9", "192.168.200.11:50051"},
	{"alma10", "192.168.200.12:50051"},
}

func dial(addr string) (pb.AgentClient, *grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, nil, err
	}
	return pb.NewAgentClient(conn), conn, nil
}

func cmdPing() {
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(name, addr string) {
			defer wg.Done()
			client, conn, err := dial(addr)
			if err != nil {
				fmt.Printf("[%s] FAIL: %v\n", name, err)
				return
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			resp, err := client.Ping(ctx, &pb.PingRequest{})
			if err != nil {
				fmt.Printf("[%s] FAIL: %v\n", name, err)
				return
			}
			fmt.Printf("[%s] OK  hostname=%s os=%s\n", name, resp.Hostname, resp.OsInfo)
		}(t.Name, t.Addr)
	}
	wg.Wait()
}

func cmdExec(scriptPath string) {
	body, err := os.ReadFile(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading script: %v\n", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(name, addr string) {
			defer wg.Done()
			client, conn, err := dial(addr)
			if err != nil {
				fmt.Printf("=== %s ===\nFAIL: %v\n\n", name, err)
				return
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			resp, err := client.ExecScript(ctx, &pb.ExecRequest{
				ScriptName: scriptPath,
				ScriptBody: string(body),
			})
			if err != nil {
				fmt.Printf("=== %s ===\nFAIL: %v\n\n", name, err)
				return
			}

			fmt.Printf("=== %s === (exit %d)\n", name, resp.ExitCode)
			if resp.Stdout != "" {
				fmt.Printf("[stdout]\n%s", resp.Stdout)
			}
			if resp.Stderr != "" {
				fmt.Printf("[stderr]\n%s", resp.Stderr)
			}
			fmt.Println()
		}(t.Name, t.Addr)
	}
	wg.Wait()
}

// --- YAML Job Runner ---

type Job struct {
	Name    string                       `yaml:"name"`
	Targets []string                     `yaml:"targets"`
	Vars    map[string]map[string]string `yaml:"vars"`
	Steps   []Step                       `yaml:"steps"`
}

type Step struct {
	Name   string      `yaml:"name"`
	Run    string      `yaml:"run"`
	Upload *UploadSpec `yaml:"upload"`
}

type UploadSpec struct {
	Src        string `yaml:"src"`
	Dest       string `yaml:"dest"`
	GitArchive string `yaml:"git_archive"`
}

// targetLog holds per-target log writer
type targetLog struct {
	file *os.File
}

func (tl *targetLog) Write(s string) {
	if tl.file != nil {
		tl.file.WriteString(s)
	}
}

func (tl *targetLog) Close() {
	if tl.file != nil {
		tl.file.Close()
	}
}

func expandVars(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

func resolveTargets(names []string) []struct{ Name, Addr string } {
	if len(names) == 0 {
		return targets
	}
	addrMap := map[string]string{}
	for _, t := range targets {
		addrMap[t.Name] = t.Addr
	}
	var result []struct{ Name, Addr string }
	for _, n := range names {
		if addr, ok := addrMap[n]; ok {
			result = append(result, struct{ Name, Addr string }{n, addr})
		} else {
			fmt.Fprintf(os.Stderr, "warning: unknown target %q, skipping\n", n)
		}
	}
	return result
}

const uploadChunkSize = 64 * 1024

func uploadFile(ctx context.Context, client pb.AgentClient, data []byte, dest string, isTar bool) (int64, error) {
	stream, err := client.Upload(ctx)
	if err != nil {
		return 0, fmt.Errorf("open upload stream: %w", err)
	}

	for i := 0; i < len(data); i += uploadChunkSize {
		end := i + uploadChunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := &pb.UploadChunk{
			Data: data[i:end],
		}
		if i == 0 {
			chunk.DestPath = dest
			chunk.IsTar = isTar
		}
		if err := stream.Send(chunk); err != nil {
			return 0, fmt.Errorf("send chunk: %w", err)
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return 0, fmt.Errorf("close upload: %w", err)
	}
	return resp.BytesWritten, nil
}

func doUpload(ctx context.Context, client pb.AgentClient, spec *UploadSpec, vars map[string]string) (int64, error) {
	src := expandVars(spec.Src, vars)
	dest := expandVars(spec.Dest, vars)

	if spec.GitArchive != "" {
		branch := expandVars(spec.GitArchive, vars)
		prefix := "src/"
		if strings.HasSuffix(dest, "/") {
		}
		cmd := exec.CommandContext(ctx, "git", "archive", "--prefix="+prefix, branch)
		cmd.Dir = src
		tarData, err := cmd.Output()
		if err != nil {
			return 0, fmt.Errorf("git archive %s (branch %s): %w", src, branch, err)
		}
		return uploadFile(ctx, client, tarData, dest, true)
	}

	info, err := os.Stat(src)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", src, err)
	}

	if info.IsDir() {
		cmd := exec.CommandContext(ctx, "tar", "cf", "-", "-C", src, ".")
		tarData, err := cmd.Output()
		if err != nil {
			return 0, fmt.Errorf("tar %s: %w", src, err)
		}
		return uploadFile(ctx, client, tarData, dest, true)
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", src, err)
	}
	return uploadFile(ctx, client, data, dest, false)
}

func printPrefixed(name, output string, isStderr bool) {
	if output == "" {
		return
	}
	prefix := colorDim
	if isStderr {
		prefix = colorYellow
	}
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		fmt.Printf("  %s  %s  %s%s\n", prefix, name, line, colorReset)
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%ds", m, s)
}

func cmdRun(jobFile string) {
	data, err := os.ReadFile(jobFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading job file: %v\n", err)
		os.Exit(1)
	}

	var job Job
	if err := yaml.Unmarshal(data, &job); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing job file: %v\n", err)
		os.Exit(1)
	}

	tgts := resolveTargets(job.Targets)
	if len(tgts) == 0 {
		fmt.Fprintf(os.Stderr, "no valid targets\n")
		os.Exit(1)
	}

	// Create log directory: logs/<job-name>/<timestamp>/
	timestamp := time.Now().Format("20060102-150405")
	logDir := filepath.Join("logs", job.Name, timestamp)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error creating log dir: %v\n", err)
		os.Exit(1)
	}

	// Open per-target log files
	logs := map[string]*targetLog{}
	for _, t := range tgts {
		f, err := os.Create(filepath.Join(logDir, t.Name+".log"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating log file: %v\n", err)
			os.Exit(1)
		}
		tl := &targetLog{file: f}
		logs[t.Name] = tl
		defer tl.Close()
		tl.Write(fmt.Sprintf("# Job: %s\n# Target: %s (%s)\n# Started: %s\n\n",
			job.Name, t.Name, t.Addr, time.Now().Format(time.RFC3339)))
	}

	jobStart := time.Now()

	// Header
	fmt.Printf("\n%s%s Job: %s %s\n", colorBold, colorCyan, job.Name, colorReset)
	fmt.Printf("%s Targets: %s", colorDim, colorReset)
	for i, t := range tgts {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Print(t.Name)
	}
	fmt.Printf("  |  Steps: %d  |  Logs: %s\n\n", len(job.Steps), logDir)

	allPassed := true

	for i, step := range job.Steps {
		stepStart := time.Now()
		fmt.Printf("%s%s [%d/%d] %s %s\n", colorBold, colorCyan, i+1, len(job.Steps), step.Name, colorReset)

		type stepResult struct {
			err     string
			stdout  string
			stderr  string
			exit    int32
			uploadN int64
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		results := map[string]*stepResult{}

		for _, t := range tgts {
			wg.Add(1)
			go func(name, addr string) {
				defer wg.Done()

				res := &stepResult{}
				mu.Lock()
				results[name] = res
				mu.Unlock()

				tlog := logs[name]
				tlog.Write(fmt.Sprintf("=== Step %d: %s ===\n", i+1, step.Name))

				vars := map[string]string{"target": name}
				if job.Vars != nil {
					for k, v := range job.Vars[name] {
						vars[k] = v
					}
				}

				client, conn, err := dial(addr)
				if err != nil {
					mu.Lock()
					res.err = fmt.Sprintf("connection failed: %v", err)
					mu.Unlock()
					tlog.Write(fmt.Sprintf("FAIL: %v\n\n", err))
					return
				}
				defer conn.Close()

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()

				if step.Upload != nil {
					n, err := doUpload(ctx, client, step.Upload, vars)
					if err != nil {
						mu.Lock()
						res.err = fmt.Sprintf("upload failed: %v", err)
						mu.Unlock()
						tlog.Write(fmt.Sprintf("UPLOAD FAIL: %v\n\n", err))
						return
					}
					mu.Lock()
					res.uploadN = n
					mu.Unlock()
					tlog.Write(fmt.Sprintf("Uploaded %d bytes\n", n))
				}

				if step.Run != "" {
					script := expandVars(step.Run, vars)
					tlog.Write(fmt.Sprintf("$ %s\n", strings.Split(strings.TrimSpace(script), "\n")[0]))

					stream, err := client.ExecStream(ctx, &pb.ExecRequest{
						ScriptName: step.Name,
						ScriptBody: script,
					})
					if err != nil {
						mu.Lock()
						res.err = fmt.Sprintf("exec failed: %v", err)
						mu.Unlock()
						tlog.Write(fmt.Sprintf("EXEC FAIL: %v\n\n", err))
						return
					}

					var stdoutBuf, stderrBuf strings.Builder
					var exitCode int32

					for {
						out, err := stream.Recv()
						if err != nil {
							break
						}
						if out.Done {
							exitCode = out.ExitCode
							break
						}

						// Write to log
						tlog.Write(out.Line + "\n")

						// Collect for summary
						if out.IsStderr {
							stderrBuf.WriteString(out.Line + "\n")
							tlog.Write("")
						} else {
							stdoutBuf.WriteString(out.Line + "\n")
						}

						// Real-time output based on verbosity
						if out.IsStderr && verbosity >= 3 {
							mu.Lock()
							fmt.Printf("  %s  %s  %s%s\n", colorYellow, name, out.Line, colorReset)
							mu.Unlock()
						} else if !out.IsStderr && verbosity >= 2 {
							mu.Lock()
							fmt.Printf("  %s  %s  %s%s\n", colorDim, name, out.Line, colorReset)
							mu.Unlock()
						}
					}

					mu.Lock()
					res.stdout = stdoutBuf.String()
					res.stderr = stderrBuf.String()
					res.exit = exitCode
					mu.Unlock()

					tlog.Write(fmt.Sprintf("--- exit: %d ---\n\n", exitCode))

					if exitCode != 0 {
						mu.Lock()
						res.err = fmt.Sprintf("exit %d", exitCode)
						mu.Unlock()
						return
					}
				}
			}(t.Name, t.Addr)
		}
		wg.Wait()

		stepDur := time.Since(stepStart)

		// Print results for this step
		stepFailed := false
		for _, t := range tgts {
			res := results[t.Name]
			if res.err != "" {
				stepFailed = true
				fmt.Printf("  %s%s %-8s FAIL%s  %s\n", colorBold, colorRed, t.Name, colorReset, res.err)
				// Always show last 5 lines on failure regardless of verbosity
				output := res.stdout + res.stderr
				if output != "" {
					lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
					start := 0
					if len(lines) > 5 {
						start = len(lines) - 5
					}
					for _, line := range lines[start:] {
						fmt.Printf("  %s  %s  %s%s\n", colorDim, t.Name, line, colorReset)
					}
				}
			} else {
				fmt.Printf("  %s%s %-8s OK%s", colorBold, colorGreen, t.Name, colorReset)
				if res.uploadN > 0 {
					fmt.Printf("    %s%d bytes uploaded%s", colorDim, res.uploadN, colorReset)
				}
				fmt.Println()

				// Post-step summary (skip if already streamed with -vv/-vvv)
				if verbosity == 1 && res.stdout != "" {
					// -v: last 3 lines
					lines := strings.Split(strings.TrimRight(res.stdout, "\n"), "\n")
					start := 0
					if len(lines) > 3 {
						start = len(lines) - 3
					}
					for _, line := range lines[start:] {
						trimmed := strings.TrimSpace(line)
						if trimmed != "" {
							fmt.Printf("  %s  %s  %s%s\n", colorDim, t.Name, trimmed, colorReset)
						}
					}
				}
			}
		}
		fmt.Printf("  %s(%s)%s\n\n", colorDim, formatDuration(stepDur), colorReset)

		if stepFailed {
			allPassed = false
			fmt.Printf("%s%s Step %d failed. Stopping.%s\n", colorBold, colorRed, i+1, colorReset)
			fmt.Printf("%sLogs: %s%s\n", colorDim, logDir, colorReset)
			os.Exit(1)
		}
	}

	totalDur := time.Since(jobStart)

	// Summary
	if allPassed {
		fmt.Printf("%s%s All %d steps passed%s %s(%s)%s\n",
			colorBold, colorGreen, len(job.Steps), colorReset,
			colorDim, formatDuration(totalDur), colorReset)
	}
	fmt.Printf("%sLogs: %s%s\n", colorDim, logDir, colorReset)
}

// --- main ---

func parseVerbosity(args []string) ([]string, int) {
	v := 0
	var rest []string
	for _, arg := range args {
		switch arg {
		case "-v":
			v = 1
		case "-vv":
			v = 2
		case "-vvv":
			v = 3
		default:
			rest = append(rest, arg)
		}
	}
	return rest, v
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: alma-ctl [-v|-vv|-vvv] <command> [args]\n")
		fmt.Fprintf(os.Stderr, "  ping              check all VMs\n")
		fmt.Fprintf(os.Stderr, "  exec <script.sh>  run script on all VMs\n")
		fmt.Fprintf(os.Stderr, "  run  <job.yaml>   run YAML job on targets\n")
		fmt.Fprintf(os.Stderr, "\nVerbosity:\n")
		fmt.Fprintf(os.Stderr, "  -v     show last 3 lines of output per target\n")
		fmt.Fprintf(os.Stderr, "  -vv    show full stdout per target\n")
		fmt.Fprintf(os.Stderr, "  -vvv   show full stdout + stderr per target\n")
		os.Exit(1)
	}

	args, v := parseVerbosity(os.Args[1:])
	verbosity = v

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: alma-ctl [-v|-vv|-vvv] <command> [args]\n")
		os.Exit(1)
	}

	switch args[0] {
	case "ping":
		cmdPing()
	case "exec":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: alma-ctl exec <script.sh>\n")
			os.Exit(1)
		}
		cmdExec(args[1])
	case "run":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: alma-ctl run <job.yaml>\n")
			os.Exit(1)
		}
		cmdRun(args[1])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		os.Exit(1)
	}
}
