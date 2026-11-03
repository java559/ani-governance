package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc/status"
)

// TenantSpaceDispatchWorker runs inside the existing Gov lifecycle.
type TenantSpaceDispatchWorker struct {
	open   *tenantSpaceDispatcher
	close  *tenantSpaceCloseDispatcher
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewTenantSpaceDispatchWorker(repo *data.TenantRepo, client *data.InstanceClient) *TenantSpaceDispatchWorker {
	return &TenantSpaceDispatchWorker{
		open:  &tenantSpaceDispatcher{repository: repo, remote: client, owner: uuid.NewString()},
		close: &tenantSpaceCloseDispatcher{repository: repo, remote: client},
	}
}
func (w *TenantSpaceDispatchWorker) Start(parent context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.done = make(chan struct{})
	go w.loop(ctx, w.done)
	return nil
}
func (w *TenantSpaceDispatchWorker) Stop(ctx context.Context) error {
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
func (w *TenantSpaceDispatchWorker) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		worked := false
		for _, step := range []func(context.Context) (bool, error){w.close.step, w.open.step} {
			didWork, err := step(ctx)
			worked = worked || didWork
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				slog.Warn("tenant space delivery not confirmed", "code", status.Code(err).String())
			}
		}
		delay := 3 * time.Second
		if worked {
			delay = time.Second
		}
		timer.Reset(delay)
	}
}
