package firecracker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// VMState represents the lifecycle state of a VM.
type VMState string

const (
	VMStateIdle     VMState = "idle"
	VMStateStarting VMState = "starting"
	VMStateRunning  VMState = "running"
	VMStateStopped  VMState = "stopped"
)

// VM manages a single Firecracker microVM.
type VM struct {
	Config VMConfig
	State  VMState
	PID    int

	fc     *FirecrackerClient
	cmd    *exec.Cmd
	logger *slog.Logger
}

// NewVM creates a new VM instance with the given config.
func NewVM(cfg VMConfig, logger *slog.Logger) *VM {
	return &VM{
		Config: cfg,
		State:  VMStateIdle,
		logger: logger,
	}
}

// Start boots the Firecracker microVM.
// 1. Prepare VM directory and rootfs copy
// 2. Start the firecracker process
// 3. Configure VM via socket API
// 4. Boot the VM
func (vm *VM) Start(ctx context.Context) error {
	if vm.State == VMStateRunning {
		return fmt.Errorf("VM %s is already running", vm.Config.ID)
	}

	vm.State = VMStateStarting
	vm.logger.Info("starting VM", "id", vm.Config.ID)

	// Validate config
	if err := vm.Config.Validate(); err != nil {
		vm.State = VMStateStopped
		return fmt.Errorf("invalid config: %w", err)
	}

	// Create VM directory
	vmDir := vm.Config.VMDir()
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		vm.State = VMStateStopped
		return fmt.Errorf("create VM dir: %w", err)
	}

	// Copy rootfs for this VM (copy-on-write if supported, otherwise full copy)
	if err := vm.copyRootfs(); err != nil {
		vm.State = VMStateStopped
		return fmt.Errorf("copy rootfs: %w", err)
	}

	// Start firecracker process
	socketPath := vm.Config.SocketPathForVM()
	if err := vm.startProcess(ctx, socketPath); err != nil {
		vm.State = VMStateStopped
		return fmt.Errorf("start firecracker: %w", err)
	}

	// Wait for socket to be available
	if err := vm.waitForSocket(socketPath); err != nil {
		vm.Stop(ctx)
		return fmt.Errorf("wait for socket: %w", err)
	}

	// Create Firecracker API client
	vm.fc = NewFirecrackerClient(socketPath)

	// Configure VM
	if err := vm.configureVM(); err != nil {
		vm.Stop(ctx)
		return fmt.Errorf("configure VM: %w", err)
	}

	// Boot
	if err := vm.fc.StartInstance(); err != nil {
		vm.Stop(ctx)
		return fmt.Errorf("boot VM: %w", err)
	}

	vm.State = VMStateRunning
	vm.logger.Info("VM started", "id", vm.Config.ID, "pid", vm.PID)
	return nil
}

// Stop terminates the Firecracker process and cleans up.
func (vm *VM) Stop(ctx context.Context) error {
	vm.logger.Info("stopping VM", "id", vm.Config.ID)

	if vm.cmd != nil && vm.cmd.Process != nil {
		// Send SIGTERM first for graceful shutdown
		_ = vm.cmd.Process.Signal(syscall.SIGTERM)

		// Wait briefly, then force kill
		done := make(chan error, 1)
		go func() { done <- vm.cmd.Wait() }()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = vm.cmd.Process.Kill()
			<-done
		}
	}

	// Cleanup VM directory
	vmDir := vm.Config.VMDir()
	if err := os.RemoveAll(vmDir); err != nil {
		vm.logger.Warn("failed to cleanup VM dir", "dir", vmDir, "error", err)
	}

	vm.State = VMStateStopped
	vm.logger.Info("VM stopped", "id", vm.Config.ID)
	return nil
}

// --- Internal ---

func (vm *VM) startProcess(ctx context.Context, socketPath string) error {
	// Remove stale socket
	os.Remove(socketPath)

	logFile := filepath.Join(vm.Config.VMDir(), "firecracker.log")
	lf, err := os.Create(logFile)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}

	vm.cmd = exec.CommandContext(ctx, vm.Config.FirecrackerBin,
		"--api-sock", socketPath,
	)
	vm.cmd.Stdout = lf
	vm.cmd.Stderr = lf

	if err := vm.cmd.Start(); err != nil {
		lf.Close()
		return fmt.Errorf("start firecracker binary: %w", err)
	}

	vm.PID = vm.cmd.Process.Pid
	return nil
}

func (vm *VM) waitForSocket(socketPath string) error {
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(socketPath); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("socket %s did not appear after 5s", socketPath)
}

func (vm *VM) configureVM() error {
	// 1. Machine config
	if err := vm.fc.SetMachineConfig(MachineConfiguration{
		VCPUCount:  vm.Config.VCPUs,
		MemSizeMib: vm.Config.MemoryMB,
		HtEnabled:  false,
	}); err != nil {
		return fmt.Errorf("set machine config: %w", err)
	}

	// 2. Boot source
	if err := vm.fc.SetBootSource(BootSource{
		KernelImagePath: vm.Config.KernelPath,
		BootArgs:        vm.Config.KernelArgsWithNetwork(),
	}); err != nil {
		return fmt.Errorf("set boot source: %w", err)
	}

	// 3. Rootfs drive
	if err := vm.fc.AddDrive(Drive{
		DriveID:      "rootfs",
		PathOnHost:   vm.Config.RootfsCopyPath(),
		IsRootDevice: true,
		IsReadOnly:   false,
	}); err != nil {
		return fmt.Errorf("add rootfs drive: %w", err)
	}

	// 4. Network interface (if configured)
	if vm.Config.TAPDevice != "" {
		if err := vm.fc.AddNetworkInterface(NetworkInterface{
			IfaceID:     "eth0",
			GuestMAC:    vm.Config.GuestMAC,
			HostDevName: vm.Config.TAPDevice,
		}); err != nil {
			return fmt.Errorf("add network interface: %w", err)
		}
	}

	return nil
}

func (vm *VM) copyRootfs() error {
	src := vm.Config.RootfsPath
	dst := vm.Config.RootfsCopyPath()

	// Try cp --reflink=auto for CoW on supported filesystems (btrfs, xfs)
	cpCmd := exec.Command("cp", "--reflink=auto", src, dst)
	if err := cpCmd.Run(); err != nil {
		// Fallback to manual copy
		return vm.manualCopyRootfs(src, dst)
	}
	return nil
}

func (vm *VM) manualCopyRootfs(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
