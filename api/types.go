package api

// CreateVMRequest is the JSON body for POST /v1/vms.
type CreateVMRequest struct {
	Template string            `json:"template,omitempty"`
	VCPUs    int               `json:"vcpus,omitempty"`
	MemoryMB int               `json:"memoryMB,omitempty"`
	Timeout  int               `json:"timeout,omitempty"` // seconds
	Metadata map[string]string `json:"metadata,omitempty"`
	EnvVars  map[string]string `json:"envVars,omitempty"`
}

// VMResponse is the JSON response for VM operations.
type VMResponse struct {
	ID        string            `json:"id"`
	State     string            `json:"state"`
	Template  string            `json:"template"`
	VCPUs     int               `json:"vcpus"`
	MemoryMB  int               `json:"memoryMB"`
	CreatedAt string            `json:"createdAt"`
	ExpiresAt string            `json:"expiresAt"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	AgentURL  string            `json:"agentURL"`
	GuestIP   string            `json:"guestIP"`
}

// ExecRequest is the JSON body for POST /v1/vms/:id/exec.
type ExecRequest struct {
	Command string            `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

// ExecResponse is the JSON response for exec operations.
type ExecResponse struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// ErrorResponse is returned on errors.
type ErrorResponse struct {
	Error string `json:"error"`
}

// HealthResponse is returned by GET /health.
type HealthResponse struct {
	Status string `json:"status"`
	VMs    int    `json:"vms"`
}
