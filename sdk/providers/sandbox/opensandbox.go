// Package sandbox provides SandboxDriver implementations backed by real
// execution environments. opensandbox.go uses OpenSandbox
// (https://open-sandbox.ai) for isolated container execution.
//
// Usage:
//
//	import sbprov "github.com/everfaz/autobuild-sdk/providers/sandbox"
//
//	driver, err := sbprov.NewOpenSandbox(sbprov.OpenSandboxConfig{
//	    Domain:   os.Getenv("OPEN_SANDBOX_DOMAIN"),
//	    APIKey:   os.Getenv("OPEN_SANDBOX_API_KEY"),
//	    Protocol: "https",
//	})
//	engine.Sandbox = driver
package sandbox

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	autobuild "github.com/everfaz/autobuild-sdk"
)

// OpenSandboxConfig holds connection settings for the OpenSandbox server.
type OpenSandboxConfig struct {
	// Domain is the server address, e.g. "api.open-sandbox.ai" or "localhost:8080".
	// Falls back to OPEN_SANDBOX_DOMAIN env var.
	Domain string

	// Protocol is "https" or "http". Falls back to OPEN_SANDBOX_PROTOCOL env var.
	Protocol string

	// APIKey is the authentication token. Falls back to OPEN_SANDBOX_API_KEY env var.
	APIKey string

	// DefaultImage overrides the code-interpreter image.
	// Defaults to opensandbox/code-interpreter:latest.
	DefaultImage string

	// DefaultTTLSeconds is the sandbox time-to-live. Defaults to 900 (15 min).
	DefaultTTLSeconds int

	// ReadyTimeout is how long to wait for a new sandbox to become ready.
	// Defaults to 60 seconds.
	ReadyTimeout time.Duration
}

// OpenSandboxDriver implements autobuild.SandboxDriver using OpenSandbox.
// Each autobuild sandbox ID maps to one OpenSandbox CodeInterpreter instance.
// Instances are lazily created on first use and cached for the driver lifetime.
type OpenSandboxDriver struct {
	config    OpenSandboxConfig
	conn      opensandbox.ConnectionConfig
	instances map[string]*opensandbox.CodeInterpreter
}

// NewOpenSandbox creates a driver. Sandboxes are created lazily — no network
// call happens until Create() or the first Exec/WriteFile.
func NewOpenSandbox(cfg OpenSandboxConfig) (*OpenSandboxDriver, error) {
	conn := opensandbox.ConnectionConfig{
		Domain:   cfg.Domain,
		Protocol: cfg.Protocol,
		APIKey:   cfg.APIKey,
	}
	return &OpenSandboxDriver{
		config:    cfg,
		conn:      conn,
		instances: make(map[string]*opensandbox.CodeInterpreter),
	}, nil
}

// ── autobuild.SandboxDriver ──────────────────────────────────────────────────

// Create provisions a new CodeInterpreter sandbox and returns its ID.
func (d *OpenSandboxDriver) Create(ctx context.Context, cfg autobuild.SandboxConfig) (string, error) {
	ttl := d.config.DefaultTTLSeconds
	if ttl <= 0 {
		ttl = 900
	}
	readyTimeout := d.config.ReadyTimeout
	if readyTimeout <= 0 {
		readyTimeout = 60 * time.Second
	}

	opts := opensandbox.CodeInterpreterCreateOptions{
		ReadyTimeout: readyTimeout,
		Metadata: map[string]string{
			"created_by": "autobuild-sdk",
		},
	}
	// Merge caller-supplied labels into metadata
	for k, v := range cfg.Labels {
		opts.Metadata[k] = v
	}
	ttlCopy := ttl
	opts.TimeoutSeconds = &ttlCopy

	if img := d.config.DefaultImage; img != "" {
		opts.Image = img
	}
	if cfg.Image != "" {
		opts.Image = cfg.Image
	}

	ci, err := opensandbox.CreateCodeInterpreter(ctx, d.conn, opts)
	if err != nil {
		return "", fmt.Errorf("opensandbox create: %w", err)
	}
	id := ci.Sandbox.ID()
	d.instances[id] = ci
	return id, nil
}

