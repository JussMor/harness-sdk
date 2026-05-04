// Package sandbox provides production SandboxDriver implementations
// for the autobuild SDK. Each file is one provider.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// OpenSandboxDriver implements autobuild.SandboxDriver backed by
// OpenSandbox (https://open-sandbox.ai). Each autobuild sandbox ID maps
// to one OpenSandbox sandbox. Sandboxes are created on demand and cached
// for the lifetime of the driver.
//
// Wire it into an Engine:
//
//	engine.Sandbox = sandbox.NewOpenSandbox(sandbox.OpenSandboxConfig{
//	    Domain:  "api.open-sandbox.ai",
//	    APIKey:  os.Getenv("OPEN_SANDBOX_API_KEY"),
//	    Protocol: "https",
//	})
//
// Each sandbox uses the code-interpreter image by default, which supports
// multi-language execution (Python, JavaScript, Bash) with persistent state
// across turns of the same conversation.
type OpenSandboxDriver struct {
	config opensandbox.ConnectionConfig
	image  string

	mu       sync.Mutex
	sandboxes map[string]*opensandbox.CodeInterpreter // autobuild id → interpreter
}

// OpenSandboxConfig configures the OpenSandbox connection.
type OpenSandboxConfig struct {
	// Domain is the OpenSandbox server (e.g. "api.open-sandbox.ai" or "localhost:8080").
	// Falls back to OPEN_SANDBOX_DOMAIN env var.
	Domain string

	// Protocol is "https" or "http". Falls back to OPEN_SANDBOX_PROTOCOL env var.
	Protocol string

	// APIKey for authentication. Falls back to OPEN_SANDBOX_API_KEY env var.
	APIKey string

	// Image overrides the default code-interpreter image.
	// Default: opensandbox/code-interpreter:latest
	Image string

	// TimeoutSeconds is the sandbox TTL. Default: 900 (15 min).
	TimeoutSeconds *int

	// ReadyTimeout is how long to wait for the sandbox to become ready.
	// Default: 60s.
	ReadyTimeout time.Duration
}

// NewOpenSandbox creates a driver using the given config.
// Each Create() call spawns a new OpenSandbox CodeInterpreter.
func NewOpenSandbox(cfg OpenSandboxConfig) *OpenSandboxDriver {
	image := cfg.Image
	if image == "" {
		image = opensandbox.CodeInterpreterImage
	}
	return &OpenSandboxDriver{
		config: opensandbox.ConnectionConfig{
			Domain:   cfg.Domain,
			Protocol: cfg.Protocol,
			APIKey:   cfg.APIKey,
		},
		image:     image,
		sandboxes: make(map[string]*opensandbox.CodeInterpreter),
	}
}

// Create provisions a new OpenSandbox CodeInterpreter and returns its ID.
// The sandbox is cached by ID for subsequent operations.
func (d *OpenSandboxDriver) Create(ctx context.Context, cfg autobuild.SandboxConfig) (string, error) {
	readyTimeout := 60 * time.Second

	opts := opensandbox.CodeInterpreterCreateOptions{
		TimeoutSeconds:  d.timeoutPtr(),
		ReadyTimeout:    readyTimeout,
		Env:             cfg.Env,
		Metadata:        cfg.Labels,
	}
	if cfg.Image != "" {
		opts.Image = cfg.Image
	}

	ci, err := opensandbox.CreateCodeInterpreter(ctx, d.config, opts)
	if err != nil {
		return "", fmt.Errorf("opensandbox: create: %w", err)
	}

	id := ci.Sandbox.ID()
	d.mu.Lock()
	d.sandboxes[id] = ci
	d.mu.Unlock()

	return id, nil
}

// Exec runs a shell command inside the sandbox. Returns combined stdout/stderr
// and exit code. Streaming output is accumulated — for real-time output use
// ExecStreaming.
func (d *OpenSandboxDriver) Exec(ctx context.Context, id string, command string) (autobuild.ExecResult, error) {
	ci, err := d.get(ctx, id)
	if err != nil {
		return autobuild.ExecResult{}, err
	}

	exec, err := ci.Sandbox.RunCommand(ctx, command, nil)
	if err != nil {
		return autobuild.ExecResult{}, fmt.Errorf("opensandbox: exec: %w", err)
	}

	result := autobuild.ExecResult{
		Stdout: exec.Text(),
	}
	var stderr strings.Builder
	for _, m := range exec.Stderr {
		stderr.WriteString(m.Text)
		stderr.WriteByte('\n')
	}
	result.Stderr = strings.TrimRight(stderr.String(), "\n")

	if exec.ExitCode != nil {
		result.ExitCode = *exec.ExitCode
	} else if exec.Error != nil {
		result.ExitCode = 1
		if result.Stderr == "" {
			result.Stderr = exec.Error.Value
		}
	}
	return result, nil
}

// ExecCode runs code in a specific language using the code interpreter.
// Language can be "python", "javascript", "bash", etc.
// Returns structured execution results including text/plain and text/html outputs.
func (d *OpenSandboxDriver) ExecCode(ctx context.Context, id, language, code string) (*opensandbox.Execution, error) {
	ci, err := d.get(ctx, id)
	if err != nil {
		return nil, err
	}
	exec, err := ci.Execute(ctx, language, code, nil)
	if err != nil {
		return nil, fmt.Errorf("opensandbox: exec code (%s): %w", language, err)
	}
	return exec, nil
}

