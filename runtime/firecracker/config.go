package firecracker

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// VMConfig holds all configuration for a Firecracker microVM.
type VMConfig struct {
	// ID is the unique identifier for this VM.
	ID string

	// VCPUs is the number of virtual CPUs. Default: 1.
	VCPUs int

	// MemoryMB is the amount of RAM in MiB. Default: 512.
	MemoryMB int

	// KernelPath is the path to the vmlinux kernel image.
	KernelPath string

	// RootfsPath is the path to the rootfs ext4 image (will be copied per-VM).
	RootfsPath string

	// KernelArgs are the kernel boot arguments.
	KernelArgs string

	// SocketPath is the Unix socket for the Firecracker API.
	SocketPath string

	// FirecrackerBin is the path to the firecracker binary.
	FirecrackerBin string

	// DataDir is the directory for VM-specific files (rootfs copies, sockets, logs).
	DataDir string

	// Network configuration.
	TAPDevice string
	GuestIP   string
	HostIP    string
	GuestMAC  string
	GatewayIP string
	Netmask   string
}

// DefaultConfig returns a VMConfig with sensible defaults.
func DefaultConfig() VMConfig {
	id := uuid.New().String()[:8]
	return VMConfig{
		ID:             id,
		VCPUs:          1,
		MemoryMB:       512,
		KernelArgs:     "console=ttyS0 reboot=k panic=1 pci=off init=/sbin/init",
		FirecrackerBin: "firecracker",
		DataDir:        filepath.Join(os.TempDir(), "gitvm"),
		Netmask:        "255.255.255.252",
	}
}

// Validate checks that all required fields are set.
func (c *VMConfig) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("VM ID is required")
	}
	if c.KernelPath == "" {
		return fmt.Errorf("KernelPath is required")
	}
	if c.RootfsPath == "" {
		return fmt.Errorf("RootfsPath is required")
	}
	if _, err := os.Stat(c.KernelPath); err != nil {
		return fmt.Errorf("kernel not found at %s: %w", c.KernelPath, err)
	}
	if _, err := os.Stat(c.RootfsPath); err != nil {
		return fmt.Errorf("rootfs not found at %s: %w", c.RootfsPath, err)
	}
	return nil
}

// SocketPathForVM returns the socket path for this VM.
func (c *VMConfig) SocketPathForVM() string {
	if c.SocketPath != "" {
		return c.SocketPath
	}
	return filepath.Join(c.DataDir, c.ID, "firecracker.sock")
}

// RootfsCopyPath returns the path for this VM's rootfs copy.
func (c *VMConfig) RootfsCopyPath() string {
	return filepath.Join(c.DataDir, c.ID, "rootfs.ext4")
}

// VMDir returns the directory for this VM's files.
func (c *VMConfig) VMDir() string {
	return filepath.Join(c.DataDir, c.ID)
}

// KernelArgsWithNetwork returns kernel args with network configuration appended.
func (c *VMConfig) KernelArgsWithNetwork() string {
	if c.GuestIP == "" {
		return c.KernelArgs
	}
	// ip=<client-ip>:<server-ip>:<gw-ip>:<netmask>:<hostname>:<device>:<autoconf>
	netArgs := fmt.Sprintf("ip=%s::%s:%s::eth0:off", c.GuestIP, c.GatewayIP, c.Netmask)
	return c.KernelArgs + " " + netArgs
}
