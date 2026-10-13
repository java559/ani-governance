package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"math"
	"sort"
	"strconv"

	attachment "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/integration/v1"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"go-wind-admin/app/admin/service/internal/data"
)

const (
	GpuPhysicalQuotaCode = "gpu.physical.count"
	GpuSharedQuotaCode   = "gpu.shared_memory_mib"
	GpuMeteringVersion   = "gpu-metering-v1"
)

// gpuCanonicalMessage maps the public protobuf to the versioned domain snapshot:
// all fields are present, absent messages are null, enums are decimal integers.
// This deliberately does not hash protojson (enum names/default omission differ).
func gpuCanonicalMessage(m protoreflect.Message) (map[string]any, error) {
	if len(m.GetUnknown()) != 0 {
		return nil, fmt.Errorf("unknown canonical fields")
	}
	out := make(map[string]any)
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.IsMap() {
			return nil, fmt.Errorf("canonical map unsupported")
		}
		var value any
		var err error
		if f.IsList() {
			list := m.Get(f).List()
			values := make([]any, 0, list.Len())
			for j := 0; j < list.Len(); j++ {
				v, e := gpuCanonicalScalar(f, list.Get(j))
				if e != nil {
					return nil, e
				}
				values = append(values, v)
			}
			value = values
		} else if f.Kind() == protoreflect.MessageKind && !m.Has(f) {
			value = nil
		} else {
			value, err = gpuCanonicalScalar(f, m.Get(f))
		}
		if err != nil {
			return nil, err
		}
		out[string(f.Name())] = value
	}
	return out, nil
}

func gpuCanonicalScalar(f protoreflect.FieldDescriptor, v protoreflect.Value) (any, error) {
	switch f.Kind() {
	case protoreflect.MessageKind:
		return gpuCanonicalMessage(v.Message())
	case protoreflect.StringKind:
		return v.String(), nil
	case protoreflect.BoolKind:
		return v.Bool(), nil
	case protoreflect.EnumKind:
		n := int64(v.Enum())
		if n < 0 {
			return nil, fmt.Errorf("negative enum")
		}
		return strconv.FormatInt(n, 10), nil
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind, protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		n := v.Int()
		if n < 0 {
			return nil, fmt.Errorf("negative canonical integer")
		}
		return strconv.FormatInt(n, 10), nil
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		n := v.Uint()
		if n > math.MaxInt64 {
			return nil, fmt.Errorf("canonical integer overflow")
		}
		return strconv.FormatUint(n, 10), nil
	default:
		return nil, fmt.Errorf("unsupported canonical scalar")
	}
}

func gpuCanonicalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}
func gpuHash(v any) (string, error) {
	b, err := gpuCanonicalJSON(v)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(append([]byte("acc-c14n-v1\n"), b...))
	return hex.EncodeToString(s[:]), nil
}

func gpuSortFields(items []*acc.KeyValue) ([]*acc.KeyValue, error) {
	c := append([]*acc.KeyValue{}, items...)
	for _, x := range c {
		if x == nil || x.Key == "" || x.Value == "" {
			return nil, fmt.Errorf("empty managed key")
		}
	}
	sort.Slice(c, func(i, j int) bool { return c[i].Key < c[j].Key })
	for i := 1; i < len(c); i++ {
		if c[i-1].Key == c[i].Key {
			return nil, fmt.Errorf("duplicate managed key")
		}
	}
	return c, nil
}

func GpuPlanDigest(p *acc.ResolvedGpuPlan) (string, error) {
	if p == nil || p.Profile == nil || p.Runtime == nil {
		return "", fmt.Errorf("invalid plan")
	}
	c := proto.Clone(p).(*acc.ResolvedGpuPlan)
	c.ResolutionDigest = ""
	c.Profile.Published = false
	var err error
	if c.Runtime.NodeLabels, err = gpuSortFields(c.Runtime.NodeLabels); err != nil {
		return "", err
	}
	if c.Runtime.PodAnnotations, err = gpuSortFields(c.Runtime.PodAnnotations); err != nil {
		return "", err
	}
	if c.Runtime.LimitsPerContainer, err = gpuSortFields(c.Runtime.LimitsPerContainer); err != nil {
		return "", err
	}
	v, err := gpuCanonicalMessage(c.ProtoReflect())
	if err != nil {
		return "", err
	}
	return gpuHash(v)
}

