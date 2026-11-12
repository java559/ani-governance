package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go-wind-admin/app/admin/service/internal/data"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

// ModelDevDispatchWorker delivers already committed CPU admissions. It owns
// neither current user authorization nor Release/default resolution.
type ModelDevDispatchWorker struct {
	createMaintenance *ModelDevCreateMaintenance
	repository        *data.ModelDevAcceptanceRepo
	client            *data.ModelDevClient
	log               *bLogger.Helper
	workerID          string
	mu                sync.Mutex
	cancel            context.CancelFunc
	done              chan struct{}
}

// Construction performs no database or network work and starts no goroutine.
func NewModelDevDispatchWorker(ctx *bootstrap.Context, repository *data.ModelDevAcceptanceRepo, client *data.ModelDevClient) *ModelDevDispatchWorker {
	w := &ModelDevDispatchWorker{repository: repository, client: client, done: make(chan struct{}), createMaintenance: NewModelDevCreateMaintenance(false)}
	if ctx != nil {
		w.log = ctx.NewLoggerHelper("modeldev-dispatch/worker/admin-service")
	}
	return w
}

// Start implements the existing transport lifecycle. A worker instance may
// start once; a new process reconstructs the queue from persisted originals.
func (w *ModelDevDispatchWorker) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w == nil || w.repository == nil || w.client == nil {
		return errors.New("modeldev dispatch worker unavailable")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return errors.New("modeldev dispatch worker already started")
	}
	identity, err := uuid.NewRandom()
	if err != nil {
		return errors.New("modeldev dispatch worker identity unavailable")
	}
	w.workerID = "modeldev-" + identity.String()
	// This is a managed cross-tenant queue, not an incoming user request.
	// The SystemViewer retains the lifecycle cancellation and does not grant
	// permission to alter the immutable actor, scope or command in a claim.
	// Shutdown can cancel the Start parent before invoking Stop.
	// Retain values but let Stop own cancellation, so it can first drain the
	// existing bounded Create calls and their leases while close work continues.
	ctx, w.cancel = context.WithCancel(context.WithoutCancel(appViewer.NewSystemViewerContext(ctx)))
	go w.loop(ctx)
	return nil
}

// Stop first blocks new Create admission/claims and drains their current work.
// Close delivery continues until the final cancellation. An incomplete drain
// is reported and any canceled in-flight lease still requires reconciliation.
func (w *ModelDevDispatchWorker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.cancel == nil {
		w.mu.Unlock()
		return nil
	}
	cancelWorker, done := w.cancel, w.done
	w.mu.Unlock()
	drainContext, cancelDrain := context.WithTimeout(ctx, 20*time.Second)
	drainErr := w.createMaintenance.pauseAndDrain(drainContext)
	if drainErr == nil && w.repository != nil && w.workerID != "" {
		drainErr = w.waitCreateLeaseExit(drainContext)
	}
	cancelDrain()
	cancelWorker()
	select {
	case <-done:
		return drainErr
	default:
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		return drainErr
	case <-ctx.Done():
		return errors.Join(drainErr, ctx.Err())
	case <-timer.C:
		return errors.Join(drainErr, errors.New("modeldev dispatch worker stop timeout"))
	}
}

