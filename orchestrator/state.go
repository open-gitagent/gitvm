package orchestrator

import (
	"sync"
	"time"
)

// VMInstance represents a running sandbox with all its metadata.
type VMInstance struct {
	ID        string            `json:"id"`
	State     string            `json:"state"`    // "creating", "running", "paused", "stopped"
	Runtime   string            `json:"runtime"`  // "docker" or "firecracker"
	Template  string            `json:"template"`
	VCPUs     int               `json:"vcpus"`
	MemoryMB  int               `json:"memoryMB"`
	CreatedAt time.Time         `json:"createdAt"`
	ExpiresAt time.Time         `json:"expiresAt"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	AgentURL  string            `json:"agentURL"`
	GuestIP   string            `json:"guestIP"`
}

// VMStore is an in-memory store of running sandbox instances.
type VMStore struct {
	mu  sync.RWMutex
	vms map[string]*VMInstance
}

func NewVMStore() *VMStore {
	return &VMStore{vms: make(map[string]*VMInstance)}
}

func (s *VMStore) Put(instance *VMInstance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vms[instance.ID] = instance
}

func (s *VMStore) Get(id string) *VMInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.vms[id]
}

func (s *VMStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vms, id)
}

func (s *VMStore) List() []*VMInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*VMInstance, 0, len(s.vms))
	for _, vm := range s.vms {
		list = append(list, vm)
	}
	return list
}

func (s *VMStore) Expired(now time.Time) []*VMInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var expired []*VMInstance
	for _, vm := range s.vms {
		if !vm.ExpiresAt.IsZero() && now.After(vm.ExpiresAt) {
			expired = append(expired, vm)
		}
	}
	return expired
}

func (s *VMStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.vms)
}
