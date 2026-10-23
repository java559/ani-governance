package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/google/uuid"
	contractv1 "github.com/liangzai006/ani-inference-service/api/inference/contract/v1"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type InferenceComputeClient interface {
	ResolveComputePreset(context.Context, string, string, *compute.ResolveComputePresetRequest) (*compute.ComputeSelection, error)
}

func NewInferenceComputeBinding(client InferenceOwnerClient, authorization InferenceActionAuthorizer, model InferenceModelClient, computeClient InferenceComputeClient) *InferenceGpuBinding {
	return &InferenceGpuBinding{client: client, authorization: authorization, model: model, compute: computeClient}
}
func validateInferenceCreateIntent(d *view.CreateInferenceRequest) error {
	if d == nil || len(d.ProtoReflect().GetUnknown()) != 0 {
		return status.Error(codes.InvalidArgument, "invalid inference request")
	}
	if d.Cpu != "" || d.Memory != "" || d.Gpu != nil {
		return status.Error(codes.InvalidArgument, "cpu, memory and gpu input are retired; use preset_id")
	}
	for field, value := range map[string]string{"idempotency_key": d.IdempotencyKey, "preset_id": d.PresetId, "name": d.Name, "model_version_id": d.ModelVersionId, "served_model_name": d.ServedModelName, "engine_image": d.EngineImage, "engine_type": d.EngineType, "command": d.Command} {
		if strings.TrimSpace(value) == "" {
			return status.Errorf(codes.InvalidArgument, "%s is required", field)
		}
	}
	if id, e := uuid.Parse(d.PresetId); e != nil || id == uuid.Nil || id.String() != d.PresetId {
		return status.Error(codes.InvalidArgument, "invalid preset_id")
	}
	if _, err := contractv1.NormalizeTopology(&runtimev1.RuntimeSpec{Mode: runtimev1.RuntimeMode(d.RuntimeMode), WorkerReplicas: d.WorkerReplicas, Provider: "kserve"}, d.Replicas, 0); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return nil
}
func (b *InferenceGpuBinding) IntentHash(tenant, actorType, actorID string, message proto.Message) (string, error) {
	d, ok := message.(*view.CreateInferenceRequest)
	if !ok {
		return "", commandcontract.Invalid("invalid inference intent")
	}
	if err := validateInferenceCreateIntent(d); err != nil {
		return "", err
	}
	intent := proto.Clone(d).(*view.CreateInferenceRequest)
	intent.IdempotencyKey = ""
	if intent.RuntimeMode == 0 {
		intent.RuntimeMode = 1
	}
	business, e := commandcontract.CanonicalMessage(intent.ProtoReflect())
	if e != nil {
		return "", e
	}
	return commandcontract.Hash(map[string]any{"schema": "gov-inference-intent-v2", "tenant_id": tenant, "actor_type": actorType, "actor_id": actorID, "owner": commandcontract.InferenceOwner, "action": InferenceCreateAction, "usage": "INFERENCE", "business": business})
}
func (b *InferenceGpuBinding) PrepareManagedBusiness(ctx context.Context, p *auth.Principal, tenant string, message proto.Message) (*preparedManagedBusiness, error) {
	d, ok := message.(*view.CreateInferenceRequest)
	if !ok || p == nil {
		return nil, commandcontract.Invalid("invalid inference intent")
	}
	if err := validateInferenceCreateIntent(d); err != nil {
		return nil, err
	}
	if b.compute == nil {
		return nil, status.Error(codes.Unavailable, "COMPUTE_ADAPTER_UNAVAILABLE")
	}
	actor, e := p.Actor()
	if e != nil {
		return nil, e
	}
	selection, e := b.compute.ResolveComputePreset(ctx, tenant, actor, &compute.ResolveComputePresetRequest{TenantId: tenant, PresetId: d.PresetId, Usage: compute.Usage_INFERENCE})
	if e != nil {
		return nil, e
	}
	if compute.ValidateSelection(selection) != nil || selection.PresetId != d.PresetId {
		return nil, status.Error(codes.Unavailable, "COMPUTE_INVALID_RESPONSE")
	}
	selection = proto.Clone(selection).(*compute.ComputeSelection)
	request, e := new(InferenceService).buildCreateRequest(d)
	if e != nil {
		return nil, e
	}
	devices := uint32(0)
	if selection.Gpu != nil {
		devices = selection.Gpu.DevicesPerUnit
	}
	topology, e := contractv1.NormalizeTopology(request.Runtime, request.Replicas, devices)
	if e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	request.Resource = &runtimev1.ResourceSpec{Requests: map[string]string{"cpu": strconv.FormatInt(selection.CpuMillis, 10) + "m", "memory": strconv.FormatInt(selection.MemoryMib, 10) + "Mi"}, Limits: map[string]string{"cpu": strconv.FormatInt(selection.CpuMillis, 10) + "m", "memory": strconv.FormatInt(selection.MemoryMib, 10) + "Mi"}}
	var gpu *acc.GpuRequest
	if g := selection.Gpu; g != nil {
		gpu = &acc.GpuRequest{ClusterId: g.ClusterId, PoolId: g.PoolId, ProfileId: g.ProfileId, ProfileVersion: g.ProfileVersion, Replicas: topology.ExecutionReplicas, DevicesPerReplica: topology.DevicesPerReplica, ContainerName: topology.ContainerName}
		request.Resource.Gpu = &runtimev1.GpuRequest{ClusterId: gpu.ClusterId, PoolId: gpu.PoolId, ProfileId: gpu.ProfileId, ProfileVersion: gpu.ProfileVersion, Replicas: gpu.Replicas, DevicesPerReplica: gpu.DevicesPerReplica, ContainerName: gpu.ContainerName}
		request.ClusterId = g.ClusterId
	}
	if err := checkInferenceModelReady(ctx, b.model, tenant, actor, d.ModelVersionId); err != nil {
		return nil, err
	}
	return &preparedManagedBusiness{gpu: gpu, freeze: func(tenant, actorType, actorID, operationID, resourceID string, plan *acc.ResolvedGpuRequest) ([]byte, error) {
		frozen := proto.Clone(request).(*runtimev1.CreateInferenceServiceRequest)
		items := []commandcontract.GpuQuotaItem{}
		if gpu != nil {
			if plan == nil || plan.Selection == nil || plan.Execution == nil || plan.Selection.Mode != selection.Gpu.Mode || plan.Selection.ModelKey != selection.Gpu.ModelKey {
				return nil, commandcontract.Invalid("resolved inference GPU selection mismatch")
			}
			q, e := commandcontract.GpuResolvedQuota(plan, gpu)
			if e != nil {
				return nil, e
			}
			frozen.GpuExecution = proto.Clone(plan.Execution).(*acc.GpuExecution)
			frozen.GpuDisplay = proto.Clone(plan.Selection).(*acc.GpuDisplay)
			items = append(items, commandcontract.GpuQuotaItem{QuotaCode: q.QuotaCode, Units: q.Units})
		} else if plan != nil {
			return nil, commandcontract.Invalid("CPU inference cannot have GPU plan")
		}
		frozen.Context = &runtimev1.CommandContext{TenantId: tenant, ResourceId: resourceID, OperationId: operationID}
		digest, e := contractv1.ManagedCreateCommandHash(frozen, tenant, actorType, actorID)
		if e != nil {
			return nil, e
		}
		frozen.Context.RequestDigest = digest
		if e = contractv1.ValidateManagedCreate(frozen); e != nil {
			return nil, e
		}
		raw, e := contractv1.CanonicalBusinessPayload(frozen)
		if e != nil {
			return nil, e
		}
		class, e := contractv1.ManagedResourceClassForCreate(frozen)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(raw)
		c := commandcontract.InferenceCanonical{Schema: commandcontract.InferenceSchema, Usage: "INFERENCE", Selection: selection, BusinessPayload: raw, BusinessPayloadDigest: hex.EncodeToString(sum[:]), CreateCommandDigest: digest, CreateActorType: actorType, CreateActorID: actorID, ResourceClass: class, ClusterID: frozen.ClusterId, GpuRequest: gpu, QuotaItems: items}
		canonical, e := commandcontract.CanonicalJSON(c)
		if e != nil {
			return nil, e
		}
		if _, e = commandcontract.DecodeInferenceCanonical(canonical); e != nil {
			return nil, e
		}
		return canonical, nil
	}}, nil
}
func (b *InferenceGpuBinding) AuthorizeGpuUse(ctx context.Context, tenant, user uint32) error {
	eligibility, ok := b.authorization.(GpuUseEligibility)
	if !ok {
		return gpuUseUnavailable()
	}
	return eligibility.AuthorizeGpuUse(ctx, tenant, user)
}
