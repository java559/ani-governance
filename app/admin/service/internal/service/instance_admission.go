package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/proto"
)

// Instance identity and persistence use shared admission. Only this binding
// interprets Resource's CreateSpec and preserves its current signed bytes.
func (b *InstanceGpuBinding) PrepareManagedBusiness(ctx context.Context, p *auth.Principal, tenant string, public proto.Message) (*preparedManagedBusiness, error) {
	gpu, message, e := b.ResolveGpuBusiness(ctx, p, tenant, public)
	if e != nil {
		return nil, e
	}
	business, ok := message.(*res.CreateSpec)
	if !ok || business == nil || business.InstanceContractVersion != data.CurrentInstanceContractVersion {
		return nil, commandcontract.Invalid("invalid instance contract")
	}
	if gpu != nil {
		if _, e = b.ValidateGpuBusiness(ctx, business); e != nil {
			return nil, e
		}
	}
	return &preparedManagedBusiness{gpu: gpu, instanceContractVersion: data.CurrentInstanceContractVersion, freeze: func(_, _, _, _, _ string, plan *acc.ResolvedGpuRequest) ([]byte, error) {
		frozen := proto.Clone(business).(*res.CreateSpec)
		if gpu == nil {
			if plan != nil || frozen.ComputeType != "cpu" || frozen.GpuSelection != nil || frozen.GpuExecution != nil || frozen.GpuDisplay != nil {
				return nil, commandcontract.Invalid("invalid CPU instance resolution")
			}
			raw, e := res.CanonicalBusinessPayload(frozen)
			if e != nil {
				return nil, e
			}
			digest, e := res.BusinessPayloadDigest(frozen)
			if e != nil {
				return nil, e
			}
			return commandcontract.MarshalInstanceCPUCanonical(&commandcontract.InstanceCPUCanonical{Schema: commandcontract.InstanceCPUCanonicalSchema, InstanceContractVersion: data.CurrentInstanceContractVersion, BusinessPayload: raw, BusinessPayloadDigest: digest})
		}
		item, e := commandcontract.GpuResolvedQuota(plan, gpu)
		if e != nil {
			return nil, e
		}
		if e = b.ValidateResolvedInstancePlan(ctx, frozen, plan); e != nil {
			return nil, e
		}
		frozen.GpuExecution = proto.Clone(plan.Execution).(*acc.GpuExecution)
		frozen.GpuDisplay = proto.Clone(plan.Selection).(*acc.GpuDisplay)
		frozen.GpuSelection = nil
		if e = res.ValidateCreateSpec(frozen, true); e != nil {
			return nil, instanceBusinessError(e)
		}
		raw, e := res.CanonicalBusinessPayload(frozen)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(raw)
		return commandcontract.CanonicalJSON(commandcontract.GpuCanonical{SchemaVersion: 3, InstanceContractVersion: data.CurrentInstanceContractVersion, GpuRequest: gpu, BusinessPayload: raw, BusinessPayloadDigest: hex.EncodeToString(sum[:]), MeteringVersion: commandcontract.GpuMeteringVersion, QuotaItems: []commandcontract.GpuQuotaItem{{QuotaCode: item.QuotaCode, Units: item.Units}}})
	}}, nil
}
func (b *InstanceGpuBinding) instanceIntentHash(tenant, actorType, actorID string, gpu *acc.GpuRequest, business proto.Message, version uint32) (string, error) {
	raw, e := b.CanonicalGpuBusinessVersion(business, version)
	if e != nil {
		return "", e
	}
	var object map[string]any
	if e = json.Unmarshal(raw, &object); e != nil {
		return "", e
	}
	var gpuObject map[string]any
	if gpu != nil {
		gpuObject, e = commandcontract.CanonicalMessage(gpu.ProtoReflect())
		if e != nil {
			return "", e
		}
	}
	return commandcontract.Hash(map[string]any{"schema": "gov-instance-create-v2", "tenant_id": tenant, "actor_type": actorType, "actor_id": actorID, "owner": b.OwnerService(), "action": b.CreateAction(), "gpu_request": gpuObject, "business_type": string(business.ProtoReflect().Descriptor().FullName()), "business": object})
}
