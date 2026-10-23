package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/pkg/quotaerrors"
	"sort"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	kratosErrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/gpudeleteacceptance"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planquota"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/app/admin/service/internal/data/ent/quotareleasereceipt"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

type QuotaLedgerRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *bLogger.Helper
}

func NewQuotaLedgerRepo(ctx *bootstrap.Context, c *entCrud.EntClient[*ent.Client]) *QuotaLedgerRepo {
	return &QuotaLedgerRepo{c, ctx.NewLoggerHelper("quota-ledger/repo/admin-service")}
}
func (r *QuotaLedgerRepo) DB() *sql.DB { return r.entClient.DB() }
func (r *QuotaLedgerRepo) transaction(ctx context.Context, fn func(*ent.Tx) error) error {
	return quotaTransaction(ctx, r.entClient.Client(), fn)
}
func generateUUID() string { return uuid.NewString() }
func ptr[T any](v T) *T    { return &v }

type QuotaOccupyItem struct {
	QuotaCode string `json:"quota_code"`
	Units     int64  `json:"units"`
}
type QuotaOccupyInput struct {
	TenantID                                                                                                              uint32
	ResourceTenantID, ResourceID, ActorType, ActorID, OwnerService, Action, IdempotencyKey, RequestHash, CanonicalRequest string
	// OperationID is optional for legacy callers; managed owner adapters use it
	// to bind the frozen command context before the durable ledger row exists.
	OperationID             string
	Items                   []QuotaOccupyItem
	InstanceContractVersion uint32
}
type QuotaOccupyResult struct {
	OperationID, ResourceID string
	ChargeIDs               []string
	Replayed                bool
}
type QuotaReleaseInput struct {
	OwnerService, ReleaseEventID, OperationID, Reason, PayloadHash, PayloadJSON string
	Items                                                                       []QuotaReleaseItemInput
	// Complete resource release derives every quantity from the original ledger.
	ResourceID string
	Complete   bool
}
type QuotaReleaseItemInput struct {
	ChargeID, QuotaCode string
	ReleasedTotal       int64
}
type QuotaReleaseResult struct {
	ChargeID                    string
	AppliedDelta, ReleasedTotal int64
}
type QuotaDeleteInput struct {
	TenantID                                                                                            uint32
	ActorType, ActorID, OwnerService, Action, IdempotencyKey, RequestHash, CanonicalRequest, ResourceID string
}
type QuotaDeleteResult struct {
	OperationID, CreateOperationID, ChargeID, ResourceID string
	ChargeIDs                                            []string
	Replayed, LocalCanceled                              bool
}
type QuotaChargeRef struct {
	ChargeID, QuotaCode string
	ChargedUnits        int64
}
type ClaimedOperation struct {
	ID, TenantID                                                                                                                          uint32
	OperationID, ResourceTenantID, ResourceID, CreateOperationID, ActorType, ActorID, OwnerService, Action, RequestHash, CanonicalRequest string
	LeaseGeneration                                                                                                                       int64
	AttemptCount                                                                                                                          int
	Charges                                                                                                                               []QuotaChargeRef
}
type InvariantRow struct {
	TenantID                       uint32
	QuotaCode                      string
	OccupiedUnits, ChargeRemainder int64
	Balanced                       bool
}

func chargeIDs(charges []*ent.QuotaCharge) []string {
	out := make([]string, len(charges))
	for i, c := range charges {
		out[i] = c.ChargeID
	}
	return out
}
func isGPUCode(code string) bool {
	return code == "gpu.physical.count" || code == "gpu.shared_memory_mib"
}

// validateFrozenCharges checks the entire immutable business vector for GPU
// operations; non-GPU operations retain their independent accounting semantics.
func validateFrozenCharges(canonical string, charges []*ent.QuotaCharge) error {
	gpu := false
	for _, c := range charges {
		gpu = gpu || isGPUCode(c.QuotaCode)
	}
	var frozen struct {
		SchemaVersion int               `json:"schema_version"`
		QuotaItems    []QuotaOccupyItem `json:"quota_items"`
	}
	if err := json.Unmarshal([]byte(canonical), &frozen); err != nil {
		return quotapb.ErrorInvalidQuotaRequest("%s", "invalid immutable GPU request")
	}
	for _, item := range frozen.QuotaItems {
		gpu = gpu || isGPUCode(item.QuotaCode)
	}
	if !gpu && frozen.SchemaVersion != 3 {
		return nil
	}
	if len(frozen.QuotaItems) != len(charges) || len(charges) == 0 {
		return quotapb.ErrorInvalidQuotaRequest("%s", "incomplete immutable charge vector")
	}
	expected := map[string]int64{}
	for _, it := range frozen.QuotaItems {
		if it.Units <= 0 || expected[it.QuotaCode] != 0 {
			return quotapb.ErrorInvalidQuotaRequest("%s", "invalid immutable charge vector")
		}
		expected[it.QuotaCode] = it.Units
	}
	for _, c := range charges {
		if expected[c.QuotaCode] != c.OriginalUnits {
			return quotapb.ErrorInvalidQuotaRequest("%s", "immutable charge vector mismatch")
		}
	}
	return nil
}