// Exec runs a shell command inside the sandbox.
func (d *OpenSandboxDriver) Exec(ctx context.Context, sandboxID, command string) (autobuild.ExecResult, error) {
	ci, err := d.resolve(ctx, sandboxID)
	if err != nil {
		return autobuild.ExecResult{}, err
	}
	exec, err := ci.Sandbox.RunCommand(ctx, command, nil)
	if err != nil {
		return autobuild.ExecResult{}, fmt.Errorf("opensandbox exec: %w", err)
	}
	return toExecResult(exec), nil
}

// ExecCode runs code in the given language using the CodeInterpreter.
// Language examples: "python", "javascript", "bash".
// State persists across calls within the same sandboxID (Python vars, imports, etc.)
func (d *OpenSandboxDriver) ExecCode(ctx context.Context, sandboxID, language, code string) (autobuild.ExecResult, error) {
	ci, err := d.resolve(ctx, sandboxID)
	if err != nil {
		return autobuild.ExecResult{}, err
	}
	exec, err := ci.Execute(ctx, language, code, nil)
	if err != nil {
		return autobuild.ExecResult{}, fmt.Errorf("opensandbox exec code: %w", err)
	}

	var stdout strings.Builder
	stdout.WriteString(exec.Text())
	for _, res := range exec.Results {
		if t := res.Text(); t != "" {
			if stdout.Len() > 0 {
				stdout.WriteByte('\n')
			}
			stdout.WriteString(t)
		}
	}

	result := autobuild.ExecResult{Stdout: stdout.String()}
	if exec.Error != nil {
		result.Stderr = exec.Error.Name + ": " + exec.Error.Value
		result.ExitCode = 1
	}
	if exec.ExitCode != nil {
		result.ExitCode = *exec.ExitCode
	}
	return result, nil
}

// WriteFile uploads content to a path inside the sandbox.
// Uploads to a temp name then moves to the target path via mv.
func (d *OpenSandboxDriver) WriteFile(ctx context.Context, sandboxID, path, content string) error {
	ci, err := d.resolve(ctx, sandboxID)
	if err != nil {
		return err
	}
	// UploadFile uploads to the sandbox's upload directory.
	// We use the filename from path, then move it to the correct location.
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	filename := parts[len(parts)-1]

	err = ci.Sandbox.UploadFile(ctx, strings.NewReader(content), opensandbox.UploadFileOptions{
		FileName: filename,
	})
	if err != nil {
		return fmt.Errorf("opensandbox upload %s: %w", path, err)
	}

	// Move from upload dir to target path
	if len(parts) > 1 {
		dir := strings.Join(parts[:len(parts)-1], "/")
		mvCmd := fmt.Sprintf("mkdir -p %q && mv %q %q", dir, "/uploads/"+filename, path)
		_, execErr := ci.Sandbox.RunCommand(ctx, mvCmd, nil)
		if execErr != nil {
			return fmt.Errorf("opensandbox move to %s: %w", path, execErr)
		}
	}
	return nil
}

