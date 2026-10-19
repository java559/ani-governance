package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"math"
	"strconv"

	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	protocol "github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
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
			values := map[string]any{}
			var mapErr error
			m.Get(f).Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
				values[k.String()], mapErr = gpuCanonicalScalar(f.MapValue(), v)
				return mapErr == nil
			})
			if mapErr != nil {
				return nil, mapErr
			}
			out[string(f.Name())] = values
			continue
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
	if p == nil || p.Ref == nil {
		return "", fmt.Errorf("invalid projection")
	}
	ref, err := gpuCanonicalMessage(p.Ref.ProtoReflect())
	if err != nil {
		return "", err
	}
	return gpuHash(map[string]any{"ref": ref, "revision": strconv.FormatUint(p.Revision, 10), "state": strconv.FormatInt(int64(p.State), 10), "profile_id": p.ProfileId, "profile_version": strconv.FormatUint(p.ProfileVersion, 10), "group_id": p.GroupId, "baseline_id": p.BaselineId, "cluster_id": p.ClusterId, "container_name": p.ContainerName, "execution_digest": p.ExecutionDigest, "end_reason": strconv.FormatInt(int64(p.EndReason), 10), "source_fact_ref": p.SourceFactRef, "source_operation_created_at": p.SourceOperationCreatedAt})
}

// GpuResolvedQuota validates the final execution and the one original GPU meter.
// Runtime values are MiB with F=1; this check never reloads mutable catalog state.
func GpuResolvedQuota(p *acc.ResolvedGpuRequest, request *acc.GpuRequest) (data.QuotaOccupyItem, error) {
	bad := func() (data.QuotaOccupyItem, error) {
		return data.QuotaOccupyItem{}, quotapb.ErrorInvalidQuotaRequest("%s", "invalid resolved GPU execution")
	}
	if p == nil || p.Selection == nil || len(p.QuotaItems) != 1 || p.QuotaItems[0] == nil || len(p.ProtoReflect().GetUnknown()) != 0 || len(p.Selection.ProtoReflect().GetUnknown()) != 0 || len(p.QuotaItems[0].ProtoReflect().GetUnknown()) != 0 || protocol.ValidateGpuExecution(p.Execution) != nil {
		return bad()
	}
	q, g := p.QuotaItems[0], p.Execution
	number, err := strconv.ParseInt(g.Resources["volcano.sh/vgpu-number"], 10, 32)
	if err != nil {
		return bad()
	}
	memory, err := strconv.ParseInt(g.Resources["volcano.sh/vgpu-memory"], 10, 32)
	if err != nil {
		return bad()
	}
	replicas := int64(1)
	if request != nil {
		if request.Replicas < 1 || request.Replicas > 16 || request.DevicesPerReplica < 1 || number != int64(request.DevicesPerReplica) || request.ProfileId != p.Selection.ProfileId || request.ProfileVersion != p.Selection.ProfileVersion || request.ContainerName == "" {
			return bad()
		}
		replicas = int64(request.Replicas)
	}
	if p.Selection.ProfileId == "" || p.Selection.ProfileVersion == 0 || p.Selection.ModelKey != g.NodeSelector["accelerator.ani.io/model-key"] || q.Units <= 0 {
		return bad()
	}
	switch p.Selection.Mode {
	case acc.SupplyMode_SHARED_FIXED:
		if number != 1 || g.Resources["volcano.sh/vgpu-cores"] == "100" || memory > math.MaxInt64/replicas || q.QuotaCode != GpuSharedQuotaCode || q.Units != memory*replicas {
			return bad()
		}
	case acc.SupplyMode_WHOLE_EXCLUSIVE:
		if g.Resources["volcano.sh/vgpu-cores"] != "100" || number > math.MaxInt64/replicas || q.QuotaCode != GpuPhysicalQuotaCode || q.Units != number*replicas {
			return bad()
		}
	default:
		return bad()
	}
	return data.QuotaOccupyItem{QuotaCode: q.QuotaCode, Units: q.Units}, nil
}

type GpuQuotaItem struct {
	QuotaCode string `json:"quota_code"`
	Units     int64  `json:"units"`
}

// Only Governance stores the original quota vector. The owner receives the
// business payload containing its single accepted execution and display snapshot.
type GpuCanonical struct {
	SchemaVersion         uint32          `json:"schema_version"`
	GpuRequest            *acc.GpuRequest `json:"gpu_request"`
	BusinessPayload       json.RawMessage `json:"business_payload"`
	BusinessPayloadDigest string          `json:"business_payload_digest"`
	MeteringVersion       string          `json:"metering_version"`
	QuotaItems            []GpuQuotaItem  `json:"quota_items"`
	DeleteExpectedVersion int64           `json:"delete_expected_version,omitempty"`
}

func isGpuCanonical(raw []byte) bool {
	var marker struct {
		SchemaVersion uint32 `json:"schema_version"`
	}
	return json.Unmarshal(raw, &marker) == nil && marker.SchemaVersion == 3
}
func DecodeGpuCanonical(raw []byte) (*GpuCanonical, error) {
	var c GpuCanonical
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || c.SchemaVersion != 3 || c.DeleteExpectedVersion < 0 || c.MeteringVersion != GpuMeteringVersion || c.GpuRequest == nil || len(c.QuotaItems) == 0 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid GPU canonical")
	}
	encoded, err := gpuCanonicalJSON(c)
	if err != nil || !bytes.Equal(encoded, raw) {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "noncanonical GPU command")
	}
	var gpuItems []GpuQuotaItem
	seen := map[string]bool{}
	for _, q := range c.QuotaItems {
		if q.QuotaCode == "" || q.Units <= 0 || seen[q.QuotaCode] {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid frozen quota vector")
		}
		seen[q.QuotaCode] = true
		if q.QuotaCode == GpuSharedQuotaCode || q.QuotaCode == GpuPhysicalQuotaCode {
			gpuItems = append(gpuItems, q)
		}
	}
	if len(gpuItems) != 1 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid GPU metering")
	}
	q := gpuItems[0]
	business, err := res.DecodeBusinessPayload(c.BusinessPayload)
	if err != nil {
		return nil, err
	}
	_, err = GpuResolvedQuota(&acc.ResolvedGpuRequest{Execution: business.GpuExecution, Selection: business.GpuDisplay, QuotaItems: []*acc.GpuQuotaItem{{QuotaCode: q.QuotaCode, Units: q.Units}}}, c.GpuRequest)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(c.BusinessPayload)
	if hex.EncodeToString(sum[:]) != c.BusinessPayloadDigest {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "business payload digest mismatch")
	}
	return &c, nil
}
func ValidateGpuCharges(c *GpuCanonical, charges []QuotaChargeRef) error {
	if c == nil || len(charges) != len(c.QuotaItems) {
		return quotapb.ErrorInvalidQuotaRequest("%s", "incomplete original charges")
	}
	want := map[string]int64{}
	for _, q := range c.QuotaItems {
		want[q.QuotaCode] = q.Units
	}
	seenCode, seenID := map[string]bool{}, map[string]bool{}
	for _, q := range charges {
		if q.ChargeID == "" || seenID[q.ChargeID] || seenCode[q.QuotaCode] || q.ChargedUnits <= 0 || want[q.QuotaCode] != q.ChargedUnits {
			return quotapb.ErrorInvalidQuotaRequest("%s", "original charges mismatch")
		}
		seenID[q.ChargeID] = true
		seenCode[q.QuotaCode] = true
	}
	return nil
}
