package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go-wind-admin/app/admin/service/internal/data"
)

type spaceCloseTestRepo struct {
	intent    *data.TenantSpaceCloseIntent
	completed int
	permit    bool
}

func (r *spaceCloseTestRepo) ClaimTenantSpaceClose(context.Context, time.Duration) (*data.TenantSpaceCloseIntent, error) {
	return r.intent, nil
}
func (r *spaceCloseTestRepo) CompleteTenantSpaceClose(context.Context, *data.TenantSpaceCloseIntent) (bool, error) {
	r.completed++
	return r.permit, nil
}

type spaceCloseTestRemote struct {
	result tenantSpaceResult
	err    error
}

func (r spaceCloseTestRemote) CloseTenantSpace(context.Context, string, string) (tenantSpaceResult, error) {
	return r.result, r.err
}

func TestTenantSpaceCloseDispatchRequiresMatchingClosed(t *testing.T) {
	intent := &data.TenantSpaceCloseIntent{TenantID: 1, ResourceTenantID: "tenant", OperationID: "operation"}
	for _, tc := range []struct {
		name            string
		result          tenantSpaceResult
		callErr         error
		permit, wantErr bool
		completed       int
	}{
		{"closed", tenantSpaceResult{"tenant", "operation", "closed"}, nil, true, false, 1},
		{"closing", tenantSpaceResult{"tenant", "operation", "closing"}, nil, true, false, 0},
		{"wrong tenant", tenantSpaceResult{"foreign", "operation", "closed"}, nil, true, true, 0},
		{"wrong operation", tenantSpaceResult{"tenant", "other", "closed"}, nil, true, true, 0},
		{"ready", tenantSpaceResult{"tenant", "operation", "ready"}, nil, true, true, 0},
		{"unavailable", tenantSpaceResult{}, errors.New("unavailable"), true, true, 0},
		{"stale intent", tenantSpaceResult{"tenant", "operation", "closed"}, nil, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &spaceCloseTestRepo{intent: intent, permit: tc.permit}
			d := tenantSpaceCloseDispatcher{repository: repo, remote: spaceCloseTestRemote{tc.result, tc.callErr}}
			worked, err := d.step(context.Background())
			if !worked || (err != nil) != tc.wantErr || repo.completed != tc.completed {
				t.Fatalf("worked=%v err=%v completed=%d", worked, err, repo.completed)
			}
		})
	}
}