// Occupy retains the existing standalone transaction boundary for current
// callers. ModelDev composes the validated core into its acceptance transaction.
func (r *QuotaLedgerRepo) Occupy(ctx context.Context, in *QuotaOccupyInput) (out *QuotaOccupyResult, err error) {
	if _, validateErr := validateQuotaOccupyInput(in); validateErr != nil {
		return nil, validateErr
	}
	err = r.transaction(ctx, func(tx *ent.Tx) error { out, err = r.occupyInTx(ctx, tx, in); return err })
	if err != nil {
		return nil, r.storageError(ctx, err)
	}
	return out, nil
}

func validateQuotaOccupyInput(in *QuotaOccupyInput) ([]QuotaOccupyItem, error) {
	if in == nil || in.TenantID == 0 || in.IdempotencyKey == "" || in.RequestHash == "" || in.CanonicalRequest == "" || in.ResourceTenantID == "" || in.ResourceID == "" || in.OwnerService == "" || in.Action == "" || in.ActorType == "" || in.ActorID == "" {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "incomplete quota input")
	}
	if in.OwnerService == instanceCPUOwner && in.Action == instanceCPUCreateAction && in.InstanceContractVersion != CurrentInstanceContractVersion {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "unsupported instance contract")
	}
	if commandcontract.ManagedOwner(in.OwnerService) {
		refs := make([]commandcontract.ChargeRef, 0, len(in.Items))
		for _, q := range in.Items {
			refs = append(refs, commandcontract.ChargeRef{QuotaCode: q.QuotaCode, ChargedUnits: q.Units})
		}
		if _, e := commandcontract.ValidateStoredCommand(commandcontract.StoredCommand{Owner: in.OwnerService, Action: in.Action, TenantID: in.ResourceTenantID, ResourceID: in.ResourceID, OperationID: in.OperationID, ActorType: in.ActorType, ActorID: in.ActorID, InstanceContractVersion: in.InstanceContractVersion, Canonical: []byte(in.CanonicalRequest), Charges: refs}); e != nil {
			return nil, e
		}
	} else if len(in.Items) == 0 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "missing original charge vector")
	}
	items := append([]QuotaOccupyItem(nil), in.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].QuotaCode < items[j].QuotaCode })
	for i, it := range items {
		if it.Units <= 0 || it.QuotaCode == "" || (i > 0 && items[i-1].QuotaCode == it.QuotaCode) {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid or duplicate quota item")
		}
	}
	return items, nil
}

