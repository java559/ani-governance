package service

import (
	"context"
	"fmt"
	"time"

	"go-wind-admin/app/admin/service/internal/data"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
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
	log        bLogger.Logger
}

func (d *tenantSpaceCloseDispatcher) step(ctx context.Context) (bool, error) {
	intent, err := d.repository.ClaimTenantSpaceClose(ctx, 30*time.Second)
	if err != nil {
		dependencyFailed(ctx, d.log, "postgresql", "tenant_space.close", []any{"worker", "tenant_space", "step", "claim"}, err)
		return false, err
	}
	if intent == nil {
		return false, nil
	}
	identity := backgroundIdentity(intent.OperationID, "tenant_space", intent.ResourceTenantID, "", "tenant_space.close")
	saved, telemetryErr := decodeClaimTelemetry(intent.TelemetryJSON)
	ctx, runSpan := restoreBackgroundTrace(ctx, saved, telemetryErr, identity, d.log, "tenant_space.close")
	defer runSpan.End()
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	result, err := d.remote.CloseTenantSpace(call, intent.ResourceTenantID, intent.OperationID)
	cancel()
	if err != nil {
		stepFailed(ctx, d.log, "dispatch", "TENANT_SPACE_CLOSE_FAILED", identity, err)
		return true, err
	}
	if result.TenantID != intent.ResourceTenantID || result.OperationID != intent.OperationID {
		return true, fmt.Errorf("tenant space close response identity differs")
	}
	switch result.Phase {
	case "closing":
		if d.log != nil {
			d.log.Info(ctx, "tenant space close waiting", append(append([]any{"event", "step.waiting"}, identity...), "step", "close", "reason", "CLOSING", "result", "waiting")...)
		}
		return true, nil
	case "closed":
		done, err := d.repository.CompleteTenantSpaceClose(ctx, intent)
		if err != nil {
			statePersistFailed(ctx, d.log, "tenant_space.close", "persist_tenant_space", identity, err)
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
