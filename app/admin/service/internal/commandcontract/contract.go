// Package commandcontract interprets immutable admission commands. It has no
// clients, database handles, configuration, or worker dependencies.
package commandcontract

import (
	"bytes"
	"encoding/json"
	"fmt"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	quota "go-wind-admin/api/gen/go/quota/service/v1"
)

const (
	InstanceOwner         = "ani-resource-service"
	InstanceCreateAction  = "/instance.v1.InstanceService/AcceptCreate"
	InstanceDeleteAction  = "/instance.v1.InstanceService/AcceptDelete"
	InferenceOwner        = "ani-inference"
	InferenceCreateAction = runtimev1.InferenceServiceManager_CreateInferenceService_FullMethodName
	InferenceDeleteAction = runtimev1.InferenceServiceManager_DeleteInferenceService_FullMethodName
)

type QuotaItem struct {
	QuotaCode string `json:"quota_code"`
	Units     int64  `json:"units"`
}
type ChargeRef struct {
	ChargeID, QuotaCode string
	ChargedUnits        int64
}
type StoredCommand struct {
	Owner, Action, TenantID, ResourceID, OperationID, CreateOperationID, ActorType, ActorID string
	InstanceContractVersion                                                                 uint32
	Canonical                                                                               []byte
	Charges                                                                                 []ChargeRef
	RequireChargeIDs                                                                        bool
}
type Facts struct {
	CPU             bool
	BusinessPayload json.RawMessage
	GpuRequest      *acc.GpuRequest
	Execution       *acc.GpuExecution
	Display         *acc.GpuDisplay
	QuotaItems      []QuotaItem
	Inference       *InferenceCanonical
}

func Invalid(reason string) error    { return quota.ErrorInvalidQuotaRequest("%s", reason) }
func ManagedOwner(owner string) bool { return owner == InstanceOwner || owner == InferenceOwner }
func Usage(owner string) string {
	switch owner {
	case InstanceOwner:
		return "CONTAINER_INSTANCE"
	case InferenceOwner:
		return "INFERENCE"
	}
	return ""
}
func ValidateStoredCommand(in StoredCommand) (*Facts, error) {
	switch in.Owner {
	case InstanceOwner:
		if (in.Action != InstanceCreateAction && in.Action != InstanceDeleteAction) || in.InstanceContractVersion != CurrentInstanceContractVersion {
			return nil, Invalid("unsupported instance contract")
		}
		deletion := in.Action == InstanceDeleteAction
		if deletion != (in.CreateOperationID != "") {
			return nil, Invalid("invalid original create association")
		}
		if IsInstanceCPUCanonical(in.Canonical) {
			c, e := DecodeInstanceCPUCanonical(in.Canonical)
			if e != nil {
				return nil, e
			}
			if len(in.Charges) != 0 || deletion != (c.DeleteExpectedVersion > 0) {
				return nil, Invalid("invalid CPU command")
			}
			return &Facts{CPU: true, BusinessPayload: c.BusinessPayload}, nil
		}
		c, e := DecodeGpuCanonical(in.Canonical)
		if e != nil {
			return nil, e
		}
		if deletion != (c.DeleteExpectedVersion > 0) {
			return nil, Invalid("invalid instance deletion version")
		}
		if e = validateVector(c.QuotaItems, in.Charges, in.RequireChargeIDs); e != nil {
			return nil, e
		}
		business, e := res.DecodeBusinessPayload(c.BusinessPayload)
		if e != nil {
			return nil, e
		}
		items := make([]QuotaItem, 0, len(c.QuotaItems))
		for _, q := range c.QuotaItems {
			items = append(items, QuotaItem{q.QuotaCode, q.Units})
		}
		return &Facts{BusinessPayload: c.BusinessPayload, GpuRequest: c.GpuRequest, Execution: business.GpuExecution, Display: business.GpuDisplay, QuotaItems: items}, nil
	case InferenceOwner:
		return validateInferenceStored(in)
	default:
		return nil, Invalid("unsupported managed owner")
	}
}
func validateVector(items []GpuQuotaItem, charges []ChargeRef, requireIDs bool) error {
	if len(items) == 0 || len(items) != len(charges) {
		return Invalid("incomplete original charges")
	}
	seenCode, seenID := map[string]bool{}, map[string]bool{}
	want := map[string]int64{}
	for _, q := range items {
		if q.Units <= 0 || q.QuotaCode == "" || want[q.QuotaCode] != 0 {
			return Invalid("invalid quota vector")
		}
		want[q.QuotaCode] = q.Units
	}
	for _, q := range charges {
		if seenCode[q.QuotaCode] || q.ChargedUnits <= 0 || want[q.QuotaCode] != q.ChargedUnits || requireIDs && (q.ChargeID == "" || seenID[q.ChargeID]) {
			return Invalid("original charges mismatch")
		}
		seenCode[q.QuotaCode] = true
		seenID[q.ChargeID] = true
	}
	return nil
}

// DeleteSnapshot accepts only this owner's added deletion control fields. The
// original resource/business bytes remain exactly as admitted.
func DeleteSnapshot(owner, original, requested string) (string, error) {
	if owner == InferenceOwner {
		before, e := DecodeInferenceCanonical([]byte(original))
		if e != nil {
			return "", e
		}
		after, e := DecodeInferenceCanonical([]byte(requested))
		if e != nil {
			return "", e
		}
		if before.DeleteScope != nil || after.DeleteScope == nil {
			return "", Invalid("missing inference delete scope")
		}
		scope := after.DeleteScope
		generation := after.DeleteExpectedGeneration
		after.DeleteScope = nil
		after.DeleteExpectedGeneration = 0
		raw, e := CanonicalJSON(after)
		if e != nil || !bytes.Equal(raw, []byte(original)) {
			return "", Invalid("deletion changed original inference command")
		}
		if scope.CreateCommandDigest != before.CreateCommandDigest || scope.ResourceClass != before.ResourceClass || scope.ClusterId != before.ClusterID || generation < 0 {
			return "", Invalid("invalid inference delete scope")
		}
		return requested, nil
	}
	if owner != InstanceOwner {
		return "", Invalid("unsupported managed owner")
	}
	decode := func(raw string) (map[string]json.RawMessage, error) {
		var value map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &value) != nil {
			return nil, fmt.Errorf("invalid snapshot")
		}
		return value, nil
	}
	before, e := decode(original)
	if e != nil {
		return "", e
	}
	after, e := decode(requested)
	if e != nil {
		return "", e
	}
	var version int64
	if json.Unmarshal(after["delete_expected_version"], &version) != nil || version < 1 {
		return "", Invalid("instance delete version missing")
	}
	delete(after, "delete_expected_version")
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if !bytes.Equal(a, b) {
		return "", Invalid("instance deletion changed original frozen request")
	}
	return requested, nil
}
