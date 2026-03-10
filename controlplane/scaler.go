package controlplane

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Scaler monitors capacity and auto-scales nodes up/down.
type Scaler struct {
	db        *DB
	providers *CloudProviderRegistry
	cpURL     string // control plane URL for node registration
	nodeKey   string // shared secret for node auth
	logger    *slog.Logger
}

func NewScaler(db *DB, providers *CloudProviderRegistry, cpURL, nodeKey string, logger *slog.Logger) *Scaler {
	return &Scaler{
		db:        db,
		providers: providers,
		cpURL:     cpURL,
		nodeKey:   nodeKey,
		logger:    logger,
	}
}

// Start runs the auto-scaler loop.
func (s *Scaler) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.evaluate(ctx)
			}
		}
	}()
}

func (s *Scaler) evaluate(ctx context.Context) {
	pools, err := s.db.ListCloudPools()
	if err != nil {
		s.logger.Error("scaler: list pools", "error", err)
		return
	}

	for _, pool := range pools {
		s.evaluatePool(ctx, pool)
	}

	// Also mark nodes as offline if they haven't heartbeated
	s.detectOfflineNodes()
}

func (s *Scaler) evaluatePool(ctx context.Context, pool CloudPool) {
	total, used, err := s.db.TotalCapacity()
	if err != nil {
		s.logger.Error("scaler: get capacity", "error", err)
		return
	}

	available := total - used
	utilization := float64(0)
	if total > 0 {
		utilization = float64(used) / float64(total)
	}

	s.logger.Info("scaler: capacity check",
		"pool", pool.Name,
		"total", total,
		"used", used,
		"available", available,
		"utilization", fmt.Sprintf("%.0f%%", utilization*100),
		"currentNodes", pool.CurrentNodes,
	)

	// Scale up: if utilization > 80% and below max nodes
	if utilization > 0.8 && pool.CurrentNodes < pool.MaxNodes {
		s.scaleUp(ctx, pool)
		return
	}

	// Scale down: if utilization < 30% and above min nodes
	if utilization < 0.3 && pool.CurrentNodes > pool.MinNodes {
		s.scaleDown(ctx, pool)
		return
	}
}

func (s *Scaler) scaleUp(ctx context.Context, pool CloudPool) {
	provider, err := s.providers.Get(pool.Provider)
	if err != nil {
		s.logger.Error("scaler: provider not found", "provider", pool.Provider, "error", err)
		return
	}

	nodeName := fmt.Sprintf("gitvm-node-%s", uuid.New().String()[:8])
	runtime := pool.Runtime
	if runtime == "" {
		runtime = "firecracker" // default
	}
	userData := NodeUserData(s.cpURL, s.nodeKey, nodeName, runtime)

	s.logger.Info("scaler: scaling up", "pool", pool.Name, "node", nodeName, "runtime", runtime)

	result, err := provider.ProvisionNode(ctx, ProvisionOpts{
		Name:         nodeName,
		Region:       pool.Region,
		InstanceType: pool.InstanceType,
		Runtime:      runtime,
		UserData:     userData,
		Tags:         map[string]string{"gitvm": "node", "pool": pool.ID, "runtime": runtime},
	})
	if err != nil {
		s.logger.Error("scaler: provision failed", "error", err)
		return
	}

	// Record the new node as provisioning
	now := time.Now().UTC()
	node := &Node{
		ID:           uuid.New().String(),
		Name:         nodeName,
		Address:      fmt.Sprintf("http://%s:9090", result.PublicIP),
		PublicIP:     result.PublicIP,
		Provider:     pool.Provider,
		ProviderID:   result.ProviderID,
		Region:       result.Region,
		InstanceType: result.InstanceType,
		Status:       "provisioning",
		MaxSandboxes: 50,
		LastSeen:     now,
		CreatedAt:    now,
	}
	if err := s.db.UpsertNode(node); err != nil {
		s.logger.Error("scaler: save node", "error", err)
		return
	}

	pool.CurrentNodes++
	s.db.UpsertCloudPool(&pool)

	s.logger.Info("scaler: node provisioned", "node", nodeName, "ip", result.PublicIP)
}

func (s *Scaler) scaleDown(ctx context.Context, pool CloudPool) {
	// Find a drainable node (least loaded, from this pool's provider)
	nodes, err := s.db.ListOnlineNodes()
	if err != nil || len(nodes) == 0 {
		return
	}

	// Pick the least loaded node that matches this provider
	var target *Node
	for i := range nodes {
		if nodes[i].Provider == pool.Provider && nodes[i].Running == 0 {
			target = &nodes[i]
			break
		}
	}
	if target == nil {
		// No empty nodes to scale down — drain the least loaded
		for i := range nodes {
			if nodes[i].Provider == pool.Provider {
				target = &nodes[i]
				break // already sorted by running ASC
			}
		}
	}
	if target == nil {
		return
	}

	s.logger.Info("scaler: scaling down", "pool", pool.Name, "node", target.Name)

	// Mark as draining first
	s.db.UpdateNodeStatus(target.ID, "draining")

	// If no running sandboxes, terminate immediately
	if target.Running == 0 {
		s.terminateNode(ctx, pool, target)
	}
	// Otherwise, the drain will be completed by the heartbeat/cleanup loop
}

func (s *Scaler) terminateNode(ctx context.Context, pool CloudPool, node *Node) {
	provider, err := s.providers.Get(node.Provider)
	if err != nil {
		return
	}

	s.db.UpdateNodeStatus(node.ID, "terminating")

	if err := provider.TerminateNode(ctx, node.ProviderID); err != nil {
		s.logger.Error("scaler: terminate failed", "node", node.Name, "error", err)
		return
	}

	s.db.DeleteNode(node.ID)

	pool.CurrentNodes--
	if pool.CurrentNodes < 0 {
		pool.CurrentNodes = 0
	}
	s.db.UpsertCloudPool(&pool)

	s.logger.Info("scaler: node terminated", "node", node.Name)
}

func (s *Scaler) detectOfflineNodes() {
	nodes, _ := s.db.ListNodes()
	threshold := time.Now().Add(-2 * time.Minute)
	for _, n := range nodes {
		if n.Status == "online" && n.LastSeen.Before(threshold) {
			s.logger.Warn("scaler: node went offline", "node", n.Name, "lastSeen", n.LastSeen)
			s.db.UpdateNodeStatus(n.ID, "offline")
		}
	}
}
