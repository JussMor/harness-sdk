package autobuild

import "context"

// SandboxDriver abstracts command execution and file I/O inside an isolated
// environment (container, VM, remote host, etc.).
type SandboxDriver interface {
	// Create provisions a new sandbox and returns its unique ID.
	Create(ctx context.Context, cfg SandboxConfig) (id string, err error)

	// Exec runs a shell command inside the sandbox identified by id.
	Exec(ctx context.Context, id string, command string) (ExecResult, error)

	// WriteFile writes content to path inside the sandbox.
	WriteFile(ctx context.Context, id string, path string, content string) error

	// ReadFile returns the contents of path inside the sandbox.
	ReadFile(ctx context.Context, id string, path string) (string, error)

	// Destroy tears down the sandbox and releases its resources.
	Destroy(ctx context.Context, id string) error

	// Status returns the current status of the sandbox.
	Status(ctx context.Context, id string) (SandboxStatus, error)

	// IP returns the reachable IP address of the sandbox.
	IP(ctx context.Context, id string) (string, error)
}

// SandboxConfig holds parameters for provisioning a new sandbox.
type SandboxConfig struct {
	// Image is the base image or template (e.g. Docker image name).
	Image string `json:"image,omitempty"`

	// DefaultCwd is the working directory for commands.
	DefaultCwd string `json:"default_cwd,omitempty"`

	// Env is a map of environment variables injected into the sandbox.
	Env map[string]string `json:"env,omitempty"`

	// Labels are metadata key-value pairs for filtering and tagging sandboxes.
	Labels map[string]string `json:"labels,omitempty"`

	// Volumes are persistent storage mounts (Docker named volumes or PVCs).
	// In Docker runtime, PVC.ClaimName maps to a named volume.
	// In Kubernetes runtime, it maps to a PersistentVolumeClaim.
	Volumes []Volume `json:"volumes,omitempty"`
}

// Volume represents a persistent storage mount in a sandbox.
// Supports Docker named volumes (pvc backend) or host paths (host backend).
type Volume struct {
	// Name is the unique identifier for this volume within the sandbox.
	Name string `json:"name"`

	// MountPath is the path inside the sandbox where the volume is mounted (e.g. /mnt/data).
	MountPath string `json:"mount_path"`

	// ReadOnly indicates whether the mount is read-only. Default is false (read-write).
	ReadOnly bool `json:"read_only,omitempty"`

	// PVC (PersistentVolumeClaim) backend: used for Docker named volumes and Kubernetes PVCs.
	// In Docker runtime, ClaimName maps to a Docker named volume.
	// In Kubernetes, it maps to a PersistentVolumeClaim name.
	PVC *PVCVolumeSource `json:"pvc,omitempty"`

	// SubPath mounts only a subdirectory within the volume (consistent with Kubernetes subPath).
	// Useful for mounting only a portion of a shared volume (e.g., datasets/train from a larger volume).
	SubPath string `json:"sub_path,omitempty"`
}

// PVCVolumeSource references a PersistentVolumeClaim or Docker named volume.
type PVCVolumeSource struct {
	// ClaimName is the name of the PVC (Kubernetes) or Docker named volume.
	ClaimName string `json:"claim_name"`
}

// ExecResult is the output of a command executed inside a sandbox.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// SandboxStatus represents the lifecycle state of a sandbox.
type SandboxStatus string

const (
	SandboxStatusRunning  SandboxStatus = "running"
	SandboxStatusStopped  SandboxStatus = "stopped"
	SandboxStatusCreating SandboxStatus = "creating"
	SandboxStatusError    SandboxStatus = "error"
	SandboxStatusUnknown  SandboxStatus = "unknown"
)
