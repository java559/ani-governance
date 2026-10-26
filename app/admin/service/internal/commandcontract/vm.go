package commandcontract

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	vmv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	appcrypto "go-wind-admin/pkg/crypto"
	"strings"
)

const CurrentVMContractVersion uint32 = 1

// VMCanonical deliberately has no schema, usage, or legacy Instance version.
// The action selects this codec and vm_contract_version selects its contract.
type VMCanonical struct {
	VMContractVersion          uint32          `json:"vm_contract_version"`
	CreateSpec                 json.RawMessage `json:"create_spec"`
	DeleteExpectedVersion      int64           `json:"delete_expected_version,omitempty"`
	GuestCredentialsCiphertext string          `json:"guest_credentials_ciphertext,omitempty"`
}

func IsVMCanonical(raw []byte) bool {
	var marker map[string]json.RawMessage
	if json.Unmarshal(raw, &marker) != nil {
		return false
	}
	_, ok := marker["vm_contract_version"]
	return ok
}

func MarshalVMCanonical(command *VMCanonical) ([]byte, error) {
	if command == nil || command.VMContractVersion != CurrentVMContractVersion || command.DeleteExpectedVersion < 0 || len(command.CreateSpec) == 0 || bytes.Equal(command.CreateSpec, []byte("null")) {
		return nil, Invalid("invalid frozen VM command")
	}
	return CanonicalJSON(command)
}

func DecodeVMCanonical(raw []byte) (*VMCanonical, error) {
	var command VMCanonical
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil {
		return nil, Invalid("invalid frozen VM command")
	}
	encoded, err := MarshalVMCanonical(&command)
	if err != nil || !bytes.Equal(encoded, raw) {
		return nil, Invalid("noncanonical VM command")
	}
	var spec map[string]json.RawMessage
	if json.Unmarshal(command.CreateSpec, &spec) != nil || len(spec) == 0 {
		return nil, Invalid("missing frozen VM create spec")
	}
	if _, exists := spec["vm_contract_version"]; exists {
		return nil, Invalid("VM create spec contains a second contract version")
	}
	frozen, err := vmv1.DecodeCreateSpec(command.CreateSpec)
	if err != nil {
		return nil, Invalid("invalid frozen VM create spec")
	}
	canonical, err := vmv1.CanonicalCreateSpec(frozen)
	if err != nil || !bytes.Equal(canonical, command.CreateSpec) {
		return nil, Invalid("noncanonical frozen VM create spec")
	}
	initialization := frozen.Intent.GuestInitialization
	passwordSet := initialization != nil && initialization.PasswordSet
	if passwordSet != (command.GuestCredentialsCiphertext != "") {
		return nil, Invalid("VM credential envelope association invalid")
	}
	if passwordSet {
		if !strings.HasPrefix(command.GuestCredentialsCiphertext, appcrypto.VMGuestCipherPrefix) || len(command.GuestCredentialsCiphertext) > 16384 {
			return nil, Invalid("VM credential envelope invalid")
		}
		envelope, e := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(command.GuestCredentialsCiphertext, appcrypto.VMGuestCipherPrefix))
		if e != nil || len(envelope) < 28 {
			return nil, Invalid("VM credential envelope invalid")
		}
	}
	return &command, nil
}

func validateVMStored(in StoredCommand) (*Facts, error) {
	command, err := DecodeVMCanonical(in.Canonical)
	if err != nil {
		return nil, err
	}
	deletion := in.Action == VMDeleteAction
	if len(in.Charges) != 0 || deletion != (in.CreateOperationID != "") || deletion != (command.DeleteExpectedVersion > 0) {
		return nil, Invalid("invalid VM command association or charges")
	}
	// InstanceContractVersion is a historical storage placeholder for VM rows.
	// It is not consulted to authorize this independent VM contract.
	return &Facts{BusinessPayload: command.CreateSpec, VM: command}, nil
}

func VMDeleteSnapshot(original, requested string) (string, error) {
	before, err := DecodeVMCanonical([]byte(original))
	if err != nil {
		return "", err
	}
	after, err := DecodeVMCanonical([]byte(requested))
	if err != nil {
		return "", err
	}
	if before.DeleteExpectedVersion != 0 || after.DeleteExpectedVersion < 1 {
		return "", Invalid("VM delete version missing")
	}
	after.DeleteExpectedVersion = 0
	raw, err := MarshalVMCanonical(after)
	if err != nil || !bytes.Equal(raw, []byte(original)) {
		return "", Invalid("VM deletion changed original frozen request")
	}
	return requested, nil
}
