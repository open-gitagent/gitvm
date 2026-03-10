package network

import (
	"fmt"
	"os/exec"
)

// CreateTAP creates a TAP device and assigns the host IP.
func CreateTAP(slot Slot) error {
	// Create TAP device
	if err := run("ip", "tuntap", "add", "dev", slot.TAPName, "mode", "tap"); err != nil {
		return fmt.Errorf("create TAP %s: %w", slot.TAPName, err)
	}

	// Assign host IP with /30 prefix
	if err := run("ip", "addr", "add", slot.HostIP+"/30", "dev", slot.TAPName); err != nil {
		DestroyTAP(slot)
		return fmt.Errorf("assign IP to %s: %w", slot.TAPName, err)
	}

	// Bring up the interface
	if err := run("ip", "link", "set", slot.TAPName, "up"); err != nil {
		DestroyTAP(slot)
		return fmt.Errorf("bring up %s: %w", slot.TAPName, err)
	}

	return nil
}

// DestroyTAP removes a TAP device.
func DestroyTAP(slot Slot) error {
	return run("ip", "link", "del", slot.TAPName)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %s: %w", name, args, string(output), err)
	}
	return nil
}
