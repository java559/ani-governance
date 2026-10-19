package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"sort"
	"strconv"

	"github.com/google/uuid"
	commandv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/command/v1"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/pkg/middleware/auth"
)

// GpuBusinessBinding is implemented only by a compiled business adapter.
// Authorization remains available when dispatch is temporarily unregistered;
// this permits authorized historical replay without reopening new admission.
type GpuBusinessBinding interface {
	QuotaDispatchAdapter
	CreateAction() string
	DeleteAction() string
	GpuQuotaCodes() []string
	AuthorizeGpu(context.Context, *auth.Principal, string, string) error
	ValidateGpuBusiness(context.Context, proto.Message) ([]data.QuotaOccupyItem, error)
}

type GpuPlanResolver interface {
	ResolveGpuRequest(context.Context, *acc.ResolveGpuRequestRequest, ...grpc.CallOption) (*acc.ResolvedGpuRequest, error)
}

// Instance presets/images are external dependencies. Their resolution occurs
// only after current authorization and the original public-request replay gate.
type GpuBusinessResolver interface {
	ResolveGpuBusiness(context.Context, *auth.Principal, string, proto.Message) (*acc.GpuRequest, proto.Message, error)
}

type GpuAcceptance struct {
	ledger   *data.QuotaLedgerRepo
	registry *QuotaAdapterRegistry
	resolver ResourceTenantResolver
	plans    GpuPlanResolver
	binding  GpuBusinessBinding
	worker   *QuotaDispatchWorker
}

func NewGpuAcceptance(ledger *data.QuotaLedgerRepo, registry *QuotaAdapterRegistry, resolver ResourceTenantResolver, plans GpuPlanResolver, binding GpuBusinessBinding, worker *QuotaDispatchWorker) (*GpuAcceptance, error) {
	if ledger == nil || registry == nil || resolver == nil || binding == nil || !supportedGpuOwner(binding.OwnerService()) || binding.CreateAction() == "" || binding.DeleteAction() == "" || binding.CreateAction() == binding.DeleteAction() {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid GPU business assembly")
	}
	return &GpuAcceptance{ledger: ledger, registry: registry, resolver: resolver, plans: plans, binding: binding, worker: worker}, nil
}

func (r *QuotaAdapterRegistry) GPUExecutionEnabled(owner string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.byOwner[owner].(GpuBusinessBinding)
	if !ok || !supportedGpuOwner(owner) || b.CreateAction() == "" || b.DeleteAction() == "" || b.CreateAction() == b.DeleteAction() {
		return false
	}
	return r.gpuActionsOwnedBy(b)
}

// Caller holds r.mu. Register permits only one binding per owner, so checking
// the registered action owner's identity proves both actions belong to b.
func (r *QuotaAdapterRegistry) gpuActionsOwnedBy(b GpuBusinessBinding) bool {
	for _, action := range []string{b.CreateAction(), b.DeleteAction()} {
		a := r.byAction[action]
		if a == nil || a.OwnerService() != b.OwnerService() {
			return false
		}
	}
	return true
}

