package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	pb "github.com/ryosuke/alma/proto"
	"google.golang.org/grpc"
)

type agentServer struct {
	pb.UnimplementedAgentServer
}

func (s *agentServer) ExecScript(ctx context.Context, req *pb.ExecRequest) (*pb.ExecResponse, error) {
	log.Printf("ExecScript: %s (%d bytes)", req.ScriptName, len(req.ScriptBody))

	tmp, err := os.CreateTemp("", "alma-*.sh")
	if err != nil {
		return &pb.ExecResponse{ExitCode: -1, Stderr: err.Error()}, nil
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(req.ScriptBody); err != nil {
		return &pb.ExecResponse{ExitCode: -1, Stderr: err.Error()}, nil
	}
	tmp.Close()

	cmd := exec.CommandContext(ctx, "/bin/bash", tmp.Name())
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := int32(0)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = int32(exitErr.ExitCode())
		} else {
			return &pb.ExecResponse{ExitCode: -1, Stderr: err.Error()}, nil
		}
	}

	return &pb.ExecResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}

func (s *agentServer) Upload(stream grpc.ClientStreamingServer[pb.UploadChunk, pb.UploadResponse]) error {
	var destPath string
	var isTar bool
	var buf []byte

	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if destPath == "" {
			destPath = chunk.DestPath
			isTar = chunk.IsTar
		}
		buf = append(buf, chunk.Data...)
	}

	if destPath == "" {
		return fmt.Errorf("no dest_path specified")
	}

	var bytesWritten int64

	if isTar {
		if err := os.MkdirAll(destPath, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", destPath, err)
		}
		n, err := extractTar(buf, destPath)
		if err != nil {
			return fmt.Errorf("extract tar: %w", err)
		}
		bytesWritten = n
		log.Printf("Upload: extracted tar to %s (%d bytes)", destPath, bytesWritten)
	} else {
		dir := filepath.Dir(destPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		if err := os.WriteFile(destPath, buf, 0644); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}
		bytesWritten = int64(len(buf))
		log.Printf("Upload: wrote %s (%d bytes)", destPath, bytesWritten)
	}

	return stream.SendAndClose(&pb.UploadResponse{BytesWritten: bytesWritten})
}

func extractTar(data []byte, destDir string) (int64, error) {
	// Try gzip first
	gr, err := gzip.NewReader(bytes.NewReader(data))
	var tr *tar.Reader
	if err == nil {
		tr = tar.NewReader(gr)
		defer gr.Close()
	} else {
		tr = tar.NewReader(bytes.NewReader(data))
	}

	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, err
		}

		target := filepath.Join(destDir, hdr.Name)
		// Prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			return total, fmt.Errorf("invalid tar path: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return total, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return total, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return total, err
			}
			n, err := io.Copy(f, tr)
			f.Close()
			if err != nil {
				return total, err
			}
			total += n
		case tar.TypeSymlink:
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

func (s *agentServer) Ping(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	hostname, _ := os.Hostname()

	out, _ := exec.Command("cat", "/etc/os-release").Output()
	osInfo := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			osInfo = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
			break
		}
	}

	return &pb.PingResponse{
		Hostname: hostname,
		OsInfo:   osInfo,
	}, nil
}

func main() {
	listen := flag.String("listen", ":50051", "listen address")
	flag.Parse()

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	srv := grpc.NewServer()
	pb.RegisterAgentServer(srv, &agentServer{})

	fmt.Printf("alma-agent listening on %s\n", *listen)
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
