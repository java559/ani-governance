package service

import (
	"context"
	"fmt"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"testing"

	"github.com/stretchr/testify/require"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestInstanceConfigurationHasOneCanonicalContract(t *testing.T) {
	require.Nil(t, (&res.CreateSpec{}).ProtoReflect().Descriptor().Fields().ByName("instance_contract_version"))
	require.Nil(t, (&res.ResolveCreateSpecRequest{}).ProtoReflect().Descriptor().Fields().ByName("instance_contract_version"))
	binding := NewInstanceGpuBinding(nil, nil, "cluster")
	acceptance := &GpuAcceptance{binding: binding}
	request := &res.CreateInstanceData{Name: "workspace", ImageId: "image", ImageScope: "platform", PresetId: "preset"}
	_, err := acceptance.createRequestHash("tenant", "user", "7", nil, request)
	require.NoError(t, err, "omitted optional configuration is valid current intent")
	raw, err := binding.CanonicalGpuBusiness(request)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "instance_contract_version")
}

func TestInstanceConfigurationCurrentIntentCoversValuesAndOrder(t *testing.T) {
	t.Setenv("ANI_INSTANCE_NEW_INTENTS_ENABLED", "true")
	binding := NewInstanceGpuBinding(nil, nil, "cluster")
	acceptance := &GpuAcceptance{binding: binding}
	request := &res.CreateInstanceData{Name: "workspace", ImageId: "image", ImageScope: "platform", PresetId: "preset", WorkspaceMountPath: proto.String("/work"), Env: []*res.InstanceEnvVar{{Name: "FIRST", Value: "  literal $(NEXT)  "}, {Name: "NEXT", Value: "second"}}}
	hash, err := acceptance.createRequestHash("tenant", "user", "7", nil, request)
	require.NoError(t, err)
	changed := proto.Clone(request).(*res.CreateInstanceData)
	changed.Env[0].Value = "literal $(NEXT)"
	other, err := acceptance.createRequestHash("tenant", "user", "7", nil, changed)
	require.NoError(t, err)
	require.NotEqual(t, hash, other)
	changed = proto.Clone(request).(*res.CreateInstanceData)
	changed.Env[0], changed.Env[1] = changed.Env[1], changed.Env[0]
	other, err = acceptance.createRequestHash("tenant", "user", "7", nil, changed)
	require.NoError(t, err)
	require.NotEqual(t, hash, other)
	// Closing new admission never changes current frozen intent.
	t.Setenv("ANI_INSTANCE_NEW_INTENTS_ENABLED", "false")
	acceptance.binding = NewInstanceGpuBinding(nil, nil, "cluster")
	replay, err := acceptance.createRequestHash("tenant", "user", "7", nil, request)
	require.NoError(t, err)
	require.Equal(t, hash, replay)
}

func TestInstanceCreateAdmissionUsesOnlyMaintenanceGate(t *testing.T) {
	for _, retiredValue := range []string{"", "false", "true"} {
		for _, maintenanceEnabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("retired_flag_%q/maintenance_%t", retiredValue, maintenanceEnabled), func(t *testing.T) {
				t.Setenv("ANI_INSTANCE_CONFIG_V2_WRITES", retiredValue)
				t.Setenv("ANI_INSTANCE_NEW_INTENTS_ENABLED", fmt.Sprint(maintenanceEnabled))
				binding := NewInstanceGpuBinding(nil, nil, "cluster")
				require.Equal(t, maintenanceEnabled, binding.NewCreateEnabled())
			})
		}
	}
}

func TestInstanceConfigurationEmptyOptionalCollectionsHaveOneIntent(t *testing.T) {
	acceptance := &GpuAcceptance{binding: NewInstanceGpuBinding(nil, nil, "cluster")}
	request := &res.CreateInstanceData{Name: "workspace", ImageId: "image", ImageScope: "platform", PresetId: "preset"}
	omitted, err := acceptance.createRequestHash("tenant", "user", "7", nil, request)
	require.NoError(t, err)
	request.Env, request.VolumeMounts = []*res.InstanceEnvVar{}, []*res.VolumeMount{}
	request.Labels, request.Annotations = map[string]string{}, map[string]string{}
	empty, err := acceptance.createRequestHash("tenant", "user", "7", nil, request)
	require.NoError(t, err)
	require.Equal(t, omitted, empty)
}

func TestInstanceConfigurationReservedKeyErrorCodeAndReason(t *testing.T) {
	for _, rejection := range []error{res.ErrReservedConfig, fmt.Errorf("configuration rejected: %w", res.ErrReservedConfig)} {
		actual := kratoserrors.FromError(instanceBusinessError(rejection))
		require.EqualValues(t, 400, actual.Code)
		require.Equal(t, "INSTANCE_RESERVED_CONFIG", actual.Reason)
	}
}

type instanceConfigurationReadPeer struct {
	InstanceOwnerClient
	instanceConfigurationClient
	t            *testing.T
	mismatchedID bool
}

func (p *instanceConfigurationReadPeer) Configuration(_ context.Context, tenant, actor, id string) (*res.InstanceConfiguration, error) {
	p.t.Helper()
	require.Equal(p.t, presetTenant, tenant)
	require.Equal(p.t, "governance:user:9", actor)
	if p.mismatchedID {
		id = "10000000-0000-4000-8000-000000000002"
	}
	return &res.InstanceConfiguration{InstanceId: id, WorkspaceMountPath: "/workspace"}, nil
}

func TestInstanceConfigurationQueryUsesOneContractAndChecksIdentity(t *testing.T) {
	const id = "10000000-0000-4000-8000-000000000001"
	for _, mismatched := range []bool{false, true} {
		owner := &instanceConfigurationReadPeer{t: t, mismatchedID: mismatched}
		authorization := &instanceNetworkViewAuthorization{t: t}
		service := NewInstanceService(nil, NewInstanceGpuBinding(owner, authorization, "cluster"), instancePresetTenantResolver{}, nil, nil)
		actual, err := service.GetInstanceConfiguration(instancePresetContext(7), &view.GetInstanceRequest{InstanceId: id})
		if !mismatched {
			require.NoError(t, err)
			require.Nil(t, actual.ProtoReflect().Descriptor().Fields().ByName("instance_contract_version"))
			require.Equal(t, "/workspace", actual.WorkspaceMountPath)
			raw, marshalErr := protojson.Marshal(actual)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(raw), "instance_contract_version")
		} else {
			require.Error(t, err)
			require.Nil(t, actual)
		}
	}
}