// ExecCodeStreaming runs code with live output callbacks.
// Useful for long-running code where you want to stream stdout to the user.
func (d *OpenSandboxDriver) ExecCodeStreaming(ctx context.Context, id, language, code string, handlers *opensandbox.ExecutionHandlers) (*opensandbox.Execution, error) {
	ci, err := d.get(ctx, id)
	if err != nil {
		return nil, err
	}
	exec, err := ci.Execute(ctx, language, code, handlers)
	if err != nil {
		return nil, fmt.Errorf("opensandbox: exec streaming (%s): %w", language, err)
	}
	return exec, nil
}

// WriteFile uploads content to a path inside the sandbox.
func (d *OpenSandboxDriver) WriteFile(ctx context.Context, id string, path string, content string) error {
	ci, err := d.get(ctx, id)
	if err != nil {
		return err
	}
	reader := strings.NewReader(content)
	return ci.Sandbox.UploadFile(ctx, reader, opensandbox.UploadFileOptions{
		Metadata: opensandbox.FileMetadata{Path: path},
	})
}

// ReadFile downloads the content of a file from the sandbox.
func (d *OpenSandboxDriver) ReadFile(ctx context.Context, id string, path string) (string, error) {
	ci, err := d.get(ctx, id)
	if err != nil {
		return "", err
	}
	rc, err := ci.Sandbox.DownloadFile(ctx, path, "")
	if err != nil {
		return "", fmt.Errorf("opensandbox: read file %s: %w", path, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("opensandbox: read file data %s: %w", path, err)
	}
	return string(data), nil
}

// Destroy terminates a sandbox and removes it from the cache.
func (d *OpenSandboxDriver) Destroy(ctx context.Context, id string) error {
	d.mu.Lock()
	ci, ok := d.sandboxes[id]
	if ok {
		delete(d.sandboxes, id)
	}
	d.mu.Unlock()

	if !ok {
		return nil
	}
	return ci.Sandbox.Kill(ctx)
}

// Status returns the current lifecycle state of the sandbox.
func (d *OpenSandboxDriver) Status(ctx context.Context, id string) (autobuild.SandboxStatus, error) {
	d.mu.Lock()
	ci, ok := d.sandboxes[id]
	d.mu.Unlock()

	if !ok {
		return autobuild.SandboxStatusUnknown, fmt.Errorf("opensandbox: unknown sandbox %q", id)
	}

	info, err := ci.Sandbox.GetInfo(ctx)
	if err != nil {
		return autobuild.SandboxStatusError, fmt.Errorf("opensandbox: get info: %w", err)
	}

	return mapState(info.Status.State), nil
}

// IP returns the endpoint URL for the default HTTP port (8080) of the sandbox.
// Useful when the sandbox is serving a web app (HTML artifact).
func (d *OpenSandboxDriver) IP(ctx context.Context, id string) (string, error) {
	d.mu.Lock()
	ci, ok := d.sandboxes[id]
	d.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("opensandbox: unknown sandbox %q", id)
	}

	endpoint, err := ci.Sandbox.GetEndpoint(ctx, 8080)
	if err != nil {
		return "", fmt.Errorf("opensandbox: get endpoint: %w", err)
	}
	return endpoint.Endpoint, nil
}

// GetCodeInterpreter returns the underlying CodeInterpreter for a sandbox ID.
// Use this when you need direct access to OpenSandbox-specific methods
// (e.g. ExecCodeStreaming with custom handlers).
func (d *OpenSandboxDriver) GetCodeInterpreter(ctx context.Context, id string) (*opensandbox.CodeInterpreter, error) {
	return d.get(ctx, id)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (d *OpenSandboxDriver) get(ctx context.Context, id string) (*opensandbox.CodeInterpreter, error) {
	d.mu.Lock()
	ci, ok := d.sandboxes[id]
	d.mu.Unlock()

	if ok {
		return ci, nil
	}

	// Not in cache — try to reconnect (e.g. after process restart)
	sb, err := opensandbox.ConnectSandbox(ctx, d.config, id)
	if err != nil {
		return nil, fmt.Errorf("opensandbox: sandbox %q not found and reconnect failed: %w", id, err)
	}
	ci = &opensandbox.CodeInterpreter{Sandbox: sb}
	d.mu.Lock()
	d.sandboxes[id] = ci
	d.mu.Unlock()
	return ci, nil
}

func (d *OpenSandboxDriver) timeoutPtr() *int {
	t := 900 // 15 min default
	return &t
}

func mapState(state opensandbox.SandboxState) autobuild.SandboxStatus {
	switch state {
	case opensandbox.StatePending:
		return autobuild.SandboxStatusCreating
	case opensandbox.StateRunning:
		return autobuild.SandboxStatusRunning
	case opensandbox.StatePausing, opensandbox.StatePaused, opensandbox.StateStopping:
		return autobuild.SandboxStatusStopped
	case opensandbox.StateTerminated, opensandbox.StateFailed:
		return autobuild.SandboxStatusStopped
	default:
		return autobuild.SandboxStatusUnknown
	}
}

// Verify interface at compile time.
var _ autobuild.SandboxDriver = (*OpenSandboxDriver)(nil)
