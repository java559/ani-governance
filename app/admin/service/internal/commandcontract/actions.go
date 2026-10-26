package commandcontract

// ActionDefinition is the compiled contract selected before decoding a stored
// command. A shared owner never implies a particular resource kind.
type ActionDefinition struct {
	Owner, Action, ResourceKind, Phase, Usage, Codec string
}

const (
	VMCreateAction        = "/vm.v1.VMService/AcceptCreate"
	VMDeleteAction        = "/vm.v1.VMService/AcceptDelete"
	ResourceKindInstance  = "container"
	ResourceKindVM        = "virtual_machine"
	ResourceKindInference = "inference"
	ResourceKindModelDev  = "modeldev"
	PhaseCreate           = "create"
	PhaseDelete           = "delete"
)

var managedActions = [...]ActionDefinition{
	{InstanceOwner, InstanceCreateAction, ResourceKindInstance, PhaseCreate, "CONTAINER_INSTANCE", "instance-v2"},
	{InstanceOwner, InstanceDeleteAction, ResourceKindInstance, PhaseDelete, "CONTAINER_INSTANCE", "instance-v2"},
	{InstanceOwner, VMCreateAction, ResourceKindVM, PhaseCreate, "VIRTUAL_MACHINE", "vm-v1"},
	{InstanceOwner, VMDeleteAction, ResourceKindVM, PhaseDelete, "VIRTUAL_MACHINE", "vm-v1"},
	{InferenceOwner, InferenceCreateAction, ResourceKindInference, PhaseCreate, "INFERENCE", InferenceSchema},
	{InferenceOwner, InferenceDeleteAction, ResourceKindInference, PhaseDelete, "INFERENCE", InferenceSchema},
	{ModelDevOwner, ModelDevCreateAction, ResourceKindModelDev, PhaseCreate, ModelDevUsage, ModelDevGPUSchema},
}

func LookupAction(owner, action string) (ActionDefinition, bool) {
	for _, definition := range managedActions {
		if definition.Owner == owner && definition.Action == action {
			return definition, true
		}
	}
	return ActionDefinition{}, false
}

func Usage(owner, action string) string {
	definition, ok := LookupAction(owner, action)
	if !ok {
		return ""
	}
	return definition.Usage
}

func InstanceVersion(owner, action string) uint32 {
	definition, ok := LookupAction(owner, action)
	if ok && definition.ResourceKind == ResourceKindInstance {
		return CurrentInstanceContractVersion
	}
	return 0
}

func PairedActions(owner, create, deletion string) bool {
	a, aok := LookupAction(owner, create)
	b, bok := LookupAction(owner, deletion)
	return aok && bok && a.Phase == PhaseCreate && b.Phase == PhaseDelete && a.ResourceKind == b.ResourceKind && a.Codec == b.Codec
}
