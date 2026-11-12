package service

import (
	"context"
	"errors"
	"time"

	"fmt"
	"go-wind-admin/app/admin/service/internal/data"
)

// Reuses the existing worker lifecycle; persisted close commands are delivered
// before new creates. Failure to read the close queue also postpones creation.
func (w *ModelDevDispatchWorker) deliverNextClose(ctx context.Context) (worked, queueAvailable bool) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	claim, err := w.repository.ClaimCloseDelivery(call, w.workerID, 15*time.Second)
	cancel()
	if ctx.Err() != nil {
		return false, false
	}
	if err != nil {
		dependencyFailed(ctx, w.log.Logger, "postgresql", "modeldev.stop", []any{"worker", "modeldev_close", "step", "claim"}, err)
		return false, false
	}
	if claim == nil {
		return false, true
	}
	if claim.Intent == nil {
		stepFailed(ctx, w.log.Logger, "claim", "MODELDEV_CLOSE_ORIGINAL_MISSING",
			[]any{"worker", "modeldev_close", "step", "claim"}, fmt.Errorf("modeldev close original unavailable"))
		return true, false
	}
	// Restore the close-side trace captured on first entry into CLOSING.
	identity := backgroundIdentity(claim.Intent.OperationID, "modeldev", claim.Intent.ExecutionID, "", "modeldev.stop")
	ctx, runSpan := restoreBackgroundTrace(ctx, decodeAcceptanceTelemetry(claim.Intent.TelemetryJSON), nil, identity, w.log.Logger, "modeldev.close")
	defer runSpan.End()
	call, cancel = context.WithTimeout(ctx, 3*time.Second)
	receipt, deliveryErr := w.client.ApplyCloseIntent(call, *claim.Intent)
	cancel()
	if ctx.Err() != nil {
		return true, false
	}
	write, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var written bool
	if deliveryErr == nil {
		written, err = w.repository.AckCloseDelivery(write, claim, receipt)
	} else {
		failure := data.ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"}
		var classified *data.ModelDevDeliveryFailure
		if errors.As(deliveryErr, &classified) && classified != nil {
			failure = *classified
		}
		// Record the real RPC/status cause before it becomes a delivery code.
		stepFailed(ctx, w.log.Logger, "dispatch", "MODELDEV_CLOSE_FAILED", identity, deliveryErr)
		written, err = w.repository.DeferCloseDelivery(write, claim, failure)
	}
	if ctx.Err() == nil {
		if err != nil {
			statePersistFailed(ctx, w.log.Logger, "modeldev.stop", "persist_ack", identity, err)
		} else if !written {
			if w.log.Logger != nil {
				w.log.Logger.Warn(ctx, "modeldev close writeback ignored after lease change", append(append([]any{"event", "operation.lease_lost", "reason", "LEASE_LOST"}, identity...))...)
			}
		}
	}
	return true, true
}