func gpuSpecDigest(p *acc.GpuProfile) (string, error) {
	if p == nil || p.Spec == nil {
		return "", fmt.Errorf("invalid spec")
	}
	v, err := gpuCanonicalMessage(p.Spec.ProtoReflect())
	if err != nil {
		return "", err
	}
	return gpuHash(map[string]any{"spec": v, "baseline_id": p.BaselineId})
}

func GpuProjectionDigest(p *acc.GpuUsageProjection) (string, error) {
	if p == nil || p.Ref == nil || p.Plan == nil {
		return "", fmt.Errorf("invalid projection")
	}
	ref, err := gpuCanonicalMessage(p.Ref.ProtoReflect())
	if err != nil {
		return "", err
	}
	return gpuHash(map[string]any{"ref": ref, "revision": strconv.FormatUint(p.Revision, 10), "state": strconv.FormatInt(int64(p.State), 10), "plan_digest": p.Plan.ResolutionDigest, "end_reason": strconv.FormatInt(int64(p.EndReason), 10), "source_fact_ref": p.SourceFactRef, "source_operation_created_at": p.SourceOperationCreatedAt})
}

// GpuPlanQuota validates the frozen metering and static rendering independently.
// Capacity is deliberately absent: zero/unknown capacity is not a reservation.
func GpuPlanQuota(p *acc.ResolvedGpuPlan) (data.QuotaOccupyItem, error) {
	bad := func() (data.QuotaOccupyItem, error) {
		return data.QuotaOccupyItem{}, quotapb.ErrorInvalidQuotaRequest("%s", "invalid frozen GPU plan")
	}
	if p == nil || p.SchemaVersion != 1 || p.Request == nil || p.Profile == nil || p.Profile.Spec == nil || p.Encoding == nil || p.Totals == nil || p.Runtime == nil {
		return bad()
	}
	r, s, e, t, rt := p.Request, p.Profile.Spec, p.Encoding, p.Totals, p.Runtime
	if r.Replicas < 1 || r.Replicas > 16 || r.DevicesPerReplica != 1 || r.ContainerName == "" || r.ProfileVersion == 0 || r.ProfileId != p.Profile.ProfileId || r.ProfileVersion != p.Profile.ProfileVersion || s.MaxDevicesPerReplica != 1 || e.MemoryBlockMib == 0 || e.MemoryBlockMib > math.MaxInt32 || e.Policy != "EXACT" {
		return bad()
	}
	d, err := GpuPlanDigest(p)
	if err != nil || d != p.ResolutionDigest {
		return bad()
	}
	d, err = gpuSpecDigest(p.Profile)
	if err != nil || d != p.Profile.SpecDigest {
		return bad()
	}
	if len(p.BaselineDigest) != 64 || rt.SchedulerName != "volcano" || rt.RecipeVersion != "volcano-hami-v1" || rt.QueueName == "" {
		return bad()
	}
	labels := map[string]string{"accelerator.ani.io/supply-group": s.GroupId, "accelerator.ani.io/model-key": s.ModelKey, "accelerator.ani.io/baseline-id": p.Profile.BaselineId}
	annotations := map[string]string{"volcano.sh/vgpu-mode": "hami-core"}
	limits := map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-cores": strconv.FormatUint(uint64(s.CoreLimitPercent), 10)}
	item := data.QuotaOccupyItem{}
	switch s.Mode {
	case acc.SupplyMode_SHARED_FIXED:
		m, f := s.SharedMemoryMib, int64(e.MemoryBlockMib)
		if m <= 0 || m > math.MaxInt32 || m%f != 0 || m/f > math.MaxInt32 || m > math.MaxInt64/int64(r.Replicas) || s.CoreLimitPercent < 1 || s.CoreLimitPercent > 99 || s.IsolationClass != "SOFTWARE_COOPERATIVE" || e.MemoryBlocksPerDevice != m/f || e.SharedMemoryMib != m || e.MemoryPercentage != 0 || t.ExclusiveDeviceCount != 0 || t.LogicalDeviceCount != int64(r.Replicas) || t.SharedMemoryMib != m*int64(r.Replicas) {
			return bad()
		}
		item = data.QuotaOccupyItem{QuotaCode: GpuSharedQuotaCode, Units: t.SharedMemoryMib}
		limits["volcano.sh/vgpu-memory"] = strconv.FormatInt(m/f, 10)
	case acc.SupplyMode_WHOLE_EXCLUSIVE:
		if s.SharedMemoryMib != 0 || s.CoreLimitPercent != 100 || s.IsolationClass != "WHOLE_DEVICE_EXCLUSIVE" || e.MemoryBlocksPerDevice != 0 || e.SharedMemoryMib != 0 || e.MemoryPercentage != 100 || t.SharedMemoryMib != 0 || t.LogicalDeviceCount != 0 || t.ExclusiveDeviceCount != int64(r.Replicas) {
			return bad()
		}
		item = data.QuotaOccupyItem{QuotaCode: GpuPhysicalQuotaCode, Units: t.ExclusiveDeviceCount}
		limits["volcano.sh/vgpu-memory-percentage"] = "100"
	default:
		return bad()
	}
	match := func(a []*acc.KeyValue, want map[string]string) bool {
		if len(a) != len(want) {
			return false
		}
		for _, v := range a {
			if v == nil || want[v.Key] != v.Value || v.Value == "" {
				return false
			}
		}
		return true
	}
	if !match(rt.NodeLabels, labels) || !match(rt.PodAnnotations, annotations) || !match(rt.LimitsPerContainer, limits) {
		return bad()
	}
	return item, nil
}

