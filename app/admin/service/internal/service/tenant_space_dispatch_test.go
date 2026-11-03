package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go-wind-admin/app/admin/service/internal/data"
)

type spaceDispatchTestRepo struct {
	claim               *data.TenantSpaceClaim
	completed, deferred int
	permit              bool
}

func (r *spaceDispatchTestRepo) ClaimTenantSpace(context.Context, string, time.Duration) (*data.TenantSpaceClaim, error) {
	return r.claim, nil
}
func (r *spaceDispatchTestRepo) DeferTenantSpace(context.Context, *data.TenantSpaceClaim, time.Duration) (bool, error) {
	r.deferred++
	return r.permit, nil
}
func (r *spaceDispatchTestRepo) CompleteTenantSpace(context.Context, *data.TenantSpaceClaim) (bool, error) {
	r.completed++
	return r.permit, nil
}

type spaceDispatchTestRemote struct {
	result tenantSpaceResult
	err    error
}

func (r spaceDispatchTestRemote) OpenTenantSpace(context.Context, string, string) (tenantSpaceResult, error) {
	return r.result, r.err
}

func TestTenantSpaceDispatchRequiresMatchingReady(t *testing.T) {
	claim := &data.TenantSpaceClaim{TenantID: 1, ResourceTenantID: "tenant", OperationID: "operation"}
	for _, tc := range []struct {
		name                string
		result              tenantSpaceResult
		callErr             error
		permit, wantErr     bool
		completed, deferred int
	}{
		{"ready", tenantSpaceResult{"tenant", "operation", "ready"}, nil, true, false, 1, 0},
		{"open", tenantSpaceResult{"tenant", "operation", "open"}, nil, true, false, 0, 1},
		{"wrong tenant", tenantSpaceResult{"foreign", "operation", "ready"}, nil, true, true, 0, 1},
		{"wrong operation", tenantSpaceResult{"tenant", "other", "ready"}, nil, true, true, 0, 1},
		{"closed", tenantSpaceResult{"tenant", "operation", "closed"}, nil, true, true, 0, 1},
		{"unavailable", tenantSpaceResult{}, errors.New("unavailable"), true, true, 0, 1},
		{"stale lease", tenantSpaceResult{"tenant", "operation", "ready"}, nil, false, true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &spaceDispatchTestRepo{claim: claim, permit: tc.permit}
			d := tenantSpaceDispatcher{repository: repo, remote: spaceDispatchTestRemote{tc.result, tc.callErr}, owner: "worker"}
			worked, err := d.step(context.Background())
			if !worked || (err != nil) != tc.wantErr || repo.completed != tc.completed || repo.deferred != tc.deferred {
				t.Fatalf("worked=%v err=%v completed=%d deferred=%d", worked, err, repo.completed, repo.deferred)
			}
		})
	}
}