// occupyInTx uses the caller's original transaction. External authorization,
// selection and GPU resolution must finish before this point.
func (r *QuotaLedgerRepo) occupyInTx(ctx context.Context, tx *ent.Tx, in *QuotaOccupyInput) (out *QuotaOccupyResult, err error) {
	items, err := validateQuotaOccupyInput(in)
	if err != nil {
		return nil, err
	}
	err = func() error {
		databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		tenant, err := lockQuotaTenant(ctx, tx, in.TenantID)
		if ent.IsNotFound(err) {
			return quotapb.ErrorQuotaAdmissionDenied("%s", "tenant not found")
		}
		if err != nil {
			return err
		}
		// Replay precedes current subscription/capability checks and reuses the first snapshot.
		op, err := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(in.TenantID), quotaoperation.ActorTypeEQ(in.ActorType), quotaoperation.ActorIDEQ(in.ActorID), quotaoperation.ActionEQ(in.Action), quotaoperation.IdempotencyKeyEQ(in.IdempotencyKey)).Only(ctx)
		if err == nil {
			if op.RequestHash != in.RequestHash || op.OwnerService != in.OwnerService || op.ResourceTenantID != in.ResourceTenantID {
				return quotapb.ErrorIdempotencyConflict("%s", "idempotency payload mismatch")
			}
			charges, e := tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(in.TenantID), quotacharge.OperationIDEQ(op.OperationID)).Order(ent.Asc(quotacharge.FieldQuotaCode), ent.Asc(quotacharge.FieldChargeID)).All(ctx)
			if e != nil {
				return e
			}
			if e = validateOperationCharges(op, charges); e != nil {
				return e
			}
			out = &QuotaOccupyResult{op.OperationID, op.ResourceID, chargeIDs(charges), true}
			return nil
		}
		if !ent.IsNotFound(err) {
			return err
		}
		if tenant.ResourceTenantID != in.ResourceTenantID {
			return quotapb.ErrorQuotaAdmissionDenied("%s", "resource tenant mapping mismatch")
		}
		if tenant.Status == nil || *tenant.Status != "ON" || (tenant.ExpiredAt != nil && !tenant.ExpiredAt.After(tenant.DatabaseNow)) {
			return quotapb.ErrorQuotaAdmissionDenied("%s", "tenant is inactive or expired")
		}
		if tenant.PlanID == nil || *tenant.PlanID == 0 {
			return quotapb.ErrorQuotaNotConfigured("%s", "tenant has no plan")
		}
		if _, err = tx.Plan.Query().Where(plan.IDEQ(*tenant.PlanID)).ForShare().Only(ctx); err != nil {
			return err
		}
		policies, err := tx.PlanQuota.Query().Where(planquota.HasPlanWith(plan.IDEQ(*tenant.PlanID))).Order(ent.Asc(planquota.FieldQuotaCode)).All(ctx)
		if err != nil {
			return err
		}
		limits := map[string]int64{}
		for _, p := range policies {
			limits[p.QuotaCode] = int64(*p.QuotaValue)
		}
		for _, it := range items {
			limit, ok := limits[it.QuotaCode]
			if !ok {
				return quotapb.ErrorQuotaNotConfigured("%s", "quota not configured: "+it.QuotaCode)
			}
			if err = tx.QuotaAccount.Create().SetCreatedAt(databaseNow).SetUpdatedAt(databaseNow).SetTenantID(in.TenantID).SetQuotaCode(it.QuotaCode).OnConflictColumns(quotaaccount.FieldTenantID, quotaaccount.FieldQuotaCode).Ignore().Exec(ctx); err != nil {
				return err
			}
			acc, e := tx.QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(in.TenantID), quotaaccount.QuotaCodeEQ(it.QuotaCode)).ForUpdate().Only(ctx)
			if e != nil {
				return e
			}
			if it.Units > limit || acc.OccupiedUnits > limit-it.Units {
				return quotapb.ErrorQuotaExceeded("%s", "quota exceeded: "+it.QuotaCode)
			}
		}
		version := in.InstanceContractVersion
		// Other quota actions retain their independent storage default; Instance
		// CREATE already requires the explicit current contract above.
		if version == 0 {
			version = 1
		}
		if version != 1 && version != 2 {
			return quotapb.ErrorInvalidQuotaRequest("%s", "invalid instance contract")
		}
		op, err = tx.QuotaOperation.Create().SetInstanceContractVersion(version).SetCreatedAt(databaseNow).SetUpdatedAt(databaseNow).SetOperationID(func() string {
			if in.OperationID != "" {
				return in.OperationID
			}
			return generateUUID()
		}()).SetTenantID(in.TenantID).SetResourceTenantID(in.ResourceTenantID).SetResourceID(in.ResourceID).SetActorType(in.ActorType).SetActorID(in.ActorID).SetOwnerService(in.OwnerService).SetAction(in.Action).SetIdempotencyKey(in.IdempotencyKey).SetRequestHash(in.RequestHash).SetCanonicalRequest(in.CanonicalRequest).SetDispatchState(quotaoperation.DispatchState("QUEUED")).Save(ctx)
		if err != nil {
			return err
		}
		charges := make([]*ent.QuotaCharge, 0, len(items))
		byCode := map[string]string{}
		for _, it := range items {
			c, e := tx.QuotaCharge.Create().SetCreatedAt(databaseNow).SetUpdatedAt(databaseNow).SetChargeID(generateUUID()).SetTenantID(in.TenantID).SetOperationID(op.OperationID).SetQuotaCode(it.QuotaCode).SetOriginalUnits(it.Units).Save(ctx)
			if e != nil {
				return e
			}
			charges = append(charges, c)
			byCode[it.QuotaCode] = c.ChargeID
			if e = changeAccount(ctx, tx, in.TenantID, it.QuotaCode, it.Units); e != nil {
				return e
			}
		}
		if err = validateOperationCharges(op, charges); err != nil {
			return err
		}
		ids := make([]string, len(in.Items))
		for i, it := range in.Items {
			ids[i] = byCode[it.QuotaCode]
		}
		out = &QuotaOccupyResult{op.OperationID, op.ResourceID, ids, false}
		return nil
	}()
	return out, err
}

func changeAccount(ctx context.Context, tx *ent.Tx, tenant uint32, code string, delta int64) error {
	databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
	if clockErr != nil {
		return clockErr
	}
	n, e := tx.QuotaAccount.Update().SetUpdatedAt(databaseNow).Where(quotaaccount.TenantIDEQ(tenant), quotaaccount.QuotaCodeEQ(code), quotaAccountNonnegative(delta)).AddOccupiedUnits(delta).AddVersion(1).Save(ctx)
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("quota account invariant violation")
	}
	return nil
}
func (r *QuotaLedgerRepo) storageError(ctx context.Context, err error) error {
	// Preserve typed business errors; never leak connection errors or storage
	// diagnostics through the BFF (including a closed pool/unreachable DB).
	var business *kratosErrors.Error
	if errors.As(err, &business) {
		return err
	}
	r.log.Errorf(ctx, "quota transaction: %v", err)
	return quotapb.ErrorQuotaStorageUnavailable("%s", "quota transaction failed")
}

