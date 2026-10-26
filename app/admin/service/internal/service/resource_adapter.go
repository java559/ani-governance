package service

import (
	"context"
	"go-wind-admin/app/admin/service/internal/commandcontract"
)

// ResourceDispatchAdapter is the single registry entry for the Resource owner.
// Domain bindings keep their codecs and clients, selected by the fixed table.
type ResourceDispatchAdapter struct {
	instance *InstanceGpuBinding
	vm       QuotaDispatchAdapter
}

func NewResourceDispatchAdapter(instance *InstanceGpuBinding, vm QuotaDispatchAdapter) *ResourceDispatchAdapter {
	return &ResourceDispatchAdapter{instance: instance, vm: vm}
}
func (*ResourceDispatchAdapter) OwnerService() string { return InstanceOwner }
func (a *ResourceDispatchAdapter) Actions() []string {
	actions := []string{}
	if a != nil && a.instance != nil {
		actions = append(actions, InstanceCreateAction, InstanceDeleteAction)
	}
	if a != nil && a.vm != nil {
		actions = append(actions, commandcontract.VMCreateAction, commandcontract.VMDeleteAction)
	}
	return actions
}
func (a *ResourceDispatchAdapter) gpuBindings() []GpuBusinessBinding {
	bindings := []GpuBusinessBinding{}
	if a != nil && a.instance != nil {
		bindings = append(bindings, a.instance)
	}
	if a != nil {
		if binding, ok := a.vm.(GpuBusinessBinding); ok {
			bindings = append(bindings, binding)
		}
	}
	return bindings
}
func (a *ResourceDispatchAdapter) Dispatch(ctx context.Context, command *QuotaDispatchCommand) ([]byte, error) {
	if a == nil || command == nil {
		return nil, commandcontract.Invalid("missing Resource command")
	}
	definition, ok := commandcontract.LookupAction(InstanceOwner, command.Action)
	if !ok {
		return nil, commandcontract.Invalid("unsupported Resource action")
	}
	if _, err := validateDispatchCommand(InstanceOwner, command); err != nil {
		return nil, err
	}
	switch definition.ResourceKind {
	case commandcontract.ResourceKindInstance:
		if a.instance != nil {
			return a.instance.Dispatch(ctx, command)
		}
	case commandcontract.ResourceKindVM:
		if a.vm != nil && a.vm.OwnerService() == InstanceOwner {
			return a.vm.Dispatch(ctx, command)
		}
	}
	return nil, commandcontract.Invalid("Resource domain adapter unavailable")
}

func adapterGpuBindings(adapter QuotaDispatchAdapter) []GpuBusinessBinding {
	if aggregate, ok := adapter.(interface{ gpuBindings() []GpuBusinessBinding }); ok {
		return aggregate.gpuBindings()
	}
	if binding, ok := adapter.(GpuBusinessBinding); ok {
		return []GpuBusinessBinding{binding}
	}
	return nil
}
