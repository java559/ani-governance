package service

import (
	"context"
	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/proto"
)

// A compiled owner prepares its business snapshot. Identity, accounting and
// persistence are owned by the existing shared admission mechanism.
type preparedManagedBusiness struct {
	gpu                     *acc.GpuRequest
	instanceContractVersion uint32
	freeze                  func(tenant, actorType, actorID, operationID, resourceID string, plan *acc.ResolvedGpuRequest) ([]byte, error)
}
type managedBusinessPreparer interface {
	PrepareManagedBusiness(context.Context, *auth.Principal, string, proto.Message) (*preparedManagedBusiness, error)
}

func (s *GpuAcceptance) acceptPreparedCreate(ctx context.Context, p *auth.Principal, tenant, actorID, key, hash string, grant gpuBusinessGrant, binding managedBusinessPreparer, public proto.Message) (*data.QuotaOccupyResult, error) {
	prepared, err := binding.PrepareManagedBusiness(ctx, p, tenant, public)
	if err != nil {
		return nil, err
	}
	if prepared == nil || prepared.freeze == nil {
		return nil, commandcontract.Invalid("missing managed business")
	}
	var plan *acc.ResolvedGpuRequest
	items := []data.QuotaOccupyItem{}
	if prepared.gpu != nil {
		eligibility, ok := s.binding.(GpuUseEligibility)
		if !ok {
			return nil, gpuUseUnavailable()
		}
		if err = authorizeNewGpuUse(ctx, grant, eligibility); err != nil {
			return nil, err
		}
		if s.plans == nil {
			return nil, commandcontract.Invalid("GPU plan resolver unavailable")
		}
		plan, err = s.plans.ResolveGpuRequest(ctx, &acc.ResolveGpuRequestRequest{Context: &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant, Actor: &acc.Actor{Type: string(p.Type), Id: actorID}}, Gpu: prepared.gpu})
		if err != nil {
			return nil, err
		}
		item, e := commandcontract.GpuResolvedQuota(plan, prepared.gpu)
		if e != nil {
			return nil, e
		}
		supported := false
		for _, code := range s.binding.GpuQuotaCodes() {
			supported = supported || code == item.QuotaCode
		}
		if !supported {
			return nil, commandcontract.Invalid("GPU metering is not enabled for owner")
		}
		items = append(items, data.QuotaOccupyItem{QuotaCode: item.QuotaCode, Units: item.Units})
	}
	operationID, resourceID := newCommandIdentity()
	raw, err := prepared.freeze(tenant, string(p.Type), actorID, operationID, resourceID, plan)
	if err != nil {
		return nil, err
	}
	result, err := s.ledger.Occupy(ctx, &data.QuotaOccupyInput{TenantID: p.TenantID, ResourceTenantID: tenant, ResourceID: resourceID, OperationID: operationID, ActorType: string(p.Type), ActorID: actorID, OwnerService: s.binding.OwnerService(), Action: s.binding.CreateAction(), IdempotencyKey: key, RequestHash: hash, CanonicalRequest: string(raw), Items: items, InstanceContractVersion: prepared.instanceContractVersion})
	if err == nil && s.worker != nil {
		s.worker.Notify()
	}
	return result, err
}

// Identity is allocated by shared admission, then supplied to the owner binding.
func newCommandIdentity() (operationID, resourceID string) { return uuid.NewString(), uuid.NewString() }