// EnforcesQuota is the directory's read-only view of compiled owner/action
// capabilities. A row in a plan or catalog cannot activate this capability.
func (r *QuotaAdapterRegistry) EnforcesQuota(code string) bool {
	if code != GpuPhysicalQuotaCode && code != GpuSharedQuotaCode {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for owner, adapter := range r.byOwner {
		b, ok := adapter.(GpuBusinessBinding)
		if !ok || !supportedGpuOwner(owner) || b.CreateAction() == "" || b.DeleteAction() == "" || b.CreateAction() == b.DeleteAction() || !r.gpuActionsOwnedBy(b) {
			continue
		}
		for _, supported := range b.GpuQuotaCodes() {
			if supported == code {
				return true
			}
		}
	}
	return false
}

// Only the explicitly integrated instance owner can enable GPU business admission.
func supportedGpuOwner(owner string) bool {
	return owner == InstanceOwner
}

func gpuPrincipal(ctx context.Context) (*auth.Principal, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if p == nil || p.Type != auth.SubjectUser || p.ID == 0 || p.TenantID == 0 {
		return nil, quotapb.ErrorQuotaNotFound("%s", "resource not found")
	}
	return p, nil
}

func (s *GpuAcceptance) AcceptGpuCreate(ctx context.Context, key string, gpu *acc.GpuRequest, business proto.Message) (*data.QuotaOccupyResult, error) {
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.binding.AuthorizeGpu(ctx, p, s.binding.CreateAction(), ""); err != nil {
		return nil, err
	}
	_, resolvesBusiness := s.binding.(GpuBusinessResolver)
	if _, err = uuid.Parse(key); err != nil || (gpu == nil && !resolvesBusiness) || business == nil || !business.ProtoReflect().IsValid() {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid create request")
	}
	tenant, err := s.resolver.ResourceTenantID(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	if _, err = uuid.Parse(tenant); err != nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid resource tenant mapping")
	}
	actorID := strconv.FormatUint(uint64(p.ID), 10)
	var businessObject map[string]any
	var businessBytes []byte
	if canonical, ok := s.binding.(interface {
		CanonicalGpuBusiness(proto.Message) ([]byte, error)
	}); ok {
		businessBytes, err = canonical.CanonicalGpuBusiness(business)
		if err == nil {
			err = json.Unmarshal(businessBytes, &businessObject)
		}
	} else {
		businessObject, err = gpuCanonicalMessage(business.ProtoReflect())
		if err == nil {
			businessBytes, err = gpuCanonicalJSON(businessObject)
		}
	}
	if err != nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid business payload")
	}
	var gpuObject map[string]any
	if gpu != nil {
		gpuObject, err = gpuCanonicalMessage(gpu.ProtoReflect())
		if err != nil {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid GPU request")
		}
	}
	requestHash, err := gpuHash(map[string]any{"schema": "gov-gpu-create-v1", "tenant_id": tenant, "actor_type": string(p.Type), "actor_id": actorID, "owner": s.binding.OwnerService(), "action": s.binding.CreateAction(), "gpu_request": gpuObject, "business_type": string(business.ProtoReflect().Descriptor().FullName()), "business": businessObject})
	if err != nil {
		return nil, err
	}
	old, err := s.ledger.FindIdempotentOperation(ctx, p.TenantID, string(p.Type), actorID, s.binding.CreateAction(), key)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if old != nil {
		if old.OwnerService != s.binding.OwnerService() || old.ResourceTenantID != tenant || old.RequestHash != requestHash {
			return nil, quotapb.ErrorIdempotencyConflict("%s", "request differs from original acceptance")
		}
		charges, e := s.ledger.GetChargesForOperation(ctx, p.TenantID, old.OperationID)
		if e != nil {
			return nil, e
		}
		if gpuAccountingTerminal(old, charges) {
			ids := make([]string, 0, len(charges))
			for _, charge := range charges {
				ids = append(ids, charge.ChargeID)
			}
			return &data.QuotaOccupyResult{OperationID: old.OperationID, ResourceID: old.ResourceID, ChargeIDs: ids, Replayed: true}, nil
		}
		if data.IsInstanceCPUCanonical([]byte(old.CanonicalRequest)) {
			if s.binding.OwnerService() != InstanceOwner || !data.AllowsInstanceCPUCommand(old.OwnerService, old.Action, old.CanonicalRequest) || len(charges) != 0 {
				return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid original CPU instance command")
			}
			return &data.QuotaOccupyResult{OperationID: old.OperationID, ResourceID: old.ResourceID, ChargeIDs: []string{}, Replayed: true}, nil
		}
		c, e := DecodeGpuCanonical([]byte(old.CanonicalRequest))
		if e != nil {
			return nil, e
		}
		refs := make([]QuotaChargeRef, 0, len(charges))
		ids := make([]string, 0, len(charges))
		for _, q := range charges {
			refs = append(refs, QuotaChargeRef{ChargeID: q.ChargeID, QuotaCode: q.QuotaCode, ChargedUnits: q.OriginalUnits})
			ids = append(ids, q.ChargeID)
		}
		if e = ValidateGpuCharges(c, refs); e != nil {
			return nil, e
		}
		return &data.QuotaOccupyResult{OperationID: old.OperationID, ResourceID: old.ResourceID, ChargeIDs: ids, Replayed: true}, nil
	}
	if !s.registry.GPUExecutionEnabled(s.binding.OwnerService()) || s.registry.Lookup(s.binding.OwnerService(), s.binding.CreateAction()) == nil {
		return nil, quotapb.ErrorQuotaAdapterUnavailable("%s", "GPU business owner is not enabled")
	}
	if resolvesBusiness {
		gpu, business, err = s.binding.(GpuBusinessResolver).ResolveGpuBusiness(ctx, p, tenant, business)
		if err != nil {
			return nil, err
		}
		if business == nil || !business.ProtoReflect().IsValid() {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid resolved GPU business")
		}
		if canonical, ok := s.binding.(interface {
			CanonicalGpuBusiness(proto.Message) ([]byte, error)
		}); ok {
			businessBytes, err = canonical.CanonicalGpuBusiness(business)
		} else {
			businessObject, err = gpuCanonicalMessage(business.ProtoReflect())
			if err == nil {
				businessBytes, err = gpuCanonicalJSON(businessObject)
			}
		}
		if err != nil {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid resolved business payload")
		}
	}
	if instance, ok := business.(*res.CreateSpec); ok && instance != nil && instance.InstanceKind == "container" && instance.ComputeType == "cpu" {
		return s.acceptInstanceCPUCreate(ctx, p, tenant, actorID, key, requestHash, instance, businessBytes)
	}
	if gpu == nil || s.plans == nil {
		return nil, quotapb.ErrorQuotaAdapterUnavailable("%s", "GPU plan resolution unavailable")
	}
	extra, err := s.binding.ValidateGpuBusiness(ctx, business)
	if err != nil {
		return nil, err
	}
	plan, err := s.plans.ResolveGpuRequest(ctx, &acc.ResolveGpuRequestRequest{Context: &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant, Actor: &acc.Actor{Type: string(p.Type), Id: actorID}}, Gpu: gpu})
	if err != nil {
		return nil, err
	}
	item, err := GpuResolvedQuota(plan, gpu)
	if err != nil {
		return nil, err
	}
	if instance, ok := business.(*res.CreateSpec); ok {
		if plan.Selection == nil || plan.Selection.ProfileId != gpu.ProfileId || plan.Selection.ProfileVersion != gpu.ProfileVersion {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "resolved selection mismatch")
		}
		instance.GpuExecution = proto.Clone(plan.Execution).(*acc.GpuExecution)
		instance.GpuDisplay = proto.Clone(plan.Selection).(*acc.GpuDisplay)
		instance.GpuSelection = nil
		if err = res.ValidateCreateSpec(instance, true); err != nil {
			return nil, instanceBusinessError(err)
		}
		if check, ok := s.binding.(interface {
			ValidateResolvedInstancePlan(context.Context, *res.CreateSpec, *acc.ResolvedGpuRequest) error
		}); ok {
			if err = check.ValidateResolvedInstancePlan(ctx, instance, plan); err != nil {
				return nil, err
			}
		}
		businessBytes, err = res.CanonicalBusinessPayload(instance)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, quotapb.ErrorQuotaAdapterUnavailable("%s", "GPU owner execution is not integrated")
	}

	supported := false
	for _, code := range s.binding.GpuQuotaCodes() {
		if code == item.QuotaCode {
			supported = true
		}
	}
	if !supported {
		return nil, quotapb.ErrorQuotaAdapterUnavailable("%s", "GPU metering mode is not enabled for this owner action")
	}
	items := append([]data.QuotaOccupyItem{item}, extra...)
	sort.Slice(items, func(i, j int) bool { return items[i].QuotaCode < items[j].QuotaCode })
	sum := sha256.Sum256(businessBytes)
	c := GpuCanonical{SchemaVersion: 3, GpuRequest: gpu, BusinessPayload: businessBytes, BusinessPayloadDigest: hex.EncodeToString(sum[:]), MeteringVersion: GpuMeteringVersion}
	for _, q := range items {
		c.QuotaItems = append(c.QuotaItems, GpuQuotaItem{QuotaCode: q.QuotaCode, Units: q.Units})
	}
	canonical, err := gpuCanonicalJSON(c)
	if err != nil {
		return nil, err
	}
	if _, err = DecodeGpuCanonical(canonical); err != nil {
		return nil, err
	}
	result, err := s.ledger.Occupy(ctx, &data.QuotaOccupyInput{TenantID: p.TenantID, ResourceTenantID: tenant, ResourceID: uuid.NewString(), ActorType: string(p.Type), ActorID: actorID, OwnerService: s.binding.OwnerService(), Action: s.binding.CreateAction(), IdempotencyKey: key, RequestHash: requestHash, CanonicalRequest: string(canonical), Items: items})
	if err == nil && s.worker != nil {
		s.worker.Notify()
	}
	return result, err
}

