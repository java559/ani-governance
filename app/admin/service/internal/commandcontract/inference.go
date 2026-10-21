package commandcontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/protobuf/proto"
)

const InferenceSchema = "inference-managed-v2"
const InferenceGPUClass = runtimev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_GPU

type InferenceCanonical struct {
	Schema                   string                         `json:"schema"`
	Usage                    string                         `json:"usage"`
	Selection                *compute.ComputeSelection      `json:"selection"`
	BusinessPayload          json.RawMessage                `json:"business_payload"`
	BusinessPayloadDigest    string                         `json:"business_payload_digest"`
	CreateCommandDigest      string                         `json:"create_command_digest"`
	CreateActorType          string                         `json:"create_actor_type"`
	CreateActorID            string                         `json:"create_actor_id"`
	ResourceClass            runtimev1.ManagedResourceClass `json:"resource_class"`
	ClusterID                string                         `json:"cluster_id"`
	GpuRequest               *acc.GpuRequest                `json:"gpu_request"`
	QuotaItems               []GpuQuotaItem                 `json:"quota_items"`
	DeleteExpectedGeneration int64                          `json:"delete_expected_generation,omitempty"`
	DeleteScope              *runtimev1.ManagedDeleteScope  `json:"delete_scope,omitempty"`
}

func IsInferenceCanonical(raw []byte) bool {
	var marker struct {
		Schema string `json:"schema"`
	}
	return json.Unmarshal(raw, &marker) == nil && marker.Schema == InferenceSchema
}
func DecodeInferenceCanonical(raw []byte) (*InferenceCanonical, error) {
	var c InferenceCanonical
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || c.Schema != InferenceSchema || c.Usage != "INFERENCE" || c.DeleteExpectedGeneration < 0 || compute.ValidateSelection(c.Selection) != nil || c.CreateCommandDigest == "" {
		return nil, Invalid("invalid frozen inference command")
	}
	encoded, e := CanonicalJSON(c)
	if e != nil || !bytes.Equal(encoded, raw) {
		return nil, Invalid("noncanonical inference command")
	}
	sum := sha256.Sum256(c.BusinessPayload)
	if hex.EncodeToString(sum[:]) != c.BusinessPayloadDigest {
		return nil, Invalid("inference business digest mismatch")
	}
	business, e := runtimev1.DecodeBusinessPayload(c.BusinessPayload)
	if e != nil {
		return nil, e
	}
	if business.GetResource() == nil {
		return nil, Invalid("missing inference resources")
	}
	wantCPU := strconv.FormatInt(c.Selection.CpuMillis, 10) + "m"
	wantRAM := strconv.FormatInt(c.Selection.MemoryMib, 10) + "Mi"
	for _, values := range []map[string]string{business.Resource.Requests, business.Resource.Limits} {
		if len(values) != 2 || values["cpu"] != wantCPU || values["memory"] != wantRAM {
			return nil, Invalid("inference selection resources mismatch")
		}
	}
	class, e := runtimev1.ManagedResourceClassForCreate(business)
	if e != nil || class != c.ResourceClass {
		return nil, Invalid("inference resource class mismatch")
	}
	if class == runtimev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_CPU {
		if c.Selection.ComputeType != "cpu" || c.Selection.Gpu != nil || c.GpuRequest != nil || len(c.QuotaItems) != 0 || c.ClusterID != "" {
			return nil, Invalid("CPU inference contains GPU accounting")
		}
	} else {
		g := c.Selection.Gpu
		if c.Selection.ComputeType != "gpu" || g == nil || c.GpuRequest == nil || len(c.QuotaItems) != 1 || c.ClusterID != g.ClusterId || business.ClusterId != g.ClusterId || business.GpuDisplay == nil || business.GpuDisplay.ProfileId != g.ProfileId || business.GpuDisplay.ProfileVersion != g.ProfileVersion || business.GpuDisplay.Mode != g.Mode || business.GpuDisplay.ModelKey != g.ModelKey {
			return nil, Invalid("inference GPU selection mismatch")
		}
		topology, e := runtimev1.NormalizeTopology(business.Runtime, business.Replicas, g.DevicesPerUnit)
		if e != nil {
			return nil, e
		}
		r := c.GpuRequest
		if r.ClusterId != g.ClusterId || r.PoolId != g.PoolId || r.ProfileId != g.ProfileId || r.ProfileVersion != g.ProfileVersion || r.Replicas != topology.ExecutionReplicas || r.DevicesPerReplica != topology.DevicesPerReplica || r.ContainerName != topology.ContainerName {
			return nil, Invalid("inference GPU topology mismatch")
		}
		if !proto.Equal(r, &acc.GpuRequest{ClusterId: business.Resource.Gpu.ClusterId, PoolId: business.Resource.Gpu.PoolId, ProfileId: business.Resource.Gpu.ProfileId, ProfileVersion: business.Resource.Gpu.ProfileVersion, Replicas: business.Resource.Gpu.Replicas, DevicesPerReplica: business.Resource.Gpu.DevicesPerReplica, ContainerName: business.Resource.Gpu.ContainerName}) {
			return nil, Invalid("inference GPU request mismatch")
		}
		q := c.QuotaItems[0]
		if _, e = GpuResolvedQuota(&acc.ResolvedGpuRequest{Execution: business.GpuExecution, Selection: business.GpuDisplay, QuotaItems: []*acc.GpuQuotaItem{{QuotaCode: q.QuotaCode, Units: q.Units}}}, r); e != nil {
			return nil, e
		}
		memory, e := strconv.ParseInt(business.GpuExecution.Resources["volcano.sh/vgpu-memory"], 10, 64)
		if business.GpuExecution.Resources["volcano.sh/vgpu-cores"] != strconv.FormatUint(uint64(g.CoreLimitPercent), 10) {
			return nil, Invalid("inference GPU core limit mismatch")
		}
		if e != nil || memory != g.MemoryMibPerDevice {
			return nil, Invalid("inference GPU memory mismatch")
		}
	}
	return &c, nil
}
func validateInferenceStored(in StoredCommand) (*Facts, error) {
	if in.Action != InferenceCreateAction && in.Action != InferenceDeleteAction {
		return nil, Invalid("unsupported inference action")
	}
	c, e := DecodeInferenceCanonical(in.Canonical)
	if e != nil {
		return nil, e
	}
	deletion := in.Action == InferenceDeleteAction
	if deletion != (in.CreateOperationID != "") || deletion != (c.DeleteScope != nil) {
		return nil, Invalid("invalid inference deletion association")
	}
	business, e := runtimev1.DecodeBusinessPayload(c.BusinessPayload)
	if e != nil {
		return nil, e
	}
	createID := in.OperationID
	if deletion {
		createID = in.CreateOperationID
	}
	business.Context = &runtimev1.CommandContext{TenantId: in.TenantID, ResourceId: in.ResourceID, OperationId: createID}
	digest, e := runtimev1.ManagedCreateCommandHash(business, in.TenantID, c.CreateActorType, c.CreateActorID)
	if e != nil || digest != c.CreateCommandDigest || (!deletion && (in.ActorType != c.CreateActorType || in.ActorID != c.CreateActorID)) {
		return nil, Invalid("inference command identity mismatch")
	}
	business.Context.RequestDigest = digest
	if e = runtimev1.ValidateManagedCreate(business); e != nil {
		return nil, e
	}

	if c.ResourceClass == runtimev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_CPU {
		if len(in.Charges) != 0 {
			return nil, Invalid("CPU inference cannot have charges")
		}
	} else if e = validateVector(c.QuotaItems, in.Charges, in.RequireChargeIDs); e != nil {
		return nil, e
	}
	if deletion {
		scope := c.DeleteScope
		if scope.ResourceClass != c.ResourceClass || scope.ClusterId != c.ClusterID || scope.CreateCommandDigest != c.CreateCommandDigest {
			return nil, Invalid("inference delete scope mismatch")
		}
		request := &runtimev1.DeleteInferenceServiceRequest{ResourceId: in.ResourceID, CreateOperationId: in.CreateOperationID, ExpectedGeneration: c.DeleteExpectedGeneration, ManagedDeleteScope: scope, Context: &runtimev1.CommandContext{TenantId: in.TenantID, ResourceId: in.ResourceID, OperationId: in.OperationID}}
		hash, e := runtimev1.ManagedDeleteCommandHash(request, in.TenantID, in.ActorType, in.ActorID)
		if e != nil {
			return nil, e
		}
		request.Context.RequestDigest = hash
		if e = runtimev1.ValidateManagedDelete(request); e != nil {
			return nil, e
		}
	}
	items := make([]QuotaItem, 0, len(c.QuotaItems))
	for _, q := range c.QuotaItems {
		items = append(items, QuotaItem{q.QuotaCode, q.Units})
	}
	return &Facts{CPU: c.ResourceClass == runtimev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_CPU, BusinessPayload: c.BusinessPayload, GpuRequest: c.GpuRequest, Execution: business.GpuExecution, Display: business.GpuDisplay, QuotaItems: items, Inference: c}, nil
}
