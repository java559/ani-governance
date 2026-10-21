package commandcontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	quota "go-wind-admin/api/gen/go/quota/service/v1"
)

const InstanceCPUCanonicalSchema = "instance-cpu-v1"
const CurrentInstanceContractVersion uint32 = 2
const instanceCPUOwner = "ani-resource-service"
const instanceCPUCreateAction = "/instance.v1.InstanceService/AcceptCreate"
const instanceCPUDeleteAction = "/instance.v1.InstanceService/AcceptDelete"

// InstanceCPUCanonical freezes an unmetered CPU command. It is deliberately
// separate from GPU plans and is accepted only for the compiled instance owner.
type InstanceCPUCanonical struct {
	Schema                  string          `json:"schema"`
	InstanceContractVersion uint32          `json:"instance_contract_version"`
	BusinessPayload         json.RawMessage `json:"business_payload"`
	BusinessPayloadDigest   string          `json:"business_payload_digest"`
	DeleteExpectedVersion   int64           `json:"delete_expected_version,omitempty"`
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
	if d.Decode(&c) != nil || c.Schema != InstanceCPUCanonicalSchema || c.InstanceContractVersion != CurrentInstanceContractVersion || c.DeleteExpectedVersion < 0 {
		return nil, invalid
	}
	// A byte-for-byte canonical encoding rejects duplicate keys and trailing data.
	encoded, e := MarshalInstanceCPUCanonical(&c)
	if e != nil || !bytes.Equal(encoded, raw) {
		return nil, invalid
	}
	business, e := res.DecodeBusinessPayload(c.BusinessPayload)
	if e != nil || business == nil || business.GpuSelection != nil || business.GpuExecution != nil || business.InstanceKind != "container" || business.ComputeType != "cpu" {
		return nil, invalid
	}
	if business.InstanceContractVersion != CurrentInstanceContractVersion {
		return nil, invalid
	}
	canonical, e := res.CanonicalBusinessPayloadVersion(business, CurrentInstanceContractVersion)
	if e != nil {
		return nil, invalid
	}
	if !bytes.Equal(canonical, c.BusinessPayload) {
		return nil, invalid
	}
	s := business
	if s.ClusterId == "" || s.Namespace == "" || s.ImageReference == "" || s.Cpu == "" || s.Memory == "" || s.ServiceAccount == "" || s.StorageClass == "" || s.WorkspaceSize == "" || s.Shell == "" {
		return nil, invalid
	}
	digest := sha256.Sum256(c.BusinessPayload)
	if hex.EncodeToString(digest[:]) != c.BusinessPayloadDigest {
		return nil, invalid
	}
	return &c, nil
}

// MarshalInstanceCPUCanonical preserves the exact signed payload bytes.
func MarshalInstanceCPUCanonical(command *InstanceCPUCanonical) ([]byte, error) {
	if command == nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "missing frozen CPU instance command")
	}
	if command.InstanceContractVersion != CurrentInstanceContractVersion {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance contract")
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(command); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'}), nil
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