// HasGpuCreate classifies this tenant's resource after current delete
// authorization. A queued CREATE may not yet exist in the downstream owner.
func (s *GpuAcceptance) HasGpuCreate(ctx context.Context, resource string) (bool, error) {
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return false, err
	}
	if err = s.binding.AuthorizeGpu(ctx, p, s.binding.DeleteAction(), resource); err != nil {
		return false, err
	}
	_, err = s.ledger.GetCreateOperationByResource(ctx, p.TenantID, s.binding.OwnerService(), resource)
	if ent.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (s *GpuAcceptance) AcceptGpuDelete(ctx context.Context, key, resource string) (*commandv1.GpuDeleteAcceptance, error) {
	return s.acceptGpuDelete(ctx, key, resource, 0)
}
func (s *GpuAcceptance) AcceptGpuDeleteVersion(ctx context.Context, key, resource string, version int64) (*commandv1.GpuDeleteAcceptance, error) {
	if version < 1 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "expected instance version required")
	}
	return s.acceptGpuDelete(ctx, key, resource, version)
}
func (s *GpuAcceptance) acceptGpuDelete(ctx context.Context, key, resource string, version int64) (*commandv1.GpuDeleteAcceptance, error) {
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.binding.AuthorizeGpu(ctx, p, s.binding.DeleteAction(), resource); err != nil {
		return nil, err
	}
	if _, err = uuid.Parse(key); err != nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid delete key")
	}
	if _, err = uuid.Parse(resource); err != nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid resource")
	}
	old, err := s.ledger.GetCreateOperationByResource(ctx, p.TenantID, s.binding.OwnerService(), resource)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, quotapb.ErrorQuotaNotFound("%s", "resource not found")
		}
		return nil, err
	}
	if data.IsInstanceCPUCanonical([]byte(old.CanonicalRequest)) {
		return s.acceptInstanceCPUDelete(ctx, p, key, resource, version, old)
	}
	actorID := strconv.FormatUint(uint64(p.ID), 10)
	object := map[string]any{"schema": "gov-gpu-delete-v1", "tenant_id": old.ResourceTenantID, "actor_type": string(p.Type), "actor_id": actorID, "owner": old.OwnerService, "action": s.binding.DeleteAction(), "resource_id": old.ResourceID, "create_operation_id": old.OperationID}
	if version > 0 {
		object["expected_version"] = version
	}
	hash, err := gpuHash(object)
	if err != nil {
		return nil, err
	}
	existing, lookupErr := s.ledger.FindIdempotentOperation(ctx, p.TenantID, string(p.Type), actorID, s.binding.DeleteAction(), key)
	if lookupErr != nil && !ent.IsNotFound(lookupErr) {
		return nil, lookupErr
	}
	if existing != nil {
		if existing.OwnerService != old.OwnerService || existing.ResourceTenantID != old.ResourceTenantID || existing.ResourceID != resource || existing.RequestHash != hash || existing.CreateOperationID == nil || *existing.CreateOperationID != old.OperationID {
			return nil, quotapb.ErrorIdempotencyConflict("%s", "delete request differs from original acceptance")
		}
		response := &commandv1.GpuDeleteAcceptance{DeleteOperationId: existing.OperationID, ResourceId: existing.ResourceID, Result: "QUEUED_FOR_OWNER", DispatchOperationId: existing.OperationID}
		if existing.DispatchState == "CANCELED_UNSENT" {
			response.Result = "CANCELED_BEFORE_DISPATCH"
			response.DispatchOperationId = ""
		}
		return response, nil
	}
	charges, err := s.ledger.GetChargesForOperation(ctx, p.TenantID, old.OperationID)
	if err != nil {
		return nil, err
	}
	if gpuAccountingTerminal(old, charges) {
		return nil, quotapb.ErrorQuotaNotFound("%s", "resource already released")
	}
	c, err := DecodeGpuCanonical([]byte(old.CanonicalRequest))
	if err != nil {
		return nil, err
	}
	refs := make([]QuotaChargeRef, 0, len(charges))
	for _, q := range charges {
		refs = append(refs, QuotaChargeRef{ChargeID: q.ChargeID, QuotaCode: q.QuotaCode, ChargedUnits: q.OriginalUnits})
	}
	if err = ValidateGpuCharges(c, refs); err != nil {
		return nil, err
	}
	canonical := old.CanonicalRequest
	if version > 0 {
		existing, e := s.ledger.FindIdempotentOperation(ctx, p.TenantID, string(p.Type), actorID, s.binding.DeleteAction(), key)
		if e != nil && !ent.IsNotFound(e) {
			return nil, e
		}
		if existing == nil {
			validator, ok := s.binding.(interface {
				ValidateGpuDelete(context.Context, *auth.Principal, string, string, int64) error
			})
			if !ok {
				return nil, quotapb.ErrorInvalidQuotaRequest("%s", "versioned delete unsupported")
			}
			if e = validator.ValidateGpuDelete(ctx, p, old.ResourceTenantID, resource, version); e != nil {
				return nil, e
			}
		}
		c.DeleteExpectedVersion = version
		raw, e := gpuCanonicalJSON(c)
		if e != nil {
			return nil, e
		}
		canonical = string(raw)
	}
	result, err := s.ledger.CreateDeleteOperation(ctx, &data.QuotaDeleteInput{TenantID: p.TenantID, ActorType: string(p.Type), ActorID: actorID, OwnerService: old.OwnerService, Action: s.binding.DeleteAction(), IdempotencyKey: key, RequestHash: hash, CanonicalRequest: canonical, ResourceID: resource})
	if err != nil {
		return nil, err
	}
	response := &commandv1.GpuDeleteAcceptance{DeleteOperationId: result.OperationID, ResourceId: result.ResourceID, Result: "QUEUED_FOR_OWNER", DispatchOperationId: result.OperationID}
	if result.LocalCanceled {
		response.Result = "CANCELED_BEFORE_DISPATCH"
		response.DispatchOperationId = ""
	}
	if s.worker != nil {
		s.worker.Notify()
	}
	return response, nil
}