func (r *QuotaLedgerRepo) Release(ctx context.Context, in *QuotaReleaseInput) (out []QuotaReleaseResult, err error) {
	if in == nil || (!in.Complete && len(in.Items) == 0) || (in.Complete && (in.ResourceID == "" || len(in.Items) != 0)) || in.OwnerService == "" || in.OperationID == "" || in.ReleaseEventID == "" || in.PayloadHash == "" {
		return nil, quotaerrors.ReleaseInvalid("incomplete release")
	}
	for _, item := range in.Items {
		if isGPUCode(item.QuotaCode) {
			return nil, quotaerrors.ReleaseInvalid("GPU release requires a complete resource fact")
		}
	}
	copyInput := *in
	in = &copyInput
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		// Sole owner-scoped locator: certificate owner plus original CREATE, before
		// explicit tenant locking. Caller-provided tenant never authorizes a refund.
		tid, e := locateQuotaReleaseTenant(ctx, tx, in.OperationID, in.OwnerService)
		if ent.IsNotFound(e) {
			return quotaerrors.ReleasePermissionDenied("release owner or operation mismatch")
		}
		if e != nil {
			return e
		}
		if _, e = lockQuotaTenant(ctx, tx, tid); e != nil {
			return e
		}
		op, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(in.OperationID)).ForUpdate().Only(ctx)
		if e != nil {
			return e
		}
		charges, e := tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tid), quotacharge.OperationIDEQ(in.OperationID)).Order(ent.Asc(quotacharge.FieldQuotaCode), ent.Asc(quotacharge.FieldChargeID)).ForUpdate().All(ctx)
		if e != nil {
			return e
		}
		if e = validateOperationCharges(op, charges); e != nil {
			return quotaerrors.ReleaseConflict("invalid original charge vector")
		}
		if in.Complete {
			if len(charges) == 0 {
				return quotaerrors.ReleaseInvalid("resource has no refundable charges")
			}
			if op.ResourceID != in.ResourceID || op.OwnerService != in.OwnerService || (in.Reason != "RESOURCE_RELEASED" && in.Reason != "ABORTED_CLEANED") {
				return quotaerrors.ReleasePermissionDenied("resource release ownership mismatch")
			}
			in.Items = make([]QuotaReleaseItemInput, 0, len(charges))
			for _, c := range charges {
				in.Items = append(in.Items, QuotaReleaseItemInput{ChargeID: c.ChargeID, QuotaCode: c.QuotaCode, ReleasedTotal: c.OriginalUnits})
			}
		}
		byID := map[string]*ent.QuotaCharge{}
		for _, c := range charges {
			byID[c.ChargeID] = c
		}
		incoming := map[string]QuotaReleaseItemInput{}
		gpu := false
		for _, it := range in.Items {
			c, ok := byID[it.ChargeID]
			if !ok {
				return quotaerrors.ReleaseNotFound("unknown charge")
			}
			if _, duplicate := incoming[it.ChargeID]; duplicate {
				return quotaerrors.ReleaseInvalid("duplicate charge")
			}
			if c.QuotaCode != it.QuotaCode || it.ReleasedTotal < 0 || it.ReleasedTotal > c.OriginalUnits {
				return quotaerrors.ReleaseConflict("invalid charge total or code")
			}
			incoming[it.ChargeID] = it
			gpu = gpu || isGPUCode(it.QuotaCode)
		}
		if gpu {
			for _, c := range charges {
				if isGPUCode(c.QuotaCode) {
					it, ok := incoming[c.ChargeID]
					if !ok || it.ReleasedTotal != c.OriginalUnits {
						return quotaerrors.ReleaseConflict("GPU release requires complete original GPU vector and full cumulative totals")
					}
				}
			}
			// The authenticated owner declares release. Original charge ownership,
			// full GPU vector and cumulative totals remain the accounting authority.
			if in.Reason != "RESOURCE_RELEASED" && in.Reason != "ABORTED_CLEANED" {
				return quotaerrors.ReleaseConflict("invalid GPU release reason")
			}
		}
		receipt, e := tx.QuotaReleaseReceipt.Query().Where(quotareleasereceipt.TenantIDEQ(tid), quotareleasereceipt.OwnerServiceEQ(in.OwnerService), quotareleasereceipt.ReleaseEventIDEQ(in.ReleaseEventID)).Only(ctx)
		replayed := e == nil
		if replayed && receipt.PayloadHash != in.PayloadHash {
			return quotaerrors.ReleaseConflict("event payload mismatch")
		}
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		out = make([]QuotaReleaseResult, 0, len(in.Items))
		for _, c := range charges {
			it, ok := incoming[c.ChargeID]
			if !ok {
				continue
			}
			total := c.ReleasedUnits
			delta := int64(0)
			if !replayed && it.ReleasedTotal > total {
				delta = it.ReleasedTotal - total
				total = it.ReleasedTotal
				if _, e = tx.QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(tid), quotaaccount.QuotaCodeEQ(c.QuotaCode)).ForUpdate().Only(ctx); e != nil {
					return e
				}
				if e = changeAccount(ctx, tx, tid, c.QuotaCode, -delta); e != nil {
					return e
				}
				if _, e = tx.QuotaCharge.Update().SetUpdatedAt(databaseNow).Where(quotacharge.TenantIDEQ(tid), quotacharge.ChargeIDEQ(c.ChargeID), quotacharge.ReleasedUnitsLTE(total), quotacharge.OriginalUnitsGTE(total)).SetReleasedUnits(total).Save(ctx); e != nil {
					return e
				}
			}
			out = append(out, QuotaReleaseResult{c.ChargeID, delta, total})
		}
		if replayed {
			return nil
		}
		// The existing globally unique receipt_id also fences a complete event
		// across tenant ledgers. A reused event cannot refund another tenant or owner.
		receiptID := generateUUID()
		if in.Complete {
			receiptID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("resource-release-v1\n"+in.ReleaseEventID)).String()
		}
		e = tx.QuotaReleaseReceipt.Create().SetCreatedAt(databaseNow).SetReceiptID(receiptID).SetTenantID(tid).SetOwnerService(in.OwnerService).SetReleaseEventID(in.ReleaseEventID).SetPayloadHash(in.PayloadHash).SetPayloadJSON(in.PayloadJSON).Exec(ctx)
		if ent.IsConstraintError(e) {
			return quotaerrors.ReleaseConflict("release event is already associated with another resource")
		}
		return e
	})
	return out, err
}

