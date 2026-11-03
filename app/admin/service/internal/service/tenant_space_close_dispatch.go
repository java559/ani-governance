package service

import (
	"context"
	"fmt"
	"time"

	"go-wind-admin/app/admin/service/internal/data"
)

type tenantSpaceCloseRepository interface {
	ClaimTenantSpaceClose(context.Context, time.Duration) (*data.TenantSpaceCloseIntent, error)
	CompleteTenantSpaceClose(context.Context, *data.TenantSpaceCloseIntent) (bool, error)
}
type tenantSpaceCloseRemote interface {
	CloseTenantSpace(context.Context, string, string) (tenantSpaceResult, error)
}
type tenantSpaceCloseDispatcher struct {
	repository tenantSpaceCloseRepository
	remote     tenantSpaceCloseRemote
}

func (d *tenantSpaceCloseDispatcher) step(ctx context.Context) (bool, error) {
	intent, err := d.repository.ClaimTenantSpaceClose(ctx, 30*time.Second)
	if err != nil || intent == nil {
		return false, err
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	result, err := d.remote.CloseTenantSpace(call, intent.ResourceTenantID, intent.OperationID)
	cancel()
	if err != nil {
		return true, err
	}
	if result.TenantID != intent.ResourceTenantID || result.OperationID != intent.OperationID {
		return true, fmt.Errorf("tenant space close response identity differs")
	}
	switch result.Phase {
	case "closing":
		return true, nil
	case "closed":
		done, err := d.repository.CompleteTenantSpaceClose(ctx, intent)
		if err != nil {
			return true, err
		}
		if !done {
			return true, fmt.Errorf("tenant space close intent changed")
		}
		return true, nil
	default:
		return true, fmt.Errorf("tenant space close returned unexpected phase %q", result.Phase)
	}
}