type GpuQuotaItem struct {
	QuotaCode string `json:"quota_code"`
	Units     int64  `json:"units"`
}

// GpuCanonical is the immutable original request/plan, shared by recovery and
// dispatch. IDs and created_at are obtained from the same original ledger row.
type GpuCanonical struct {
	SchemaVersion         uint32               `json:"schema_version"`
	GpuRequest            *acc.GpuRequest      `json:"gpu_request"`
	GpuPlan               *acc.ResolvedGpuPlan `json:"gpu_plan"`
	BusinessPayload       json.RawMessage      `json:"business_payload"`
	BusinessPayloadDigest string               `json:"business_payload_digest"`
	MeteringVersion       string               `json:"metering_version"`
	QuotaItems            []GpuQuotaItem       `json:"quota_items"`
	DeleteExpectedVersion int64                `json:"delete_expected_version,omitempty"`
}

func isGpuCanonical(raw []byte) bool {
	var marker struct {
		SchemaVersion uint32          `json:"schema_version"`
		GpuPlan       json.RawMessage `json:"gpu_plan"`
	}
	return json.Unmarshal(raw, &marker) == nil && (marker.SchemaVersion == 2 || len(marker.GpuPlan) > 0)
}

func DecodeGpuCanonical(raw []byte) (*GpuCanonical, error) {
	var c GpuCanonical
	if json.Unmarshal(raw, &c) != nil || c.SchemaVersion != 2 || c.DeleteExpectedVersion < 0 || c.MeteringVersion != GpuMeteringVersion || c.GpuRequest == nil || c.GpuPlan == nil || !proto.Equal(c.GpuRequest, c.GpuPlan.Request) || len(c.QuotaItems) == 0 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid GPU canonical")
	}
	item, err := GpuPlanQuota(c.GpuPlan)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	found := false
	for _, q := range c.QuotaItems {
		if q.QuotaCode == "" || q.Units <= 0 || seen[q.QuotaCode] {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid frozen charge vector")
		}
		seen[q.QuotaCode] = true
		if q.QuotaCode == GpuPhysicalQuotaCode || q.QuotaCode == GpuSharedQuotaCode {
			if q.QuotaCode != item.QuotaCode || q.Units != item.Units {
				return nil, quotapb.ErrorInvalidQuotaRequest("%s", "GPU metering mismatch")
			}
			found = true
		}
	}
	if !found {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "missing GPU metering")
	}
	sum := sha256.Sum256(c.BusinessPayload)
	if hex.EncodeToString(sum[:]) != c.BusinessPayloadDigest {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "business payload digest mismatch")
	}
	return &c, nil
}

