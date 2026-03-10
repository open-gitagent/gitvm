package controlplane

import "time"

// --- Core Domain Types ---

// Node represents a registered agent node (a cloud VM running gitvm-node).
type Node struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Address      string    `json:"address"`      // e.g., "http://54.23.1.100:9090"
	PublicIP     string    `json:"publicIp"`
	Provider     string    `json:"provider"`      // "aws", "gcp", "azure", "custom"
	ProviderID   string    `json:"providerId"`    // cloud instance ID (i-xxx, etc.)
	Region       string    `json:"region"`
	InstanceType string    `json:"instanceType"`
	Status       string    `json:"status"`        // "provisioning", "online", "draining", "offline", "terminating"
	MaxSandboxes int       `json:"maxSandboxes"`
	Running      int       `json:"runningSandboxes"`
	LastSeen     time.Time `json:"lastSeen"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Sandbox represents a running sandbox VM on a node.
type Sandbox struct {
	ID         string            `json:"sandboxId"`
	ProjectID  string            `json:"projectId"`
	NodeID     string            `json:"nodeId"`
	Template   string            `json:"template"`
	Status     string            `json:"status"` // "creating", "running", "paused", "stopped"
	VCPUs      int               `json:"vcpus"`
	MemoryMB   int               `json:"memoryMB"`
	HostIP     string            `json:"hostIp"`
	TimeoutSec int               `json:"timeoutSec"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	ExpiresAt  time.Time         `json:"expiresAt"`
}

// Project groups sandboxes under an API key.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	APIKey    string    `json:"apiKey"`
	OwnerID   string    `json:"ownerId"`
	CreatedAt time.Time `json:"createdAt"`
}

// User represents a dashboard user.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
}

// CloudPool defines a pool of cloud nodes for auto-scaling.
type CloudPool struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`     // "aws", "gcp", "azure"
	Region       string `json:"region"`
	InstanceType string `json:"instanceType"` // e.g., "m5.metal", "n2-standard-16" (auto-selected if empty)
	Runtime      string `json:"runtime"`      // "firecracker" (default) or "docker"
	MinNodes     int    `json:"minNodes"`
	MaxNodes     int    `json:"maxNodes"`
	CurrentNodes int    `json:"currentNodes"`
	// Provider credentials are stored separately
}

// --- API Request/Response Types ---

type CreateSandboxRequest struct {
	Template string            `json:"template,omitempty"`
	VCPUs    int               `json:"vcpus,omitempty"`
	MemoryMB int               `json:"memoryMB,omitempty"`
	Timeout  int               `json:"timeout,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	EnvVars  map[string]string `json:"envVars,omitempty"`
}

type CreateSandboxResponse struct {
	SandboxID string `json:"sandboxId"`
	Status    string `json:"status"`
	HostIP    string `json:"hostIp"`
}

type SandboxListResponse struct {
	Sandboxes []Sandbox `json:"sandboxes"`
}

type ExecRequest struct {
	Command string            `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

type ExecResponse struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type NodeRegisterRequest struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	MaxSandboxes int    `json:"maxSandboxes"`
	Region       string `json:"region,omitempty"`
	NodeKey      string `json:"nodeKey"`
}

type NodeHeartbeatRequest struct {
	RunningSandboxes int `json:"runningSandboxes"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type HealthResponse struct {
	Status string `json:"status"`
	Nodes  int    `json:"nodes"`
	VMs    int    `json:"vms"`
}
