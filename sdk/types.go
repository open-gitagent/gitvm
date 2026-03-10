package sdk

// MachineState matches gitmachine-go's MachineState type.
type MachineState string

const (
	StateIdle    MachineState = "idle"
	StateRunning MachineState = "running"
	StatePaused  MachineState = "paused"
	StateStopped MachineState = "stopped"
)

// ExecutionResult matches gitmachine-go's ExecutionResult type.
type ExecutionResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// ExecuteOptions matches gitmachine-go's ExecuteOptions type.
type ExecuteOptions struct {
	Cwd      string
	Env      map[string]string
	Timeout  int
	OnStdout func(data string)
	OnStderr func(data string)
}

// GitVMConfig holds configuration for creating a GitVMMachine.
type GitVMConfig struct {
	// ServerURL is the gitvm API server URL (e.g., "http://localhost:8080").
	ServerURL string

	// APIKey is the API key for authentication.
	APIKey string

	// Template is the VM template name. Default: "base".
	Template string

	// VCPUs is the number of virtual CPUs. Default: 1.
	VCPUs int

	// MemoryMB is the amount of RAM in MiB. Default: 512.
	MemoryMB int

	// Timeout is the VM lifetime in seconds. Default: 300.
	Timeout int

	// EnvVars are environment variables passed to the VM.
	EnvVars map[string]string

	// Metadata is key-value metadata attached to the VM.
	Metadata map[string]string
}