func GpuChargeSubset(c *GpuCanonical, charges []QuotaChargeRef) ([]*attachment.GpuChargeRef, error) {
	if c == nil || len(charges) != len(c.QuotaItems) {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "incomplete original charges")
	}
	want := map[string]int64{}
	for _, q := range c.QuotaItems {
		want[q.QuotaCode] = q.Units
	}
	seenCode, seenID := map[string]bool{}, map[string]bool{}
	var gpu []*attachment.GpuChargeRef
	for _, q := range charges {
		if q.ChargeID == "" || seenID[q.ChargeID] || seenCode[q.QuotaCode] || q.ChargedUnits <= 0 || want[q.QuotaCode] != q.ChargedUnits {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "original charges mismatch")
		}
		seenID[q.ChargeID], seenCode[q.QuotaCode] = true, true
		if q.QuotaCode == GpuPhysicalQuotaCode || q.QuotaCode == GpuSharedQuotaCode {
			gpu = append(gpu, &attachment.GpuChargeRef{ChargeId: q.ChargeID, QuotaCode: q.QuotaCode, OriginalUnits: q.ChargedUnits})
		}
	}
	if len(gpu) != 1 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid GPU charge subset")
	}
	return gpu, nil
}

// BuildGpuOwnerAttachments is used only by a compiled, business-specific adapter.
// It cannot select an RPC or owner supplied by a public caller.
func BuildGpuOwnerAttachments(owner string, cmd *QuotaDispatchCommand) (*attachment.GpuOwnerCreateAttachment, *attachment.GpuOwnerDeleteAttachment, error) {
	if cmd == nil || !supportedGpuOwner(owner) || cmd.Actor.Type == "" || cmd.Actor.ID == "" {
		return nil, nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid owner command")
	}
	c, err := DecodeGpuCanonical(cmd.CanonicalRequest)
	if err != nil {
		return nil, nil, err
	}
	charges, err := GpuChargeSubset(c, cmd.Charges)
	if err != nil {
		return nil, nil, err
	}
	createID := cmd.OperationID
	if cmd.CreateOperationID != "" {
		createID = cmd.CreateOperationID
	}
	ref := &acc.GpuUsageRef{TenantId: cmd.ResourceTenantID, OwnerService: owner, ResourceId: cmd.ResourceID, CreateOperationId: createID}
	actor := &acc.Actor{Type: cmd.Actor.Type, Id: cmd.Actor.ID}
	if cmd.CreateOperationID != "" {
		return nil, &attachment.GpuOwnerDeleteAttachment{DeleteOperationId: cmd.OperationID, Ref: ref, Actor: actor, RequestHash: cmd.RequestHash, OriginalGpuCharges: charges, OriginalGpuPlan: c.GpuPlan, MeteringVersion: c.MeteringVersion}, nil
	}
	return &attachment.GpuOwnerCreateAttachment{Ref: ref, Actor: actor, RequestHash: cmd.RequestHash, GpuPlan: c.GpuPlan, GpuCharges: charges, BusinessPayloadDigest: c.BusinessPayloadDigest, MeteringVersion: c.MeteringVersion}, nil, nil
}
