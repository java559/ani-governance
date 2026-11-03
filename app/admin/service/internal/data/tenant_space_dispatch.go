package data

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

// TenantSpaceClaim is a lease over the tenant's original provisioning intent.
// A lease is not evidence that Resource accepted or completed the operation.
type TenantSpaceClaim struct {
	TenantID                                  uint32
	ResourceTenantID, OperationID, LeaseOwner string
	LeaseGeneration                           int64
}

// ClaimTenantSpace locates a bounded set of due tenants, then locks and
// rechecks each row in the same transaction that records its new lease. It is
// not wired to a worker until Resource provides a durable, fenced operation.
func (r *TenantRepo) ClaimTenantSpace(ctx context.Context, owner string, lease time.Duration) (*TenantSpaceClaim, error) {
	if !validTenantSpaceOwner(owner) || lease < time.Microsecond || lease > 5*time.Minute {
		return nil, fmt.Errorf("invalid tenant space lease")
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	candidates, err := r.entClient.Client().Tenant.Query().Where(tenantSpaceDue).Order(ent.Asc(tenant.FieldID)).Limit(32).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		tx, err := r.entClient.Client().Tx(ctx)
		if err != nil {
			return nil, err
		}
		var claim *TenantSpaceClaim
		row, err := tx.Tenant.Query().Where(tenant.IDEQ(candidate.ID), tenantSpaceDue).ForUpdate().Only(ctx)
		if err == nil {
			if row.ID == 0 || row.TenantSpaceOperationID == nil || !tenantSpaceUUID(row.ResourceTenantID) || !tenantSpaceUUID(*row.TenantSpaceOperationID) || row.TenantSpaceLeaseGeneration == math.MaxInt64 {
				err = fmt.Errorf("invalid tenant space intent")
			} else {
				generation := row.TenantSpaceLeaseGeneration + 1
				_, err = tx.Tenant.UpdateOneID(row.ID).SetTenantSpaceState(tenant.TenantSpaceStateDISPATCHING).
					SetTenantSpaceLeaseGeneration(generation).SetTenantSpaceLeaseOwner(owner).ClearTenantSpaceNextAttemptAt().
					Modify(func(u *entsql.UpdateBuilder) { u.Set(tenant.FieldTenantSpaceLeaseUntil, tenantSpaceAfter(lease)) }).Save(ctx)
				if err == nil {
					claim = &TenantSpaceClaim{TenantID: row.ID, ResourceTenantID: row.ResourceTenantID, OperationID: *row.TenantSpaceOperationID, LeaseOwner: owner, LeaseGeneration: generation}
				}
			}
		}
		if ent.IsNotFound(err) {
			_ = tx.Rollback()
			continue
		}
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return claim, nil
	}
	return nil, nil
}

// DeferTenantSpace retains the original operation after an uncertain or
// transient result. A stale, expired or replaced claim cannot change it.
func (r *TenantRepo) DeferTenantSpace(ctx context.Context, claim *TenantSpaceClaim, delay time.Duration) (bool, error) {
	if claim == nil || claim.TenantID == 0 || !tenantSpaceUUID(claim.ResourceTenantID) || !tenantSpaceUUID(claim.OperationID) || !validTenantSpaceOwner(claim.LeaseOwner) || claim.LeaseGeneration <= 0 || delay < 0 || delay > 5*time.Minute {
		return false, fmt.Errorf("invalid tenant space claim")
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	n, err := r.entClient.Client().Tenant.Update().Where(
		tenant.IDEQ(claim.TenantID), tenant.ResourceTenantIDEQ(claim.ResourceTenantID), tenant.TenantSpaceOperationIDEQ(claim.OperationID),
		tenant.TenantSpaceStateEQ(tenant.TenantSpaceStateDISPATCHING), tenant.TenantSpaceLeaseOwnerEQ(claim.LeaseOwner),
		tenant.TenantSpaceLeaseGenerationEQ(claim.LeaseGeneration), tenantSpaceLeaseLive,
	).SetTenantSpaceState(tenant.TenantSpaceStateUNKNOWN).ClearTenantSpaceLeaseOwner().ClearTenantSpaceLeaseUntil().
		Modify(func(u *entsql.UpdateBuilder) {
			u.Set(tenant.FieldTenantSpaceNextAttemptAt, tenantSpaceAfter(delay))
		}).Save(ctx)
	return n == 1, err
}

// CompleteTenantSpace records only a verified Resource ready response for
// the currently held lease. Stale workers cannot mark a new claim complete.
func (r *TenantRepo) CompleteTenantSpace(ctx context.Context, claim *TenantSpaceClaim) (bool, error) {
	if claim == nil || claim.TenantID == 0 || !tenantSpaceUUID(claim.ResourceTenantID) || !tenantSpaceUUID(claim.OperationID) || !validTenantSpaceOwner(claim.LeaseOwner) || claim.LeaseGeneration <= 0 {
		return false, fmt.Errorf("invalid tenant space claim")
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	n, err := r.entClient.Client().Tenant.Update().Where(
		tenant.IDEQ(claim.TenantID), tenant.ResourceTenantIDEQ(claim.ResourceTenantID), tenant.TenantSpaceOperationIDEQ(claim.OperationID),
		tenant.StatusEQ(tenant.StatusOn), tenant.TenantSpaceStateEQ(tenant.TenantSpaceStateDISPATCHING), tenant.TenantSpaceLeaseOwnerEQ(claim.LeaseOwner),
		tenant.TenantSpaceLeaseGenerationEQ(claim.LeaseGeneration), tenantSpaceLeaseLive,
	).SetTenantSpaceState(tenant.TenantSpaceStateREADY).ClearTenantSpaceLeaseOwner().ClearTenantSpaceLeaseUntil().ClearTenantSpaceNextAttemptAt().Save(ctx)
	return n == 1, err
}

func tenantSpaceDue(s *entsql.Selector) {
	s.Where(entsql.And(entsql.EQ(s.C(tenant.FieldStatus), string(tenant.StatusOn)),
		entsql.Or(
			entsql.EQ(s.C(tenant.FieldTenantSpaceState), string(tenant.TenantSpaceStateQUEUED)),
			entsql.And(entsql.EQ(s.C(tenant.FieldTenantSpaceState), string(tenant.TenantSpaceStateUNKNOWN)), entsql.Or(entsql.IsNull(s.C(tenant.FieldTenantSpaceNextAttemptAt)), entsql.LTE(s.C(tenant.FieldTenantSpaceNextAttemptAt), entsql.Expr("statement_timestamp()")))),
			entsql.And(entsql.EQ(s.C(tenant.FieldTenantSpaceState), string(tenant.TenantSpaceStateDISPATCHING)), entsql.LTE(s.C(tenant.FieldTenantSpaceLeaseUntil), entsql.Expr("statement_timestamp()"))),
		)))
}

func tenantSpaceLeaseLive(s *entsql.Selector) {
	s.Where(entsql.GT(s.C(tenant.FieldTenantSpaceLeaseUntil), entsql.Expr("statement_timestamp()")))
}

func tenantSpaceAfter(delay time.Duration) entsql.Querier {
	return entsql.ExprFunc(func(b *entsql.Builder) {
		b.WriteString("statement_timestamp() + ").Arg(delay.Microseconds()).WriteString(" * INTERVAL '1 microsecond'")
	})
}

func tenantSpaceUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func validTenantSpaceOwner(owner string) bool {
	return owner != "" && len(owner) <= 128 && strings.TrimSpace(owner) == owner
}