// ResolveGpuUsageRef always starts with this tenant's immutable original row.
func (s *GpuAcceptance) ResolveGpuUsageRef(ctx context.Context, tenant uint32, owner, resource string) (*acc.GpuUsageRef, error) {
	return resolveGpuUsageRef(ctx, s.ledger, tenant, owner, resource)
}
func resolveGpuUsageRef(ctx context.Context, ledger *data.QuotaLedgerRepo, tenant uint32, owner, resource string) (*acc.GpuUsageRef, error) {
	if tenant == 0 || !supportedGpuOwner(owner) {
		return nil, quotapb.ErrorQuotaNotFound("%s", "resource not found")
	}
	op, err := ledger.GetCreateOperationByResource(ctx, tenant, owner, resource)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, quotapb.ErrorQuotaNotFound("%s", "resource not found")
		}
		return nil, err
	}
	charges, e := ledger.GetChargesForOperation(ctx, tenant, op.OperationID)
	if e != nil {
		return nil, e
	}
	gpu := false
	for _, q := range charges {
		gpu = gpu || q.QuotaCode == GpuPhysicalQuotaCode || q.QuotaCode == GpuSharedQuotaCode
	}
	if !gpu {
		return nil, quotapb.ErrorQuotaNotFound("%s", "GPU usage not found")
	}

	return &acc.GpuUsageRef{TenantId: op.ResourceTenantID, OwnerService: op.OwnerService, ResourceId: op.ResourceID, CreateOperationId: op.OperationID}, nil
}

// Terminal original accounting is read by reference and receipt. It never
// decodes an executable payload, regardless of when the operation was accepted.
func gpuAccountingTerminal(op *ent.QuotaOperation, charges []*ent.QuotaCharge) bool {
	if op == nil || (op.DispatchState != "ACKED" && op.DispatchState != "CANCELED_UNSENT") || len(charges) == 0 {
		return false
	}
	for _, q := range charges {
		if q == nil || q.TenantID == nil || op.TenantID == nil || *q.TenantID != *op.TenantID || q.OperationID != op.OperationID || q.OriginalUnits <= 0 || q.ReleasedUnits != q.OriginalUnits {
			return false
		}
	}
	return true
}
