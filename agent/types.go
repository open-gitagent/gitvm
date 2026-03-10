package agent

// InitRequest configures the agent after VM boot.
type InitRequest struct {
	EnvVars    map[string]string `json:"envVars,omitempty"`
	User       string            `json:"user,omitempty"`
	WorkDir    string            `json:"workDir,omitempty"`
	AccessToken string           `json:"accessToken,omitempty"`
}

// ExecRequest describes a command to execute.
type ExecRequest struct {
	Command string            `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // seconds, 0 = no timeout
}

// ExecResult holds the output of a completed command.
type ExecResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// FileInfo describes a file or directory entry.
type FileInfo struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
}

// ErrorResponse is returned on failure.
type ErrorResponse struct {
	Error string `json:"error"`
}
