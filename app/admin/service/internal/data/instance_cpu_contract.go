package data

import (
	"bytes"
	"encoding/json"

	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	quota "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
)

const InstanceCPUCanonicalSchema = "instance-cpu-v1"
const instanceCPUOwner = "ani-resource-service"
const instanceCPUCreateAction = "/instance.v1.InstanceService/AcceptCreate"
const instanceCPUDeleteAction = "/instance.v1.InstanceService/AcceptDelete"

// InstanceCPUCanonical freezes an unmetered CPU command. It is deliberately
// separate from GPU plans and is accepted only for the compiled instance owner.
type InstanceCPUCanonical struct {
	Schema                string          `json:"schema"`
	BusinessPayload       json.RawMessage `json:"business_payload"`
	BusinessPayloadDigest string          `json:"business_payload_digest"`
	DeleteExpectedVersion int64           `json:"delete_expected_version,omitempty"`
}

func IsInstanceCPUCanonical(raw []byte) bool {
	var marker struct {
		Schema string `json:"schema"`
	}
	return json.Unmarshal(raw, &marker) == nil && marker.Schema == InstanceCPUCanonicalSchema
}

func DecodeInstanceCPUCanonical(raw []byte) (*InstanceCPUCanonical, error) {
	invalid := quota.ErrorInvalidQuotaRequest("%s", "invalid frozen CPU instance command")
	var c InstanceCPUCanonical
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || c.Schema != InstanceCPUCanonicalSchema || c.DeleteExpectedVersion < 0 {
		return nil, invalid
	}
	// A byte-for-byte canonical encoding rejects duplicate keys and trailing data.
	encoded, e := json.Marshal(c)
	if e != nil || !bytes.Equal(encoded, raw) {
		return nil, invalid
	}
	business, e := res.DecodeBusinessPayload(c.BusinessPayload)
	if e != nil || business == nil || business.GpuSelection != nil || business.GpuExecution != nil || business.InstanceKind != "container" || business.ComputeType != "cpu" {
		return nil, invalid
	}
	s := business
	if s.ClusterId == "" || s.Namespace == "" || s.ImageReference == "" || s.Cpu == "" || s.Memory == "" || s.ServiceAccount == "" || s.StorageClass == "" || s.WorkspaceSize == "" || s.Shell == "" {
		return nil, invalid
	}
	digest, e := res.BusinessPayloadDigest(business)
	if e != nil || digest != c.BusinessPayloadDigest {
		return nil, invalid
	}
	return &c, nil
}

// AllowsInstanceCPUCommand is the only exception to nonempty quota vectors.
// Public input cannot choose this path: resolution freezes the trusted preset.
func AllowsInstanceCPUCommand(owner, action, canonical string) bool {
	if owner != instanceCPUOwner {
		return false
	}
	c, e := DecodeInstanceCPUCanonical([]byte(canonical))
	if e != nil {
		return false
	}
	return (action == instanceCPUCreateAction && c.DeleteExpectedVersion == 0) ||
		(action == instanceCPUDeleteAction && c.DeleteExpectedVersion > 0)
}

func validateOperationCharges(op *ent.QuotaOperation, charges []*ent.QuotaCharge) error {
	if op == nil {
		return quota.ErrorInvalidQuotaRequest("%s", "missing original command")
	}
	if IsInstanceCPUCanonical([]byte(op.CanonicalRequest)) {
		create := op.Action == instanceCPUCreateAction && op.CreateOperationID == nil
		deletion := op.Action == instanceCPUDeleteAction && op.CreateOperationID != nil && *op.CreateOperationID != ""
		if len(charges) != 0 || (!create && !deletion) || !AllowsInstanceCPUCommand(op.OwnerService, op.Action, op.CanonicalRequest) {
			return quota.ErrorInvalidQuotaRequest("%s", "invalid CPU instance charge vector")
		}
		return nil
	}
	if len(charges) == 0 {
		return quota.ErrorInvalidQuotaRequest("%s", "missing original charges")
	}
	return validateFrozenCharges(op.CanonicalRequest, charges)
}
