package network

import "fmt"

// Slot represents a reserved network allocation for a single VM.
type Slot struct {
	// ID is the slot index used for IP calculation.
	ID int

	// TAPName is the TAP device name on the host.
	TAPName string

	// GuestIP is the IP address assigned to the VM.
	GuestIP string

	// HostIP is the gateway IP on the host side of the TAP.
	HostIP string

	// GuestMAC is the MAC address for the VM's network interface.
	GuestMAC string
}

// NewSlot creates a network slot from an ID.
// Each slot gets a /30 subnet: 4 IPs per slot.
// Slot 0: 10.0.0.0/30 → host=10.0.0.1, guest=10.0.0.2
// Slot 1: 10.0.0.4/30 → host=10.0.0.5, guest=10.0.0.6
// etc.
func NewSlot(id int) Slot {
	base := id * 4
	octet3 := (base >> 8) & 0xFF
	octet4 := base & 0xFF

	hostIP := fmt.Sprintf("10.0.%d.%d", octet3, octet4+1)
	guestIP := fmt.Sprintf("10.0.%d.%d", octet3, octet4+2)
	mac := fmt.Sprintf("AA:FC:00:00:%02X:%02X", (id>>8)&0xFF, id&0xFF)

	return Slot{
		ID:       id,
		TAPName:  fmt.Sprintf("gitvm%d", id),
		GuestIP:  guestIP,
		HostIP:   hostIP,
		GuestMAC: mac,
	}
}
