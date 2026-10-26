package service

import "go-wind-admin/app/admin/service/internal/commandcontract"

func validateDispatchCommand(owner string, cmd *QuotaDispatchCommand) (*commandcontract.Facts, error) {
	if cmd == nil {
		return nil, commandcontract.Invalid("missing dispatch command")
	}
	refs := make([]commandcontract.ChargeRef, 0, len(cmd.Charges))
	for _, q := range cmd.Charges {
		refs = append(refs, commandcontract.ChargeRef{ChargeID: q.ChargeID, QuotaCode: q.QuotaCode, ChargedUnits: q.ChargedUnits})
	}
	version := commandcontract.InstanceVersion(owner, cmd.Action)
	return commandcontract.ValidateStoredCommand(commandcontract.StoredCommand{Owner: owner, Action: cmd.Action, TenantID: cmd.ResourceTenantID, ResourceID: cmd.ResourceID, OperationID: cmd.OperationID, CreateOperationID: cmd.CreateOperationID, ActorType: cmd.Actor.Type, ActorID: cmd.Actor.ID, InstanceContractVersion: version, Canonical: cmd.CanonicalRequest, Charges: refs, RequireChargeIDs: true})
}
