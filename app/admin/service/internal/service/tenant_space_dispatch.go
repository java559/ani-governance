package service

import (
	"context"
	"fmt"
	"time"

	"go-wind-admin/app/admin/service/internal/data"
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
}

func (d *tenantSpaceDispatcher) step(ctx context.Context) (bool, error) {
	claim, err := d.repository.ClaimTenantSpace(ctx, d.owner, 90*time.Second)
	if err != nil || claim == nil {
		return false, err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	result, callErr := d.remote.OpenTenantSpace(call, claim.ResourceTenantID, claim.OperationID)
	cancel()
	if callErr == nil && result.TenantID == claim.ResourceTenantID && result.OperationID == claim.OperationID && result.Phase == "ready" {
		done, err := d.repository.CompleteTenantSpace(ctx, claim)
		if err != nil {
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
		return true, deferErr
	}
	if !deferred {
		return true, fmt.Errorf("tenant space lease lost before retry")
	}
	if callErr != nil {
		return true, callErr
	}
	if result.TenantID != claim.ResourceTenantID || result.OperationID != claim.OperationID {
		return true, fmt.Errorf("tenant space response identity differs")
	}
	if result.Phase != "open" {
		return true, fmt.Errorf("tenant space unexpected phase %q", result.Phase)
	}
	return true, nil
}
