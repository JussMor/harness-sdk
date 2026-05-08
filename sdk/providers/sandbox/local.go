// Package sandbox provides SandboxDriver implementations for the autobuild SDK.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// LocalSandbox runs commands directly on the host without isolation.
// Use ONLY for development and testing. For production, use DockerSandbox.
//
// WARNING: LocalSandbox gives the agent unrestricted access to the host
// filesystem and environment. Pair with SafetyFilter to prevent dangerous
// commands from reaching this driver.
//
// Each "sandbox" is a temporary directory. Destroy() removes it.
type LocalSandbox struct {
	mu      sync.Mutex
	sandboxes map[string]*localInstance
}

type localInstance struct {
	dir    string
	status autobuild.SandboxStatus
}

// NewLocal creates a LocalSandbox. No configuration needed.
func NewLocal() *LocalSandbox {
	return &LocalSandbox{
		sandboxes: make(map[string]*localInstance),
	}
}

// Create provisions a temp directory as the sandbox working directory.
func (s *LocalSandbox) Create(_ context.Context, cfg autobuild.SandboxConfig) (string, error) {
	dir, err := os.MkdirTemp("", "autobuild-sandbox-*")
	if err != nil {
		return "", fmt.Errorf("local sandbox: create temp dir: %w", err)
	}
	id := filepath.Base(dir)
	s.mu.Lock()
	s.sandboxes[id] = &localInstance{dir: dir, status: autobuild.SandboxStatusRunning}
	s.mu.Unlock()
	return id, nil
}

// Exec runs a shell command inside the sandbox directory.
func (s *LocalSandbox) Exec(ctx context.Context, id string, command string) (autobuild.ExecResult, error) {
	inst, err := s.get(id)
	if err != nil {
		return autobuild.ExecResult{}, err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = inst.dir
	out, execErr := cmd.CombinedOutput()
	result := autobuild.ExecResult{
		Stdout:   string(out),
		ExitCode: 0,
	}
	if execErr != nil {
		if exitErr, ok := execErr.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return result, fmt.Errorf("local sandbox exec: %w", execErr)
		}
	}
	return result, nil
}

// WriteFile writes content to a path inside the sandbox directory.
func (s *LocalSandbox) WriteFile(_ context.Context, id string, path string, content string) error {
	inst, err := s.get(id)
	if err != nil {
		return err
	}
	full := filepath.Join(inst.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(full, []byte(content), 0644)
}

// ReadFile returns file content from inside the sandbox.
func (s *LocalSandbox) ReadFile(_ context.Context, id string, path string) (string, error) {
	inst, err := s.get(id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(inst.dir, path))
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return string(data), nil
}

// Destroy removes the sandbox directory.
func (s *LocalSandbox) Destroy(_ context.Context, id string) error {
	s.mu.Lock()
	inst, ok := s.sandboxes[id]
	if ok {
		inst.status = autobuild.SandboxStatusStopped
		delete(s.sandboxes, id)
	}
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return os.RemoveAll(inst.dir)
}

// Status returns the current status.
func (s *LocalSandbox) Status(_ context.Context, id string) (autobuild.SandboxStatus, error) {
	inst, err := s.get(id)
	if err != nil {
		return autobuild.SandboxStatusStopped, err
	}
	return inst.status, nil
}

// IP always returns localhost for local sandboxes.
func (s *LocalSandbox) IP(_ context.Context, _ string) (string, error) {
	return "127.0.0.1", nil
}

func (s *LocalSandbox) get(id string) (*localInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.sandboxes[id]
	if !ok {
		return nil, fmt.Errorf("local sandbox: unknown id %q", id)
	}
	return inst, nil
}

// ExecStream runs a command and streams stdout/stderr line by line.
func (s *LocalSandbox) ExecStream(ctx context.Context, id string, command string) (<-chan autobuild.ExecOutput, error) {
	inst, err := s.get(id)
	if err != nil {
		return nil, err
	}

	out := make(chan autobuild.ExecOutput, 64)
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = inst.dir

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("local sandbox: start: %w", err)
	}

	go func() {
		defer close(out)
		buf := make([]byte, 4096)
		// Drain stdout
		go func() {
			for {
				n, err := stdout.Read(buf)
				if n > 0 {
					out <- autobuild.ExecOutput{Stream: "stdout", Data: string(buf[:n])}
				}
				if err != nil {
					return
				}
			}
		}()
		// Drain stderr
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				out <- autobuild.ExecOutput{Stream: "stderr", Data: string(buf[:n])}
			}
			if err != nil {
				break
			}
		}
		exitCode := 0
		if err := cmd.Wait(); err != nil {
			exitCode = 1
		}
		out <- autobuild.ExecOutput{Stream: "exit", ExitCode: &exitCode}
	}()

	return out, nil
}

var _ autobuild.SandboxDriver = (*LocalSandbox)(nil)