func cancelLocked(ctx context.Context, tx *ent.Tx, op *ent.QuotaOperation, charges []*ent.QuotaCharge) error {
	databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
	if clockErr != nil {
		return clockErr
	}
	if op.DispatchState == "CANCELED_UNSENT" {
		if op.AttemptCount != 0 {
			return quotapb.ErrorInvalidQuotaRequest("%s", "inconsistent local cancellation")
		}
		if e := validateOperationCharges(op, charges); e != nil {
			return e
		}
		for _, charge := range charges {
			if charge.ReleasedUnits != charge.OriginalUnits {
				return quotapb.ErrorInvalidQuotaRequest("%s", "local cancellation has incomplete refunds")
			}
		}
		return nil
	}
	if op.DispatchState != "QUEUED" || op.AttemptCount != 0 {
		return quotapb.ErrorInvalidQuotaRequest("%s", "owner closure required after a send attempt")
	}
	if e := validateOperationCharges(op, charges); e != nil {
		return e
	}
	for _, c := range charges {
		delta := c.OriginalUnits - c.ReleasedUnits
		if delta == 0 {
			continue
		}
		if _, e := tx.QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(*op.TenantID), quotaaccount.QuotaCodeEQ(c.QuotaCode)).ForUpdate().Only(ctx); e != nil {
			return e
		}
		if e := changeAccount(ctx, tx, *op.TenantID, c.QuotaCode, -delta); e != nil {
			return e
		}
		if _, e := tx.QuotaCharge.Update().SetUpdatedAt(databaseNow).Where(quotacharge.TenantIDEQ(*op.TenantID), quotacharge.ChargeIDEQ(c.ChargeID), quotacharge.ReleasedUnitsLTE(c.OriginalUnits), quotacharge.OriginalUnitsGTE(c.OriginalUnits)).SetReleasedUnits(c.OriginalUnits).Save(ctx); e != nil {
			return e
		}
	}
	n, e := tx.QuotaOperation.Update().SetUpdatedAt(databaseNow).Where(quotaoperation.TenantIDEQ(*op.TenantID), quotaoperation.OperationIDEQ(op.OperationID), quotaoperation.DispatchStateEQ(quotaoperation.DispatchStateQueued), quotaoperation.AttemptCountEQ(0)).SetDispatchState(quotaoperation.DispatchStateCanceledUnsent).SetLastErrorCode("LOCAL_CANCEL").Save(ctx)
	if e != nil {
		return e
	}
	if n != 1 {
		return quotapb.ErrorInvalidQuotaRequest("%s", "operation concurrently claimed")
	}
	return nil
}
func (r *QuotaLedgerRepo) CancelUnsent(ctx context.Context, tid uint32, id string) error {
	return r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		if _, e := lockQuotaTenant(ctx, tx, tid); e != nil {
			return e
		}
		op, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id)).ForUpdate().Only(ctx)
		if e != nil {
			return e
		}
		charges, e := tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tid), quotacharge.OperationIDEQ(id)).Order(ent.Asc(quotacharge.FieldQuotaCode), ent.Asc(quotacharge.FieldChargeID)).ForUpdate().All(ctx)
		if e != nil {
			return e
		}
		return cancelLocked(ctx, tx, op, charges)
	})
}
func (r *QuotaLedgerRepo) CreateDeleteOperation(ctx context.Context, in *QuotaDeleteInput) (out *QuotaDeleteResult, err error) {
	if in == nil || in.TenantID == 0 || in.IdempotencyKey == "" || in.RequestHash == "" || in.ResourceID == "" || in.OwnerService == "" || in.ActorType == "" || in.ActorID == "" || in.Action == "" || in.CanonicalRequest == "" {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "incomplete delete input")
	}
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		tid := in.TenantID
		if _, e := lockQuotaTenant(ctx, tx, tid); e != nil {
			return e
		}
		existing, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.ActorTypeEQ(in.ActorType), quotaoperation.ActorIDEQ(in.ActorID), quotaoperation.ActionEQ(in.Action), quotaoperation.IdempotencyKeyEQ(in.IdempotencyKey)).Only(ctx)
		replay := e == nil
		if replay && (existing.RequestHash != in.RequestHash || existing.OwnerService != in.OwnerService || existing.ResourceID != in.ResourceID) {
			return quotapb.ErrorIdempotencyConflict("%s", "delete idempotency mismatch")
		}
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		original, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OwnerServiceEQ(in.OwnerService), quotaoperation.ResourceIDEQ(in.ResourceID), quotaoperation.CreateOperationIDIsNil()).Only(ctx)
		if ent.IsNotFound(e) {
			return quotapb.ErrorQuotaNotFound("%s", "resource not found")
		}
		if e != nil {
			return e
		}
		original, e = tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(original.OperationID)).ForUpdate().Only(ctx)
		if e != nil {
			return e
		}
		charges, e := tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tid), quotacharge.OperationIDEQ(original.OperationID)).Order(ent.Asc(quotacharge.FieldQuotaCode), ent.Asc(quotacharge.FieldChargeID)).ForUpdate().All(ctx)
		if e != nil {
			return e
		}
		if e = validateOperationCharges(original, charges); e != nil {
			return e
		}
		gpu := false
		for _, charge := range charges {
			gpu = gpu || isGPUCode(charge.QuotaCode)
		}
		if replay {
			if e = validateOperationCharges(existing, charges); e != nil {
				return e
			}
			if commandcontract.ManagedOwner(in.OwnerService) {
				if _, e = commandcontract.DeleteSnapshot(in.OwnerService, original.CanonicalRequest, in.CanonicalRequest); e != nil {
					return e
				}
			}
			if existing.CreateOperationID == nil || *existing.CreateOperationID != original.OperationID {
				return quotapb.ErrorIdempotencyConflict("%s", "delete origin mismatch")
			}
			if gpu {
				accepted, e := tx.GpuDeleteAcceptance.Query().Where(gpudeleteacceptance.TenantIDEQ(tid), gpudeleteacceptance.DeleteOperationIDEQ(existing.OperationID)).Only(ctx)
				if e != nil {
					return e
				}
				if accepted.CreateOperationID != original.OperationID || accepted.RequestHash != in.RequestHash {
					return quotapb.ErrorIdempotencyConflict("%s", "GPU delete acceptance mismatch")
				}
			}
			out = deleteResult(existing, charges, true)
			return nil
		}
		local := original.DispatchState == "CANCELED_UNSENT" || (original.DispatchState == "QUEUED" && original.AttemptCount == 0)
		if local {
			if e = cancelLocked(ctx, tx, original, charges); e != nil {
				return e
			}
		}
		state := "QUEUED"
		if local {
			state = "CANCELED_UNSENT"
		}
		// Preserve the original request/plan/charges. The fixed instance owner
		// additionally freezes the validated deletion version for durable dispatch.
		canonical := original.CanonicalRequest
		if commandcontract.ManagedOwner(in.OwnerService) {
			canonical, e = commandcontract.DeleteSnapshot(in.OwnerService, original.CanonicalRequest, in.CanonicalRequest)
			if e != nil {
				return e
			}
		}
		op, e := tx.QuotaOperation.Create().SetInstanceContractVersion(original.InstanceContractVersion).SetCreatedAt(databaseNow).SetUpdatedAt(databaseNow).SetOperationID(generateUUID()).SetTenantID(tid).SetResourceTenantID(original.ResourceTenantID).SetResourceID(original.ResourceID).SetNillableCreateOperationID(&original.OperationID).SetActorType(in.ActorType).SetActorID(in.ActorID).SetOwnerService(in.OwnerService).SetAction(in.Action).SetIdempotencyKey(in.IdempotencyKey).SetRequestHash(in.RequestHash).SetCanonicalRequest(canonical).SetDispatchState(quotaoperation.DispatchState(state)).Save(ctx)
		if e != nil {
			return e
		}
		if e = validateOperationCharges(op, charges); e != nil {
			return e
		}
		if gpu {
			result := "OWNER_DELETE"
			if local {
				result = "LOCAL_CANCELED"
			}
			if e = tx.GpuDeleteAcceptance.Create().SetCreatedAt(databaseNow).SetTenantID(tid).SetActorType(in.ActorType).SetActorID(in.ActorID).SetAction(in.Action).SetIdempotencyKey(in.IdempotencyKey).SetRequestHash(in.RequestHash).SetCreateOperationID(original.OperationID).SetDeleteOperationID(op.OperationID).SetResult(result).Exec(ctx); e != nil {
				return e
			}
		}
		out = deleteResult(op, charges, false)
		return nil
	})
	return out, err
}

