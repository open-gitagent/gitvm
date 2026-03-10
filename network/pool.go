package network

import (
	"fmt"
	"log/slog"
	"sync"
)

// Pool manages network slot allocation for VMs.
type Pool struct {
	mu            sync.Mutex
	maxSlots      int
	allocated     map[int]bool
	hostInterface string
	logger        *slog.Logger
}

// NewPool creates a network pool with the given capacity.
func NewPool(maxSlots int, hostInterface string, logger *slog.Logger) *Pool {
	if maxSlots <= 0 {
		maxSlots = 255 // ~255 VMs
	}
	if hostInterface == "" {
		hostInterface = "eth0"
	}
	return &Pool{
		maxSlots:      maxSlots,
		allocated:     make(map[int]bool),
		hostInterface: hostInterface,
		logger:        logger,
	}
}

// Allocate reserves a network slot, creates the TAP device, and sets up NAT.
func (p *Pool) Allocate() (*Slot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Find first free slot
	id := -1
	for i := 0; i < p.maxSlots; i++ {
		if !p.allocated[i] {
			id = i
			break
		}
	}
	if id < 0 {
		return nil, fmt.Errorf("no free network slots (max %d)", p.maxSlots)
	}

	slot := NewSlot(id)

	// Create TAP device
	if err := CreateTAP(slot); err != nil {
		return nil, fmt.Errorf("create TAP: %w", err)
	}

	// Setup NAT rules
	if err := SetupNAT(slot, p.hostInterface); err != nil {
		DestroyTAP(slot)
		return nil, fmt.Errorf("setup NAT: %w", err)
	}

	p.allocated[id] = true
	p.logger.Info("allocated network slot", "id", id, "tap", slot.TAPName, "guestIP", slot.GuestIP)
	return &slot, nil
}

// Release frees a network slot, removes the TAP device and NAT rules.
func (p *Pool) Release(slot *Slot) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	TeardownNAT(*slot, p.hostInterface)
	if err := DestroyTAP(*slot); err != nil {
		p.logger.Warn("failed to destroy TAP", "tap", slot.TAPName, "error", err)
	}

	delete(p.allocated, slot.ID)
	p.logger.Info("released network slot", "id", slot.ID, "tap", slot.TAPName)
	return nil
}

// Count returns the number of allocated slots.
func (p *Pool) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.allocated)
}
