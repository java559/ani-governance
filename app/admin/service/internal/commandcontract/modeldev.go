package commandcontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"google.golang.org/protobuf/proto"
)

const (
	ModelDevOwner        = "ani-modeldev-service"
	ModelDevCreateAction = "modeldev.execution.create"
	ModelDevUsage        = "TRAINING"
	ModelDevGPUSchema    = "ani.modeldev.gpu-ledger.v2.1"
)

// ModelDevGPUCanonical belongs exclusively to Governance's original ledger.
// Resolved and QuotaItems are the same original ACC response; quota quantities
// are never inferred from the owner snapshot or sent to ModelDev.
type ModelDevGPUCanonical struct {
	Schema            string                  `json:"schema"`
	Usage             string                  `json:"usage"`
	IntentCanonical   json.RawMessage         `json:"intent"`
	SnapshotCanonical json.RawMessage         `json:"snapshot"`
	ExecutionSpecHash string                  `json:"execution_spec_hash"`
	GpuRequest        *acc.GpuRequest         `json:"gpu_request"`
	Resolved          *acc.ResolvedGpuRequest `json:"resolved"`
	QuotaItems        []GpuQuotaItem          `json:"quota_items"`
}

func NewModelDevGPUCanonical(envelope cpup01.AdmissionEnvelope, resolved *acc.ResolvedGpuRequest) (*ModelDevGPUCanonical, error) {
	intent, snapshot, err := envelope.CanonicalPayloads()
	if err != nil || envelope.Snapshot.ComputeOrigin.DeviceMode != "gpu" || resolved == nil {
		return nil, Invalid("invalid ModelDev GPU envelope")
	}
	r, err := cpup01.GPURequestFor(envelope.Snapshot.ComputeOrigin)
	if err != nil || r == nil {
		return nil, Invalid("missing original ModelDev GPU request")
	}
	c := &ModelDevGPUCanonical{Schema: ModelDevGPUSchema, Usage: ModelDevUsage, IntentCanonical: intent, SnapshotCanonical: snapshot, ExecutionSpecHash: envelope.SpecHash,
		GpuRequest: &acc.GpuRequest{ClusterId: r.ClusterID, PoolId: r.PoolID, ProfileId: r.ProfileID, ProfileVersion: r.ProfileVersion, Replicas: r.Replicas, DevicesPerReplica: r.DevicesPerReplica, ContainerName: r.ContainerName}, Resolved: proto.Clone(resolved).(*acc.ResolvedGpuRequest)}
	for _, item := range resolved.QuotaItems {
		if item == nil {
			return nil, Invalid("missing original ModelDev meter")
		}
		c.QuotaItems = append(c.QuotaItems, GpuQuotaItem{QuotaCode: item.QuotaCode, Units: item.Units})
	}
	if _, err := c.Envelope(); err != nil {
		return nil, err
	}
	return c, nil
}

// Envelope verifies all owner facts against the exact original formal ACC
// execution/display and meter. It never reads current configuration or a clock.
func (c *ModelDevGPUCanonical) Envelope() (cpup01.AdmissionEnvelope, error) {
	bad := func() (cpup01.AdmissionEnvelope, error) {
		return cpup01.AdmissionEnvelope{}, Invalid("invalid frozen ModelDev GPU command")
	}
	if c == nil || c.Schema != ModelDevGPUSchema || c.Usage != ModelDevUsage || c.GpuRequest == nil || c.Resolved == nil || len(c.QuotaItems) != 1 {
		return bad()
	}
	intent, err := cpup01.DecodeCanonicalIntent(c.IntentCanonical)
	if err != nil {
		return bad()
	}
	snapshot, err := cpup01.DecodeCanonicalSnapshot(c.SnapshotCanonical)
	if err != nil || snapshot.ComputeOrigin.DeviceMode != "gpu" || snapshot.GPU == nil {
		return bad()
	}
	origin, err := contractpb.DecodeComputeSelection([]byte(snapshot.ComputeOrigin.SelectionJSON), snapshot.ComputeOrigin.SelectionDigest)
	if err != nil || origin != snapshot.ComputeOrigin {
		return bad()
	}
	id := snapshot.IdentityRef
	if _, _, err := ModelDevLedgerActor(id.Actor); err != nil {
		return bad()
	}
	envelope := cpup01.AdmissionEnvelope{TenantID: id.TenantID, Actor: id.Actor, OperationID: id.OperationID, ExecutionID: id.ExecutionID, Intent: intent, IntentHash: id.IntentHash, Snapshot: snapshot, SpecHash: c.ExecutionSpecHash, AcceptedAt: snapshot.Timing.AcceptedAt}
	intentBytes, snapshotBytes, err := envelope.CanonicalPayloads()
	if err != nil || !bytes.Equal(intentBytes, c.IntentCanonical) || !bytes.Equal(snapshotBytes, c.SnapshotCanonical) {
		return bad()
	}
	r, err := cpup01.GPURequestFor(snapshot.ComputeOrigin)
	if err != nil || r == nil || !proto.Equal(c.GpuRequest, &acc.GpuRequest{ClusterId: r.ClusterID, PoolId: r.PoolID, ProfileId: r.ProfileID, ProfileVersion: r.ProfileVersion, Replicas: r.Replicas, DevicesPerReplica: r.DevicesPerReplica, ContainerName: r.ContainerName}) {
		return bad()
	}
	if _, err := CanonicalMessage(c.Resolved.ProtoReflect()); err != nil {
		return bad()
	}
	if _, err := CanonicalMessage(c.GpuRequest.ProtoReflect()); err != nil {
		return bad()
	}
	meter, err := GpuResolvedQuota(c.Resolved, c.GpuRequest)
	if err != nil || c.QuotaItems[0].QuotaCode != meter.QuotaCode || c.QuotaItems[0].Units != meter.Units {
		return bad()
	}
	actualExecution, actualDisplay, err := contractpb.EncodeGPUPlanCanonical(c.Resolved.Execution, c.Resolved.Selection)
	if err != nil {
		return bad()
	}
	wantExecution, err := snapshot.GPU.Execution.Canonical()
	if err != nil {
		return bad()
	}
	wantDisplay, err := snapshot.GPU.Display.Canonical()
	if err != nil || !bytes.Equal(actualExecution, wantExecution) || !bytes.Equal(actualDisplay, wantDisplay) {
		return bad()
	}
	return envelope, nil
}