func instanceDeleteSnapshot(original, requested string) (string, error) {
	return commandcontract.DeleteSnapshot(commandcontract.InstanceOwner, original, requested)
}
func deleteResult(op *ent.QuotaOperation, charges []*ent.QuotaCharge, replay bool) *QuotaDeleteResult {
	result := &QuotaDeleteResult{OperationID: op.OperationID, ResourceID: op.ResourceID, ChargeIDs: chargeIDs(charges), Replayed: replay, LocalCanceled: op.DispatchState == "CANCELED_UNSENT"}
	if op.CreateOperationID != nil {
		result.CreateOperationID = *op.CreateOperationID
	}
	if len(charges) == 1 {
		result.ChargeID = charges[0].ChargeID
	}
	return result
}

func (r *QuotaLedgerRepo) FindIdempotentOperation(ctx context.Context, tid uint32, actorType, actorID, action, key string) (out *ent.QuotaOperation, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		v, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.ActorTypeEQ(actorType), quotaoperation.ActorIDEQ(actorID), quotaoperation.ActionEQ(action), quotaoperation.IdempotencyKeyEQ(key)).Only(ctx)
		if e == nil {
			out = v.Unwrap()
		}
		return e
	})
	return out, err
}
func (r *QuotaLedgerRepo) GetOperationForUser(ctx context.Context, tid uint32, id string) (out *ent.QuotaOperation, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		v, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id)).Only(ctx)
		if e == nil {
			out = v.Unwrap()
		}
		return e
	})
	return out, err
}
func (r *QuotaLedgerRepo) GetCreateOperationByResource(ctx context.Context, tid uint32, owner, resource string) (out *ent.QuotaOperation, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		v, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OwnerServiceEQ(owner), quotaoperation.ResourceIDEQ(resource), quotaoperation.CreateOperationIDIsNil()).Only(ctx)
		if e == nil {
			out = v.Unwrap()
		}
		return e
	})
	return out, err
}
func (r *QuotaLedgerRepo) GetChargesForOperation(ctx context.Context, tid uint32, id string) (out []*ent.QuotaCharge, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		v, e := tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tid), quotacharge.OperationIDEQ(id)).Order(ent.Asc(quotacharge.FieldQuotaCode), ent.Asc(quotacharge.FieldChargeID)).All(ctx)
		if e != nil {
			return e
		}
		if len(v) == 0 {
			original, err := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id)).Only(ctx)
			if err != nil {
				return err
			}
			// Only an existing, tenant-scoped CPU command may have no charges.
			// Missing/corrupt GPU vectors still fail instead of masquerading as CPU.
			if err = validateOperationCharges(original, nil); err != nil {
				return err
			}
			out = []*ent.QuotaCharge{}
			return nil
		}
		original, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id)).Only(ctx)
		if e != nil {
			return e
		}
		if e = validateOperationCharges(original, v); e != nil {
			return e
		}
		for _, c := range v {
			out = append(out, c.Unwrap())
		}
		return nil
	})
	return out, err
}
func (r *QuotaLedgerRepo) GetChargeForOperation(ctx context.Context, tid uint32, id string) (*ent.QuotaCharge, error) {
	cs, e := r.GetChargesForOperation(ctx, tid, id)
	if e != nil {
		return nil, e
	}
	if len(cs) != 1 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "single-charge interface cannot represent the complete operation")
	}
	return cs[0], nil
}
func (r *QuotaLedgerRepo) FindCreateOperationByResource(ctx context.Context, tid uint32, owner, resource string) (*ent.QuotaOperation, *ent.QuotaCharge, error) {
	op, e := r.GetCreateOperationByResource(ctx, tid, owner, resource)
	if e != nil {
		return nil, nil, e
	}
	c, e := r.GetChargeForOperation(ctx, tid, op.OperationID)
	return op, c, e
}
func (r *QuotaLedgerRepo) RecomputeInvariants(ctx context.Context, tid uint32) (out []InvariantRow, err error) {
	if tid == 0 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "tenant is required")
	}
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		// A single database snapshot computes the invariant without loading
		// every charge into Go or overflowing an application-side accumulator.
		var rows []struct {
			TenantID        uint32 `json:"tenant_id"`
			QuotaCode       string `json:"quota_code"`
			OccupiedUnits   int64  `json:"occupied_units"`
			ChargeRemainder int64  `json:"charge_remainder"`
		}
		e := tx.QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(tid)).Modify(func(s *entsql.Selector) {
			charges := entsql.Table(quotacharge.Table)
			s.LeftJoin(charges).
				On(s.C(quotaaccount.FieldTenantID), charges.C(quotacharge.FieldTenantID)).
				On(s.C(quotaaccount.FieldQuotaCode), charges.C(quotacharge.FieldQuotaCode))
			columns := []string{s.C(quotaaccount.FieldTenantID), s.C(quotaaccount.FieldQuotaCode), s.C(quotaaccount.FieldOccupiedUnits)}
			s.Select(columns...).AppendSelectExprAs(entsql.ExprFunc(func(b *entsql.Builder) {
				b.WriteString("COALESCE(SUM(").Ident(charges.C(quotacharge.FieldOriginalUnits)).WriteString(" - ").
					Ident(charges.C(quotacharge.FieldReleasedUnits)).WriteString("), 0)::bigint")
			}), "charge_remainder").GroupBy(columns...).OrderBy(s.C(quotaaccount.FieldQuotaCode))
		}).Scan(ctx, &rows)
		if e != nil {
			return e
		}
		for _, v := range rows {
			out = append(out, InvariantRow{v.TenantID, v.QuotaCode, v.OccupiedUnits, v.ChargeRemainder, v.OccupiedUnits == v.ChargeRemainder})
		}
		return nil
	})
	return
}

