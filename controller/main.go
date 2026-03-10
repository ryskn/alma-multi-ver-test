package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	pb "github.com/ryosuke/alma/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/yaml.v3"
)

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

func uploadFile(ctx context.Context, client pb.AgentClient, data []byte, dest string, isTar bool) error {
	stream, err := client.Upload(ctx)
	if err != nil {
		return fmt.Errorf("open upload stream: %w", err)
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
			return fmt.Errorf("send chunk: %w", err)
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return fmt.Errorf("close upload: %w", err)
	}
	fmt.Printf("    uploaded %d bytes\n", resp.BytesWritten)
	return nil
}

func doUpload(ctx context.Context, client pb.AgentClient, spec *UploadSpec, vars map[string]string) error {
	src := expandVars(spec.Src, vars)
	dest := expandVars(spec.Dest, vars)

	if spec.GitArchive != "" {
		branch := expandVars(spec.GitArchive, vars)
		// Use git archive to create tar from the source repo
		prefix := "src/"
		if strings.HasSuffix(dest, "/") {
			// extract prefix from dest basename
		}
		cmd := exec.CommandContext(ctx, "git", "archive", "--prefix="+prefix, branch)
		cmd.Dir = src
		tarData, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("git archive %s (branch %s): %w", src, branch, err)
		}
		return uploadFile(ctx, client, tarData, dest, true)
	}

	// Regular file upload
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	if info.IsDir() {
		// tar the directory
		cmd := exec.CommandContext(ctx, "tar", "cf", "-", "-C", src, ".")
		tarData, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("tar %s: %w", src, err)
		}
		return uploadFile(ctx, client, tarData, dest, true)
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	return uploadFile(ctx, client, data, dest, false)
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

	fmt.Printf("Job: %s (%d targets, %d steps)\n", job.Name, len(tgts), len(job.Steps))

	for i, step := range job.Steps {
		fmt.Printf("\n--- Step %d: %s ---\n", i+1, step.Name)

		var wg sync.WaitGroup
		var mu sync.Mutex
		failed := false

		for _, t := range tgts {
			wg.Add(1)
			go func(name, addr string) {
				defer wg.Done()

				vars := map[string]string{"target": name}
				if job.Vars != nil {
					for k, v := range job.Vars[name] {
						vars[k] = v
					}
				}

				client, conn, err := dial(addr)
				if err != nil {
					mu.Lock()
					fmt.Printf("[%s] FAIL: %v\n", name, err)
					failed = true
					mu.Unlock()
					return
				}
				defer conn.Close()

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()

				if step.Upload != nil {
					fmt.Printf("[%s] uploading...\n", name)
					if err := doUpload(ctx, client, step.Upload, vars); err != nil {
						mu.Lock()
						fmt.Printf("[%s] UPLOAD FAIL: %v\n", name, err)
						failed = true
						mu.Unlock()
						return
					}
					fmt.Printf("[%s] upload OK\n", name)
				}

				if step.Run != "" {
					script := expandVars(step.Run, vars)
					resp, err := client.ExecScript(ctx, &pb.ExecRequest{
						ScriptName: step.Name,
						ScriptBody: script,
					})
					if err != nil {
						mu.Lock()
						fmt.Printf("[%s] EXEC FAIL: %v\n", name, err)
						failed = true
						mu.Unlock()
						return
					}

					mu.Lock()
					fmt.Printf("[%s] exit=%d\n", name, resp.ExitCode)
					if resp.Stdout != "" {
						// Indent output
						for _, line := range strings.Split(strings.TrimRight(resp.Stdout, "\n"), "\n") {
							fmt.Printf("[%s]   %s\n", name, line)
						}
					}
					if resp.Stderr != "" {
						for _, line := range strings.Split(strings.TrimRight(resp.Stderr, "\n"), "\n") {
							fmt.Printf("[%s]   (stderr) %s\n", name, line)
						}
					}
					if resp.ExitCode != 0 {
						failed = true
					}
					mu.Unlock()
				}
			}(t.Name, t.Addr)
		}
		wg.Wait()

		if failed {
			fmt.Printf("\n!!! Step %d failed on one or more targets. Stopping.\n", i+1)
			os.Exit(1)
		}
	}

	fmt.Printf("\nJob %q completed successfully.\n", job.Name)
}

// --- main ---

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: alma-ctl <command> [args]\n")
		fmt.Fprintf(os.Stderr, "  ping              check all VMs\n")
		fmt.Fprintf(os.Stderr, "  exec <script.sh>  run script on all VMs\n")
		fmt.Fprintf(os.Stderr, "  run  <job.yaml>   run YAML job on targets\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "ping":
		cmdPing()
	case "exec":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "usage: alma-ctl exec <script.sh>\n")
			os.Exit(1)
		}
		cmdExec(os.Args[2])
	case "run":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "usage: alma-ctl run <job.yaml>\n")
			os.Exit(1)
		}
		cmdRun(os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

