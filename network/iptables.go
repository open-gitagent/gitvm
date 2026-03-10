package network

import "fmt"

// SetupNAT configures iptables rules for a VM slot to access the internet.
func SetupNAT(slot Slot, hostInterface string) error {
	if hostInterface == "" {
		hostInterface = "eth0"
	}

	// Enable forwarding from TAP to host interface
	if err := run("iptables", "-A", "FORWARD",
		"-i", slot.TAPName, "-o", hostInterface, "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("forward out: %w", err)
	}

	// Enable forwarding for established connections back
	if err := run("iptables", "-A", "FORWARD",
		"-i", hostInterface, "-o", slot.TAPName,
		"-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("forward in: %w", err)
	}

	// NAT: masquerade outbound traffic from VM subnet
	if err := run("iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", slot.GuestIP+"/30", "-o", hostInterface, "-j", "MASQUERADE"); err != nil {
		return fmt.Errorf("masquerade: %w", err)
	}

	return nil
}

// TeardownNAT removes iptables rules for a VM slot.
func TeardownNAT(slot Slot, hostInterface string) error {
	if hostInterface == "" {
		hostInterface = "eth0"
	}

	// Remove rules (ignore errors — they may not exist)
	run("iptables", "-D", "FORWARD",
		"-i", slot.TAPName, "-o", hostInterface, "-j", "ACCEPT")

	run("iptables", "-D", "FORWARD",
		"-i", hostInterface, "-o", slot.TAPName,
		"-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")

	run("iptables", "-t", "nat", "-D", "POSTROUTING",
		"-s", slot.GuestIP+"/30", "-o", hostInterface, "-j", "MASQUERADE")

	return nil
}

// EnableIPForwarding enables IP forwarding on the host.
func EnableIPForwarding() error {
	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return fmt.Errorf("enable ip forwarding: %w", err)
	}
	return nil
}
