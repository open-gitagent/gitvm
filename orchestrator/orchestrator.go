package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/open-gitagent/gitvm/runtime"
)

// Config holds orchestrator configuration.
type Config struct {
	DataDir           string
	MaxVMs            int
	DefaultTimeoutSec int

	// Firecracker-specific (ignored when using Docker runtime)
	FirecrackerBin string
	HostInterface  string
}

// DefaultOrchestratorConfig returns sensible defaults.
func DefaultOrchestratorConfig() Config {
	return Config{
		DataDir:           "/tmp/gitvm",
		MaxVMs:            50,
		DefaultTimeoutSec: 300,
		FirecrackerBin:    "firecracker",
		HostInterface:     "eth0",
	}
}

// CreateRequest describes a sandbox to create.
type CreateRequest struct {
	Template string            `json:"template"`
	VCPUs    int               `json:"vcpus,omitempty"`
	MemoryMB int               `json:"memoryMB,omitempty"`
	Timeout  int               `json:"timeout,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	EnvVars  map[string]string `json:"envVars,omitempty"`
}

// Orchestrator manages sandboxes using a pluggable Runtime backend.
type Orchestrator struct {
	config  Config
	store   *VMStore
	runtime runtime.Runtime
	logger  *slog.Logger
}

// New creates a new Orchestrator with the given runtime (docker or firecracker).
func New(cfg Config, rt runtime.Runtime, logger *slog.Logger) *Orchestrator {
	return &Orchestrator{
		config:  cfg,
		store:   NewVMStore(),
		runtime: rt,
		logger:  logger,
	}
}

// RuntimeName returns which runtime backend is in use.
func (o *Orchestrator) RuntimeName() string {
	return o.runtime.Name()
}

// Create boots a new sandbox.
func (o *Orchestrator) Create(ctx context.Context, req CreateRequest) (*VMInstance, error) {
	if o.store.Count() >= o.config.MaxVMs {
		return nil, fmt.Errorf("max sandboxes reached (%d)", o.config.MaxVMs)
	}

	templateName := req.Template
	if templateName == "" {
		templateName = "base"
	}
	vcpus := 1
	if req.VCPUs > 0 {
		vcpus = req.VCPUs
	}
	memoryMB := 512
	if req.MemoryMB > 0 {
		memoryMB = req.MemoryMB
	}
	timeout := o.config.DefaultTimeoutSec
	if req.Timeout > 0 {
		timeout = req.Timeout
	}

	id := "sb-" + uuid.New().String()[:8]

	// Create sandbox via the runtime backend
	sb, err := o.runtime.Create(ctx, runtime.CreateOpts{
		ID:       id,
		Template: templateName,
		VCPUs:    vcpus,
		MemoryMB: memoryMB,
		EnvVars:  req.EnvVars,
	})
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}

	// Best-effort agent init with env vars
	if len(req.EnvVars) > 0 {
		initAgent(sb.AgentURL, req.EnvVars)
	}

	now := time.Now()
	instance := &VMInstance{
		ID:        id,
		State:     "running",
		Template:  templateName,
		VCPUs:     vcpus,
		MemoryMB:  memoryMB,
		Runtime:   o.runtime.Name(),
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(timeout) * time.Second),
		Metadata:  req.Metadata,
		AgentURL:  sb.AgentURL,
		GuestIP:   sb.IP,
	}
	o.store.Put(instance)

	o.logger.Info("sandbox created",
		"id", id,
		"runtime", o.runtime.Name(),
		"template", templateName,
		"ip", sb.IP,
	)
	return instance, nil
}

// Delete stops and removes a sandbox.
func (o *Orchestrator) Delete(ctx context.Context, id string) error {
	instance := o.store.Get(id)
	if instance == nil {
		return fmt.Errorf("sandbox not found: %s", id)
	}

	if err := o.runtime.Destroy(ctx, id); err != nil {
		o.logger.Warn("error destroying sandbox", "id", id, "error", err)
	}

	o.store.Delete(id)
	o.logger.Info("sandbox deleted", "id", id)
	return nil
}

// Pause pauses a running sandbox.
func (o *Orchestrator) Pause(ctx context.Context, id string) error {
	instance := o.store.Get(id)
	if instance == nil {
		return fmt.Errorf("sandbox not found: %s", id)
	}
	if instance.State != "running" {
		return fmt.Errorf("sandbox %s is not running (status: %s)", id, instance.State)
	}
	if err := o.runtime.Pause(ctx, id); err != nil {
		return err
	}
	instance.State = "paused"
	o.store.Put(instance)
	return nil
}

// Resume resumes a paused sandbox.
func (o *Orchestrator) Resume(ctx context.Context, id string) error {
	instance := o.store.Get(id)
	if instance == nil {
		return fmt.Errorf("sandbox not found: %s", id)
	}
	if instance.State != "paused" {
		return fmt.Errorf("sandbox %s is not paused (status: %s)", id, instance.State)
	}
	if err := o.runtime.Resume(ctx, id); err != nil {
		return err
	}
	instance.State = "running"
	o.store.Put(instance)
	return nil
}

// Get returns a sandbox instance by ID.
func (o *Orchestrator) Get(id string) *VMInstance {
	return o.store.Get(id)
}

// List returns all sandbox instances.
func (o *Orchestrator) List() []*VMInstance {
	return o.store.List()
}

// Snapshot saves the current state of a sandbox.
func (o *Orchestrator) Snapshot(ctx context.Context, sandboxID, snapshotID string) error {
	instance := o.store.Get(sandboxID)
	if instance == nil {
		return fmt.Errorf("sandbox not found: %s", sandboxID)
	}
	if instance.State != "running" && instance.State != "paused" {
		return fmt.Errorf("sandbox %s must be running or paused to snapshot (status: %s)", sandboxID, instance.State)
	}
	if err := o.runtime.Snapshot(ctx, sandboxID, snapshotID); err != nil {
		return err
	}
	o.logger.Info("snapshot created", "sandbox", sandboxID, "snapshot", snapshotID)
	return nil
}

// Restore creates a new sandbox from a snapshot.
func (o *Orchestrator) Restore(ctx context.Context, snapshotID string, timeout int) (*VMInstance, error) {
	if o.store.Count() >= o.config.MaxVMs {
		return nil, fmt.Errorf("max sandboxes reached (%d)", o.config.MaxVMs)
	}
	if timeout <= 0 {
		timeout = o.config.DefaultTimeoutSec
	}

	newID := "sb-" + uuid.New().String()[:8]

	sb, err := o.runtime.Restore(ctx, snapshotID, newID)
	if err != nil {
		return nil, fmt.Errorf("restore from snapshot: %w", err)
	}

	now := time.Now()
	instance := &VMInstance{
		ID:        newID,
		State:     "running",
		Template:  "snapshot:" + snapshotID,
		Runtime:   o.runtime.Name(),
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(timeout) * time.Second),
		AgentURL:  sb.AgentURL,
		GuestIP:   sb.IP,
	}
	o.store.Put(instance)

	o.logger.Info("sandbox restored from snapshot", "id", newID, "snapshot", snapshotID)
	return instance, nil
}

// DeleteSnapshot removes a saved snapshot.
func (o *Orchestrator) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	return o.runtime.DeleteSnapshot(ctx, snapshotID)
}

// ListSnapshots returns all snapshots, optionally filtered by sandbox ID.
func (o *Orchestrator) ListSnapshots(ctx context.Context, sandboxID string) ([]runtime.SnapshotInfo, error) {
	return o.runtime.ListSnapshots(ctx, sandboxID)
}

// --- Internal ---

func initAgent(agentURL string, envVars map[string]string) {
	data, _ := json.Marshal(map[string]interface{}{"envVars": envVars})
	resp, err := http.Post(agentURL+"/init", "application/json", bytes.NewReader(data))
	if err != nil {
		return
	}
	resp.Body.Close()
}