// Global scanning is bounded and returns the tenant. Every claim, read and CAS
// after that scan is explicitly tenant scoped.
func (r *QuotaLedgerRepo) ClaimDispatchable(ctx context.Context, worker string, lease time.Duration, limit int) (out []ClaimedOperation, err error) {
	if limit < 1 || limit > 100 || worker == "" || lease <= 0 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid worker claim")
	}
	var candidates []*ent.QuotaOperation
	if err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		var e error
		candidates, e = tx.QuotaOperation.Query().Where(quotaDispatchDue).Order(ent.Asc(quotaoperation.FieldCreatedAt), ent.Asc(quotaoperation.FieldID)).Limit(limit).All(ctx)
		return e
	}); err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		var claimed *ClaimedOperation
		e := r.transaction(ctx, func(tx *ent.Tx) error {
			databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
			if clockErr != nil {
				return clockErr
			}
			ctx := appViewer.NewSystemViewerContext(ctx)
			// Tenant-first lock order serializes cancellation, release and worker claim.
			if _, e := lockQuotaTenant(ctx, tx, *candidate.TenantID); e != nil {
				return e
			}
			op, e := claimQuotaOperation(ctx, tx, *candidate.TenantID, candidate.OperationID, worker, lease)
			if e != nil {
				return e
			}
			origin := op.OperationID
			if op.CreateOperationID != nil {
				origin = *op.CreateOperationID
			}
			charges, e := tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(*op.TenantID), quotacharge.OperationIDEQ(origin)).Order(ent.Asc(quotacharge.FieldQuotaCode), ent.Asc(quotacharge.FieldChargeID)).All(ctx)
			if e != nil {
				return e
			}
			if validateOperationCharges(op, charges) != nil {
				// Keep the failed claim and its diagnostic durable. Rolling this
				// transaction back would repeatedly select the same broken vector
				// without recording an attempt and could starve healthy operations.
				// A repaired original vector is retried without changing accounting.
				code := "ORIGINAL_CHARGES_INVALID"
				delay := min(30*time.Second, time.Second<<min(max(op.AttemptCount-1, 0), 5))
				next := time.Now().Add(delay)
				n, e := tx.QuotaOperation.Update().SetUpdatedAt(databaseNow).Where(quotaoperation.TenantIDEQ(*op.TenantID), quotaoperation.OperationIDEQ(op.OperationID), quotaoperation.LeaseGenerationEQ(op.LeaseGeneration), quotaoperation.DispatchStateEQ(quotaoperation.DispatchStateDispatching)).SetDispatchState(quotaoperation.DispatchStateUnknown).SetNillableNextAttemptAt(&next).SetNillableLastErrorCode(&code).SetRetryBlocked(false).Save(ctx)
				if e != nil {
					return e
				}
				if n != 1 {
					return quotapb.ErrorInvalidQuotaRequest("%s", "failed to retain invalid original charge claim")
				}
				return nil
			}
			v := ClaimedOperation{ID: op.ID, TenantID: *op.TenantID, OperationID: op.OperationID, ResourceTenantID: op.ResourceTenantID, ResourceID: op.ResourceID, ActorType: op.ActorType, ActorID: op.ActorID, OwnerService: op.OwnerService, Action: op.Action, RequestHash: op.RequestHash, CanonicalRequest: op.CanonicalRequest, LeaseGeneration: op.LeaseGeneration, AttemptCount: op.AttemptCount}
			if op.CreateOperationID != nil {
				v.CreateOperationID = *op.CreateOperationID
			}
			for _, c := range charges {
				v.Charges = append(v.Charges, QuotaChargeRef{c.ChargeID, c.QuotaCode, c.OriginalUnits})
			}
			claimed = &v
			return nil
		})
		if ent.IsNotFound(e) {
			continue
		}
		if e != nil {
			return out, e
		}
		if claimed != nil {
			out = append(out, *claimed)
		}
	}
	return out, nil
}
func (r *QuotaLedgerRepo) AckDispatched(ctx context.Context, tid uint32, id string, generation int64, ack string) (ok bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		n, e := tx.QuotaOperation.Update().SetUpdatedAt(databaseNow).Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id), quotaoperation.LeaseGenerationEQ(generation), quotaoperation.DispatchStateEQ(quotaoperation.DispatchStateDispatching)).SetDispatchState(quotaoperation.DispatchStateAcked).SetNillableAckJSON(&ack).Save(ctx)
		ok = n == 1
		return e
	})
	return
}
func (r *QuotaLedgerRepo) MarkUnknown(ctx context.Context, tid uint32, id string, generation int64, next time.Time, code string, blocked bool) (ok bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		n, e := tx.QuotaOperation.Update().SetUpdatedAt(databaseNow).Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id), quotaoperation.LeaseGenerationEQ(generation), quotaoperation.DispatchStateEQ(quotaoperation.DispatchStateDispatching)).SetDispatchState(quotaoperation.DispatchStateUnknown).SetNillableNextAttemptAt(&next).SetNillableLastErrorCode(&code).SetRetryBlocked(blocked).Save(ctx)
		ok = n == 1
		return e
	})
	return
}
func (r *QuotaLedgerRepo) ResumeDispatch(ctx context.Context, tid uint32, id string) error {
	return r.transaction(ctx, func(tx *ent.Tx) error {
		databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		n, e := tx.QuotaOperation.Update().SetUpdatedAt(databaseNow).Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id), quotaoperation.RetryBlockedEQ(true)).SetRetryBlocked(false).Modify(func(u *entsql.UpdateBuilder) {
			u.Set(quotaoperation.FieldNextAttemptAt, entsql.Expr("CURRENT_TIMESTAMP"))
		}).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return quotapb.ErrorQuotaNotFound("%s", "blocked operation not found")
		}
		return nil
	})
}
