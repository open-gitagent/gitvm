package orchestrator

import (
	"context"
	"time"
)

// StartCleanup runs a background goroutine that removes expired VMs.
func (o *Orchestrator) StartCleanup(ctx context.Context, interval time.Duration) {
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
				o.cleanupExpired(ctx)
			}
		}
	}()
}

func (o *Orchestrator) cleanupExpired(ctx context.Context) {
	expired := o.store.Expired(time.Now())
	for _, instance := range expired {
		o.logger.Info("cleaning up expired VM", "id", instance.ID)
		if err := o.Delete(ctx, instance.ID); err != nil {
			o.logger.Warn("failed to cleanup VM", "id", instance.ID, "error", err)
		}
	}
}

// Shutdown stops all running VMs gracefully.
func (o *Orchestrator) Shutdown(ctx context.Context) {
	o.logger.Info("shutting down orchestrator", "vms", o.store.Count())
	for _, instance := range o.store.List() {
		if err := o.Delete(ctx, instance.ID); err != nil {
			o.logger.Warn("failed to stop VM during shutdown", "id", instance.ID, "error", err)
		}
	}
}