func (c *ModelDevGPUCanonical) Canonical() ([]byte, error) {
	if _, err := c.Envelope(); err != nil {
		return nil, err
	}
	return CanonicalJSON(c)
}

func DecodeModelDevGPUCanonical(raw []byte) (*ModelDevGPUCanonical, error) {
	if len(raw) == 0 || len(raw) > 1024*1024 {
		return nil, Invalid("invalid ModelDev GPU canonical size")
	}
	var c ModelDevGPUCanonical
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return nil, Invalid("invalid ModelDev GPU canonical")
	}
	encoded, err := c.Canonical()
	if err != nil || !bytes.Equal(encoded, raw) {
		return nil, Invalid("noncanonical ModelDev GPU command")
	}
	return &c, nil
}

func ModelDevGPUCanonicalHash(raw []byte) (string, error) {
	if _, err := DecodeModelDevGPUCanonical(raw); err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// The existing ModelDev create boundary accepts tenant users. This is the
// same principal's original ledger spelling, not service-identity authority.
func ModelDevLedgerActor(actor string) (string, string, error) {
	const prefix = "governance:user:"
	id, err := strconv.ParseUint(strings.TrimPrefix(actor, prefix), 10, 32)
	if err != nil || id == 0 || actor != prefix+strconv.FormatUint(id, 10) {
		return "", "", Invalid("invalid ModelDev create actor")
	}
	return "user", strconv.FormatUint(id, 10), nil
}

// ValidateModelDevStoredCommand is the finite branch used by the existing
// managed-owner validator. Stops remain ModelDev commands, never quota deletes.
func ValidateModelDevStoredCommand(in StoredCommand) (*Facts, error) {
	if in.Owner != ModelDevOwner || in.Action != ModelDevCreateAction || in.CreateOperationID != "" {
		return nil, Invalid("invalid ModelDev ledger owner/action")
	}
	c, err := DecodeModelDevGPUCanonical(in.Canonical)
	if err != nil {
		return nil, err
	}
	envelope, err := c.Envelope()
	if err != nil {
		return nil, err
	}
	typ, id, err := ModelDevLedgerActor(envelope.Actor)
	if err != nil || in.ActorType != typ || in.ActorID != id || in.TenantID != envelope.TenantID || in.ResourceID != envelope.ExecutionID || in.OperationID != envelope.OperationID {
		return nil, Invalid("ModelDev ledger identity mismatch")
	}
	if err := validateVector(c.QuotaItems, in.Charges, in.RequireChargeIDs); err != nil {
		return nil, err
	}
	items := make([]QuotaItem, 0, len(c.QuotaItems))
	for _, q := range c.QuotaItems {
		items = append(items, QuotaItem{QuotaCode: q.QuotaCode, Units: q.Units})
	}
	return &Facts{BusinessPayload: c.SnapshotCanonical, GpuRequest: c.GpuRequest, Execution: c.Resolved.Execution, Display: c.Resolved.Selection, QuotaItems: items}, nil
}