// ReadFile downloads a file from the sandbox.
func (d *OpenSandboxDriver) ReadFile(ctx context.Context, sandboxID, path string) (string, error) {
	ci, err := d.resolve(ctx, sandboxID)
	if err != nil {
		return "", err
	}
	rc, err := ci.Sandbox.DownloadFile(ctx, path, "")
	if err != nil {
		return "", fmt.Errorf("opensandbox read %s: %w", path, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("opensandbox read body: %w", err)
	}
	return string(data), nil
}

// Destroy terminates the sandbox.
func (d *OpenSandboxDriver) Destroy(ctx context.Context, sandboxID string) error {
	delete(d.instances, sandboxID)
	lc := opensandbox.NewLifecycleClient(d.conn.GetBaseURL(), d.conn.GetAPIKey())
	return lc.DeleteSandbox(ctx, sandboxID)
}

// Status returns the current lifecycle state.
func (d *OpenSandboxDriver) Status(ctx context.Context, sandboxID string) (autobuild.SandboxStatus, error) {
	lc := opensandbox.NewLifecycleClient(d.conn.GetBaseURL(), d.conn.GetAPIKey())
	info, err := lc.GetSandbox(ctx, sandboxID)
	if err != nil {
		return autobuild.SandboxStatusUnknown, fmt.Errorf("opensandbox status: %w", err)
	}
	return mapSandboxState(info.Status.State), nil
}

// IP returns the public URL of the sandbox on port 8080.
// Use GetEndpointURL for other ports (e.g. a web app serving HTML artifacts).
func (d *OpenSandboxDriver) IP(ctx context.Context, sandboxID string) (string, error) {
	url, err := d.GetEndpointURL(ctx, sandboxID, 8080)
	return url, err
}

// GetEndpointURL returns the public URL for a specific port inside the sandbox.
// Use port 8080 for web artifacts (e.g. HTML/React served from inside).
func (d *OpenSandboxDriver) GetEndpointURL(ctx context.Context, sandboxID string, port int) (string, error) {
	lc := opensandbox.NewLifecycleClient(d.conn.GetBaseURL(), d.conn.GetAPIKey())
	ep, err := lc.GetEndpoint(ctx, sandboxID, port, nil)
	if err != nil {
		return "", fmt.Errorf("opensandbox endpoint port %d: %w", port, err)
	}
	return ep.Endpoint, nil
}

// RenewExpiration extends the sandbox TTL to the given time.
func (d *OpenSandboxDriver) RenewExpiration(ctx context.Context, sandboxID string, expiresAt time.Time) error {
	lc := opensandbox.NewLifecycleClient(d.conn.GetBaseURL(), d.conn.GetAPIKey())
	_, err := lc.RenewExpiration(ctx, sandboxID, expiresAt)
	return err
}

// ── helpers ──────────────────────────────────────────────────────────────────

// resolve returns a cached CodeInterpreter or reconnects to an existing sandbox.
func (d *OpenSandboxDriver) resolve(ctx context.Context, sandboxID string) (*opensandbox.CodeInterpreter, error) {
	if ci, ok := d.instances[sandboxID]; ok {
		return ci, nil
	}
	sb, err := opensandbox.ConnectSandbox(ctx, d.conn, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("opensandbox connect %s: %w", sandboxID, err)
	}
	ci := &opensandbox.CodeInterpreter{Sandbox: sb}
	d.instances[sandboxID] = ci
	return ci, nil
}

func toExecResult(exec *opensandbox.Execution) autobuild.ExecResult {
	result := autobuild.ExecResult{Stdout: exec.Text()}
	stderrParts := make([]string, 0, len(exec.Stderr))
	for _, m := range exec.Stderr {
		stderrParts = append(stderrParts, m.Text)
	}
	result.Stderr = strings.Join(stderrParts, "\n")
	if exec.Error != nil {
		if result.Stderr != "" {
			result.Stderr += "\n"
		}
		result.Stderr += exec.Error.Name + ": " + exec.Error.Value
		result.ExitCode = 1
	}
	if exec.ExitCode != nil {
		result.ExitCode = *exec.ExitCode
	}
	return result
}

func mapSandboxState(state opensandbox.SandboxState) autobuild.SandboxStatus {
	switch state {
	case opensandbox.StateRunning:
		return autobuild.SandboxStatusRunning
	case opensandbox.StatePausing, opensandbox.StatePaused:
		return autobuild.SandboxStatusStopped
	case opensandbox.StatePending:
		return autobuild.SandboxStatusCreating
	case opensandbox.StateFailed:
		return autobuild.SandboxStatusError
	default:
		return autobuild.SandboxStatusUnknown
	}
}

var _ autobuild.SandboxDriver = (*OpenSandboxDriver)(nil)
