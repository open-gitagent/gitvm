package runtime

import "context"

// Runtime is the pluggable interface for sandbox backends.
// Implementations: DockerRuntime (containers) and FirecrackerRuntime (microVMs).
type Runtime interface {
	// Name returns the runtime type ("docker" or "firecracker").
	Name() string

	// Create creates a new sandbox and returns its info.
	Create(ctx context.Context, opts CreateOpts) (*Sandbox, error)

	// Start starts a stopped/paused sandbox.
	Start(ctx context.Context, id string) error

	// Stop stops a running sandbox (preserves state).
	Stop(ctx context.Context, id string) error

	// Destroy removes a sandbox completely.
	Destroy(ctx context.Context, id string) error

	// Pause pauses a running sandbox.
	Pause(ctx context.Context, id string) error

	// Resume resumes a paused sandbox.
	Resume(ctx context.Context, id string) error

	// Get returns sandbox info by ID.
	Get(ctx context.Context, id string) (*Sandbox, error)

	// List returns all sandboxes managed by this runtime.
	List(ctx context.Context) ([]*Sandbox, error)

	// AgentURL returns the URL to reach the guest agent for a sandbox.
	AgentURL(ctx context.Context, id string) (string, error)

	// Snapshot saves the current state of a sandbox and returns a snapshot ID.
	Snapshot(ctx context.Context, id string, snapshotID string) error

	// Restore creates a new sandbox from a previously saved snapshot.
	Restore(ctx context.Context, snapshotID string, newID string) (*Sandbox, error)

	// DeleteSnapshot removes a saved snapshot.
	DeleteSnapshot(ctx context.Context, snapshotID string) error

	// ListSnapshots returns all snapshot IDs for a sandbox.
	ListSnapshots(ctx context.Context, id string) ([]SnapshotInfo, error)
}

// CreateOpts are the options for creating a sandbox.
type CreateOpts struct {
	ID       string
	Template string // image name for Docker, rootfs template for Firecracker
	VCPUs    int
	MemoryMB int
	EnvVars  map[string]string
}

// Sandbox represents a running sandbox instance.
type Sandbox struct {
	ID       string
	State    string // "creating", "running", "paused", "stopped"
	Runtime  string // "docker" or "firecracker"
	IP       string // internal IP to reach the agent
	AgentURL string // full URL e.g. "http://172.17.0.2:49983"
}

// SnapshotInfo describes a saved snapshot.
type SnapshotInfo struct {
	ID        string `json:"id"`
	SandboxID string `json:"sandboxId"`
	CreatedAt string `json:"createdAt"`
	SizeMB    int64  `json:"sizeMB,omitempty"`
}