func (w *ModelDevDispatchWorker) loop(ctx context.Context) {
	defer close(w.done)
	for ctx.Err() == nil {
		closed, queueAvailable := w.deliverNextClose(ctx)
		if closed || (queueAvailable && w.deliverNext(ctx)) {
			continue
		}
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (w *ModelDevDispatchWorker) deliverNext(ctx context.Context) bool {
	finish, allowed := w.createMaintenance.begin()
	if !allowed {
		return false
	}
	defer finish()
	claimContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	claim, err := w.repository.ClaimDelivery(claimContext, w.workerID, 15*time.Second)
	cancel()
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		dependencyFailed(ctx, w.log.Logger, "postgresql", "modeldev.create", []any{"worker", "modeldev_create", "step", "claim"}, err)
		return false
	}
	if claim == nil {
		return false
	}
	if claim.Acceptance == nil {
		stepFailed(ctx, w.log.Logger, "claim", "MODELDEV_ORIGINAL_MISSING",
			[]any{"worker", "modeldev_create", "step", "claim"}, fmt.Errorf("modeldev delivery original unavailable"))
		return false
	}
	// Restore the accepted request's trace for this delivery pass.
	identity := backgroundIdentity(claim.Acceptance.OperationID, "modeldev", claim.Acceptance.ExecutionID, "", "modeldev.create")
	ctx, runSpan := restoreBackgroundTrace(ctx, decodeAcceptanceTelemetry(claim.Acceptance.TelemetryJSON), nil, identity, w.log.Logger, "modeldev.create")
	defer runSpan.End()
	if w.log.Logger != nil {
		w.log.Logger.Info(ctx, "modeldev delivery dispatch", append(append([]any{"event", "operation.dispatch_started"}, identity...), "peer.service", "ani-modeldev-service")...)
	}
	// Claim committed before the RPC begins. The typed sender validates and
	// delivers this exact original; it never resolves defaults or renews time.
	deliveryContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	receipt, deliveryErr := w.client.AcceptExecution(deliveryContext, claim.Acceptance.Envelope())
	cancel()
	if ctx.Err() != nil {
		return true
	}
	writeContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if deliveryErr == nil {
		written, err := w.repository.AckDelivery(writeContext, claim, receipt)
		if ctx.Err() != nil {
			return true
		}
		if err != nil {
			// A failed or lost writeback leaves the lease recoverable. Do not
			// turn an uncertain commit into a different command or local ACK.
			statePersistFailed(ctx, w.log.Logger, "modeldev.create", "persist_ack", identity, err)
		} else if !written {
			if w.log.Logger != nil {
				w.log.Logger.Warn(ctx, "modeldev delivery ack ignored after lease change", append(append([]any{"event", "operation.lease_lost", "reason", "LEASE_LOST"}, identity...))...)
			}
		} else if w.log.Logger != nil {
			w.log.Logger.Info(ctx, "modeldev delivery acknowledged", append(append([]any{"event", "operation.dispatched"}, identity...))...)
		}
		return true
	}
	// Record the real RPC/status cause before it becomes a fixed delivery code.
	stepFailed(ctx, w.log.Logger, "dispatch", "MODELDEV_DELIVERY_FAILED", identity, deliveryErr)
	failure := data.ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"}
	var classified *data.ModelDevDeliveryFailure
	if errors.As(deliveryErr, &classified) && classified != nil {
		failure = *classified
	}
	written, err := w.repository.DeferDelivery(writeContext, claim, failure)
	if ctx.Err() != nil {
		return true
	}
	if err != nil {
		statePersistFailed(ctx, w.log.Logger, "modeldev.create", "schedule_retry", identity, err)
	} else if !written {
		if w.log.Logger != nil {
			w.log.Logger.Warn(ctx, "modeldev delivery retry ignored after lease change", append(append([]any{"event", "operation.lease_lost", "reason", "LEASE_LOST"}, identity...))...)
		}
	} else if failure.Permanent {
		if w.log.Logger != nil {
			w.log.Logger.Warn(ctx, "modeldev delivery retry blocked by contract", append(append([]any{"event", "operation.retry_scheduled", "reason", failure.Code}, identity...))...)
		}
	}
	return true
}

// Only local finite messages reach logs; persisted command material, remote
// status text, database errors and credentials are never interpolated here.
func (w *ModelDevDispatchWorker) warn(message string) {
	if w.log != nil {
		w.log.Warnf(context.Background(), "%s", message)
	}
}

func (w *ModelDevDispatchWorker) waitCreateLeaseExit(ctx context.Context) error {
	for {
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		active, err := w.repository.HasDeliveryLease(call, w.workerID)
		cancel()
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
