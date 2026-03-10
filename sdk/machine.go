package sdk

import (
	"context"
	"fmt"
)

// GitVMMachine implements the gitmachine-go Machine interface
// using a gitvm API server as the backend.
type GitVMMachine struct {
	config GitVMConfig
	client *Client
	vmID   string
	state  MachineState
}

// NewGitVMMachine creates a new machine backed by a gitvm server.
func NewGitVMMachine(config GitVMConfig) *GitVMMachine {
	if config.ServerURL == "" {
		config.ServerURL = "http://localhost:8080"
	}
	return &GitVMMachine{
		config: config,
		client: NewClient(config.ServerURL, config.APIKey),
		state:  StateIdle,
	}
}

// ID returns the VM ID, or empty string if not started.
func (m *GitVMMachine) ID() string {
	return m.vmID
}

// State returns the current lifecycle state.
func (m *GitVMMachine) State() MachineState {
	return m.state
}

// Start creates and boots a new VM.
func (m *GitVMMachine) Start(ctx context.Context) error {
	if m.state == StateRunning {
		return fmt.Errorf("machine is already running")
	}

	req := map[string]interface{}{}
	if m.config.Template != "" {
		req["template"] = m.config.Template
	}
	if m.config.VCPUs > 0 {
		req["vcpus"] = m.config.VCPUs
	}
	if m.config.MemoryMB > 0 {
		req["memoryMB"] = m.config.MemoryMB
	}
	if m.config.Timeout > 0 {
		req["timeout"] = m.config.Timeout
	}
	if m.config.EnvVars != nil {
		req["envVars"] = m.config.EnvVars
	}
	if m.config.Metadata != nil {
		req["metadata"] = m.config.Metadata
	}

	resp, err := m.client.CreateVM(ctx, req)
	if err != nil {
		return fmt.Errorf("create VM: %w", err)
	}

	if id, ok := resp["id"].(string); ok {
		m.vmID = id
	}
	m.state = StateRunning
	return nil
}

// Pause suspends the machine (not yet implemented in gitvm).
func (m *GitVMMachine) Pause(ctx context.Context) error {
	m.state = StatePaused
	return nil
}

// Resume restores a paused machine (not yet implemented in gitvm).
func (m *GitVMMachine) Resume(ctx context.Context) error {
	m.state = StateRunning
	return nil
}

// Stop terminates the VM.
func (m *GitVMMachine) Stop(ctx context.Context) error {
	if m.vmID == "" {
		m.state = StateStopped
		return nil
	}

	if err := m.client.DeleteVM(ctx, m.vmID); err != nil {
		return fmt.Errorf("delete VM: %w", err)
	}

	m.state = StateStopped
	return nil
}

// Execute runs a command inside the VM and returns the result.
func (m *GitVMMachine) Execute(ctx context.Context, command string, opts *ExecuteOptions) (*ExecutionResult, error) {
	if m.state != StateRunning {
		return nil, fmt.Errorf("machine is not running (state: %s)", m.state)
	}

	return m.client.Exec(ctx, m.vmID, command, opts)
}

// ReadFile reads a file from inside the VM.
func (m *GitVMMachine) ReadFile(ctx context.Context, path string) (string, error) {
	if m.state != StateRunning {
		return "", fmt.Errorf("machine is not running")
	}
	return m.client.ReadFile(ctx, m.vmID, path)
}

// WriteFile writes content to a file inside the VM.
func (m *GitVMMachine) WriteFile(ctx context.Context, path string, content []byte) error {
	if m.state != StateRunning {
		return fmt.Errorf("machine is not running")
	}
	return m.client.WriteFile(ctx, m.vmID, path, content)
}

// ListFiles lists files in a directory inside the VM.
func (m *GitVMMachine) ListFiles(ctx context.Context, path string) ([]string, error) {
	if m.state != StateRunning {
		return nil, fmt.Errorf("machine is not running")
	}
	return m.client.ListFiles(ctx, m.vmID, path)
}
