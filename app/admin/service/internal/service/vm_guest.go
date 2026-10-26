package service

import (
	"bytes"
	"context"
	"encoding/json"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
)

type vmGuestCredentialContextKey struct{}
type vmGuestCredentialPayload struct {
	Password string `json:"password"`
}

func vmGuestCredentialBinding(tenant, actorType, actor, operation, resource, specDigest string) ([]byte, error) {
	return commandcontract.CanonicalJSON(map[string]any{"purpose": "vm-guest-credentials-v1", "tenant_id": tenant, "actor_type": actorType, "actor_id": actor, "operation_id": operation, "resource_id": resource, "create_spec_digest": specDigest})
}

func (b *VMBinding) guestCredentials(ctx context.Context, spec *vm.CreateSpec) ([]byte, error) {
	raw, _ := ctx.Value(vmGuestCredentialContextKey{}).([]byte)
	initialization := spec.Intent.GuestInitialization
	if initialization == nil || !initialization.PasswordSet {
		if len(raw) != 0 {
			return nil, commandcontract.Invalid("unexpected VM guest credentials")
		}
		return nil, vm.ValidateGuestCredentials(spec, nil)
	}
	if b.guestCipher == nil || len(raw) == 0 {
		return nil, commandcontract.Invalid("VM guest credential encryption unavailable")
	}
	digest, err := b.guestCipher.IntentDigest(raw)
	if err != nil || digest != initialization.CredentialIntentDigest {
		return nil, commandcontract.Invalid("VM guest credential intent mismatch")
	}
	credentials, err := decodeVMGuestCredentials(raw)
	if err != nil {
		return nil, err
	}
	if err = vm.ValidateGuestCredentials(spec, credentials); err != nil {
		return nil, err
	}
	return bytes.Clone(raw), nil
}

func decodeVMGuestCredentials(raw []byte) (*vm.GuestCredentials, error) {
	var payload vmGuestCredentialPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || payload.Password == "" {
		return nil, commandcontract.Invalid("VM guest credential envelope invalid")
	}
	canonical, err := json.Marshal(payload)
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil, commandcontract.Invalid("VM guest credential envelope invalid")
	}
	return &vm.GuestCredentials{Password: payload.Password}, nil
}
