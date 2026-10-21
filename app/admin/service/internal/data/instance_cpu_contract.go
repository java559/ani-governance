package data

import (
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
)

const (
	InstanceCPUCanonicalSchema     = commandcontract.InstanceCPUCanonicalSchema
	CurrentInstanceContractVersion = commandcontract.CurrentInstanceContractVersion
	instanceCPUOwner               = commandcontract.InstanceOwner
	instanceCPUCreateAction        = commandcontract.InstanceCreateAction
	instanceCPUDeleteAction        = commandcontract.InstanceDeleteAction
)

type InstanceCPUCanonical = commandcontract.InstanceCPUCanonical

func IsInstanceCPUCanonical(raw []byte) bool { return commandcontract.IsInstanceCPUCanonical(raw) }
func DecodeInstanceCPUCanonical(raw []byte) (*InstanceCPUCanonical, error) {
	return commandcontract.DecodeInstanceCPUCanonical(raw)
}
func MarshalInstanceCPUCanonical(c *InstanceCPUCanonical) ([]byte, error) {
	return commandcontract.MarshalInstanceCPUCanonical(c)
}
func AllowsInstanceCPUCommand(owner, action, canonical string) bool {
	return commandcontract.AllowsInstanceCPUCommand(owner, action, canonical)
}
func validateOperationCharges(op *ent.QuotaOperation, charges []*ent.QuotaCharge) error {
	if op == nil {
		return commandcontract.Invalid("missing original command")
	}
	if !commandcontract.ManagedOwner(op.OwnerService) {
		return validateFrozenCharges(op.CanonicalRequest, charges)
	}
	refs := make([]commandcontract.ChargeRef, 0, len(charges))
	for _, q := range charges {
		if q == nil {
			return commandcontract.Invalid("missing charge")
		}
		refs = append(refs, commandcontract.ChargeRef{ChargeID: q.ChargeID, QuotaCode: q.QuotaCode, ChargedUnits: q.OriginalUnits})
	}
	createID := ""
	if op.CreateOperationID != nil {
		createID = *op.CreateOperationID
	}
	_, err := commandcontract.ValidateStoredCommand(commandcontract.StoredCommand{Owner: op.OwnerService, Action: op.Action, TenantID: op.ResourceTenantID, ResourceID: op.ResourceID, OperationID: op.OperationID, ActorType: op.ActorType, ActorID: op.ActorID, CreateOperationID: createID, InstanceContractVersion: op.InstanceContractVersion, Canonical: []byte(op.CanonicalRequest), Charges: refs, RequireChargeIDs: true})
	return err
}
