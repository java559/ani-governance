package service

import (
	"context"
	"fmt"
	"time"

	"go-wind-admin/app/admin/service/internal/data"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

type tenantSpaceDispatchRepository interface {
	ClaimTenantSpace(context.Context, string, time.Duration) (*data.TenantSpaceClaim, error)
	DeferTenantSpace(context.Context, *data.TenantSpaceClaim, time.Duration) (bool, error)
	CompleteTenantSpace(context.Context, *data.TenantSpaceClaim) (bool, error)
}

type tenantSpaceResult = data.TenantSpaceResult
type tenantSpaceRemote interface {
	OpenTenantSpace(context.Context, string, string) (tenantSpaceResult, error)
}

// tenantSpaceDispatcher only records readiness after an exact Resource result.
// Its lease schedules retries; it does not establish a provider-write fence.
type tenantSpaceDispatcher struct {
	repository tenantSpaceDispatchRepository
	remote     tenantSpaceRemote
	owner      string
	log        bLogger.Logger
}

func (d *tenantSpaceDispatcher) step(ctx context.Context) (bool, error) {
	claim, err := d.repository.ClaimTenantSpace(ctx, d.owner, 90*time.Second)
	if err != nil {
		dependencyFailed(ctx, d.log, "postgresql", "tenant_space.open", []any{"worker", "tenant_space", "step", "claim"}, err)
		return false, err
	}
	if claim == nil {
		return false, nil
	}
	identity := backgroundIdentity(claim.OperationID, "tenant_space", claim.ResourceTenantID, "", "tenant_space.open")
	saved, telemetryErr := decodeClaimTelemetry(claim.TelemetryJSON)
	ctx, runSpan := restoreBackgroundTrace(ctx, saved, telemetryErr, identity, d.log, "tenant_space.open")
	defer runSpan.End()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	if d.log != nil {
		d.log.Info(ctx, "tenant space open dispatch", append(append([]any{"event", "operation.dispatch_started"}, identity...), "peer.service", "ani-resource-service")...)
	}
	result, callErr := d.remote.OpenTenantSpace(call, claim.ResourceTenantID, claim.OperationID)
	cancel()
	if callErr == nil && result.TenantID == claim.ResourceTenantID && result.OperationID == claim.OperationID && result.Phase == "ready" {
		done, err := d.repository.CompleteTenantSpace(ctx, claim)
		if err != nil {
			statePersistFailed(ctx, d.log, "tenant_space.open", "persist_tenant_space", identity, err)
			return true, err
		}
		if !done {
			return true, fmt.Errorf("tenant space lease lost before readiness")
		}
		return true, nil
	}
	// Persist the retry even when the caller has canceled, but never extend it
	// indefinitely. The repository still rejects a stale or expired lease.
	cleanup, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	deferred, deferErr := d.repository.DeferTenantSpace(cleanup, claim, 5*time.Second)
	cleanupCancel()
	if deferErr != nil {
		statePersistFailed(ctx, d.log, "tenant_space.open", "defer_tenant_space", identity, deferErr)
		return true, deferErr
	}
	if !deferred {
		return true, fmt.Errorf("tenant space lease lost before retry")
	}
	if callErr != nil {
		stepFailed(ctx, d.log, "dispatch", "TENANT_SPACE_DISPATCH_FAILED", identity, callErr)
		return true, callErr
	}
	if result.TenantID != claim.ResourceTenantID || result.OperationID != claim.OperationID {
		return true, fmt.Errorf("tenant space response identity differs")
	}
	if result.Phase == "open" {
		// The owner accepted the request and is still converging. This is a known
		// wait, not a failure: record it as step.waiting so an operator can see the
		// lane is alive, and let the existing retry schedule continue untouched.
		if d.log != nil {
			d.log.Info(ctx, "tenant space open still converging",
				append(append([]any{"event", "step.waiting", "step", "open", "reason", "TENANT_SPACE_OPEN", "result", "waiting"}, identity...))...)
		}
		return true, nil
	}
	return true, fmt.Errorf("tenant space unexpected phase %q", result.Phase)
}
