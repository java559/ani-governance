package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc/status"
)

// SandboxDispatchWorker retries frozen create commands after client or process
// failure. Owner idempotency permits independent workers to retry the same row.
// An error never clears the parent deletion barrier.
type SandboxDispatchWorker struct {
	ledger  *data.QuotaLedgerRepo
	sandbox *SandboxService
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewSandboxDispatchWorker(ledger *data.QuotaLedgerRepo, sandbox *SandboxService) *SandboxDispatchWorker {
	return &SandboxDispatchWorker{ledger: ledger, sandbox: sandbox}
}
func (w *SandboxDispatchWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return nil
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	go w.loop(ctx, w.done)
	return nil
}
func (w *SandboxDispatchWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (w *SandboxDispatchWorker) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// A delivery contains a read followed by a create; each Resource RPC
		// may take up to 30 seconds. Its DB lease lasts longer than this budget.
		callCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
		intent, err := w.ledger.ClaimSandboxCreate(callCtx)
		if err == nil && intent != nil {
			_, err = w.sandbox.deliverSandboxCreate(callCtx, intent, true)
			if err != nil {
				code := status.Code(err).String()
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if retryErr := w.ledger.RetrySandboxCreate(cleanupCtx, intent.TenantID, intent.SandboxID, intent.LeaseGeneration, code); retryErr != nil {
					slog.Warn("sandbox create retry scheduling failed", "sandbox_id", intent.SandboxID)
				}
				cleanupCancel()
				slog.Warn("sandbox create delivery not confirmed", "sandbox_id", intent.SandboxID, "code", code)
			}
		} else if err != nil && ctx.Err() == nil {
			slog.Warn("sandbox create claim failed")
		}
		cancel()
	}
}
