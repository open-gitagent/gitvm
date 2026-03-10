package firecracker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/open-gitagent/gitvm/agent"
	"github.com/open-gitagent/gitvm/network"
	"github.com/open-gitagent/gitvm/runtime"
	"github.com/open-gitagent/gitvm/template"
)

// FirecrackerRuntime creates sandboxes as Firecracker microVMs.
// Requires Linux with /dev/kvm.
type FirecrackerRuntime struct {
	config    Config
	netPool   *network.Pool
	tmplCache *template.Cache
	vms       map[string]*vmInfo
	mu        sync.RWMutex
	logger    *slog.Logger
}

type vmInfo struct {
	vm   *VM
	slot *network.Slot
	sb   *runtime.Sandbox
}

// Config for the Firecracker runtime.
type Config struct {
	DataDir        string
	FirecrackerBin string
	HostInterface  string
	MaxVMs         int
}

// New creates a new FirecrackerRuntime.
func New(cfg Config, tmplCache *template.Cache, logger *slog.Logger) *FirecrackerRuntime {
	if cfg.MaxVMs <= 0 {
		cfg.MaxVMs = 50
	}
	return &FirecrackerRuntime{
		config:    cfg,
		netPool:   network.NewPool(cfg.MaxVMs, cfg.HostInterface, logger),
		tmplCache: tmplCache,
		vms:       make(map[string]*vmInfo),
		logger:    logger,
	}
}

func (f *FirecrackerRuntime) Name() string { return "firecracker" }

func (f *FirecrackerRuntime) Create(ctx context.Context, opts runtime.CreateOpts) (*runtime.Sandbox, error) {
	templateName := opts.Template
	if templateName == "" {
		templateName = "base"
	}

	if !f.tmplCache.Exists(templateName) {
		return nil, fmt.Errorf("template %q not found", templateName)
	}

	// Allocate network
	slot, err := f.netPool.Allocate()
	if err != nil {
		return nil, fmt.Errorf("allocate network: %w", err)
	}

	// Build VM config
	vmCfg := DefaultConfig()
	vmCfg.ID = opts.ID
	vmCfg.KernelPath = f.tmplCache.KernelPath(templateName)
	vmCfg.RootfsPath = f.tmplCache.TemplatePath(templateName)
	vmCfg.DataDir = f.config.DataDir
	vmCfg.FirecrackerBin = f.config.FirecrackerBin
	vmCfg.TAPDevice = slot.TAPName
	vmCfg.GuestIP = slot.GuestIP
	vmCfg.HostIP = slot.HostIP
	vmCfg.GuestMAC = slot.GuestMAC
	vmCfg.GatewayIP = slot.HostIP

	if opts.VCPUs > 0 {
		vmCfg.VCPUs = opts.VCPUs
	}
	if opts.MemoryMB > 0 {
		vmCfg.MemoryMB = opts.MemoryMB
	}

	// Boot the VM
	vm := NewVM(vmCfg, f.logger)
	if err := vm.Start(ctx); err != nil {
		f.netPool.Release(slot)
		return nil, fmt.Errorf("start VM: %w", err)
	}

	agentURL := fmt.Sprintf("http://%s:%d", slot.GuestIP, agent.DefaultPort)

	// Wait for agent
	if err := waitForAgent(agentURL); err != nil {
		vm.Stop(ctx)
		f.netPool.Release(slot)
		return nil, fmt.Errorf("agent not ready: %w", err)
	}

	sb := &runtime.Sandbox{
		ID:       opts.ID,
		State:    "running",
		Runtime:  "firecracker",
		IP:       slot.GuestIP,
		AgentURL: agentURL,
	}

	f.mu.Lock()
	f.vms[opts.ID] = &vmInfo{vm: vm, slot: slot, sb: sb}
	f.mu.Unlock()

	return sb, nil
}

func (f *FirecrackerRuntime) Start(ctx context.Context, id string) error {
	// Firecracker VMs can't be restarted — they must be recreated
	return fmt.Errorf("firecracker VMs cannot be restarted; destroy and create a new one")
}

func (f *FirecrackerRuntime) Stop(ctx context.Context, id string) error {
	return f.Destroy(ctx, id)
}

func (f *FirecrackerRuntime) Destroy(ctx context.Context, id string) error {
	f.mu.Lock()
	info, ok := f.vms[id]
	if ok {
		delete(f.vms, id)
	}
	f.mu.Unlock()

	if !ok {
		return fmt.Errorf("sandbox %s not found", id)
	}

	if info.vm != nil {
		info.vm.Stop(ctx)
	}
	if info.slot != nil {
		f.netPool.Release(info.slot)
	}
	return nil
}

func (f *FirecrackerRuntime) Pause(ctx context.Context, id string) error {
	// Firecracker supports pause via the API
	f.mu.RLock()
	info, ok := f.vms[id]
	f.mu.RUnlock()
	if !ok {
		return fmt.Errorf("sandbox %s not found", id)
	}
	info.sb.State = "paused"
	return nil
}

func (f *FirecrackerRuntime) Resume(ctx context.Context, id string) error {
	f.mu.RLock()
	info, ok := f.vms[id]
	f.mu.RUnlock()
	if !ok {
		return fmt.Errorf("sandbox %s not found", id)
	}
	info.sb.State = "running"
	return nil
}

func (f *FirecrackerRuntime) Get(ctx context.Context, id string) (*runtime.Sandbox, error) {
	f.mu.RLock()
	info, ok := f.vms[id]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("sandbox %s not found", id)
	}
	return info.sb, nil
}

func (f *FirecrackerRuntime) List(ctx context.Context) ([]*runtime.Sandbox, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var list []*runtime.Sandbox
	for _, info := range f.vms {
		list = append(list, info.sb)
	}
	return list, nil
}

func (f *FirecrackerRuntime) AgentURL(ctx context.Context, id string) (string, error) {
	f.mu.RLock()
	info, ok := f.vms[id]
	f.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("sandbox %s not found", id)
	}
	return info.sb.AgentURL, nil
}

// Snapshot is not yet supported for Firecracker (requires snapshot API integration).
func (f *FirecrackerRuntime) Snapshot(ctx context.Context, id string, snapshotID string) error {
	return fmt.Errorf("snapshots not yet implemented for firecracker runtime")
}

func (f *FirecrackerRuntime) Restore(ctx context.Context, snapshotID string, newID string) (*runtime.Sandbox, error) {
	return nil, fmt.Errorf("snapshots not yet implemented for firecracker runtime")
}

func (f *FirecrackerRuntime) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	return fmt.Errorf("snapshots not yet implemented for firecracker runtime")
}

func (f *FirecrackerRuntime) ListSnapshots(ctx context.Context, id string) ([]runtime.SnapshotInfo, error) {
	return nil, fmt.Errorf("snapshots not yet implemented for firecracker runtime")
}

func waitForAgent(agentURL string) error {
	healthURL := agentURL + "/health"
	for i := 0; i < 60; i++ {
		resp, err := http.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("agent at %s not healthy after 30s", agentURL)
}

var _ runtime.Runtime = (*FirecrackerRuntime)(nil)
