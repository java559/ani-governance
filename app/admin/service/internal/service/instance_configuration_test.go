package service

import (
	"context"
	"fmt"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"testing"

	"github.com/stretchr/testify/require"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"google.golang.org/protobuf/proto"
)

func TestInstanceConfigurationCurrentContractRejectsRetiredVersions(t *testing.T) {
	binding := NewInstanceGpuBinding(nil, nil, "cluster")
	acceptance := &GpuAcceptance{binding: binding}
	request := &res.CreateInstanceData{Name: "workspace", ImageId: "image", ImageScope: "platform", PresetId: "preset"}
	for _, version := range []uint32{0, 1, 3} {
		_, err := acceptance.createRequestHash("tenant", "user", "7", nil, request, version)
		require.Error(t, err)
		_, err = binding.CanonicalGpuBusinessVersion(request, version)
		require.Error(t, err)
	}
	_, err := acceptance.createRequestHash("tenant", "user", "7", nil, request, 2)
	require.NoError(t, err, "omitted optional configuration is valid current intent")
}

func TestInstanceConfigurationCurrentIntentCoversValuesAndOrder(t *testing.T) {
	t.Setenv("ANI_INSTANCE_CONFIG_V2_WRITES", "true")
	binding := NewInstanceGpuBinding(nil, nil, "cluster")
	acceptance := &GpuAcceptance{binding: binding}
	request := &res.CreateInstanceData{Name: "workspace", ImageId: "image", ImageScope: "platform", PresetId: "preset", WorkspaceMountPath: proto.String("/work"), Env: []*res.InstanceEnvVar{{Name: "FIRST", Value: "  literal $(NEXT)  "}, {Name: "NEXT", Value: "second"}}}
	hash, err := acceptance.createRequestHash("tenant", "user", "7", nil, request, 2)
	require.NoError(t, err)
	changed := proto.Clone(request).(*res.CreateInstanceData)
	changed.Env[0].Value = "literal $(NEXT)"
	other, err := acceptance.createRequestHash("tenant", "user", "7", nil, changed, 2)
	require.NoError(t, err)
	require.NotEqual(t, hash, other)
	changed = proto.Clone(request).(*res.CreateInstanceData)
	changed.Env[0], changed.Env[1] = changed.Env[1], changed.Env[0]
	other, err = acceptance.createRequestHash("tenant", "user", "7", nil, changed, 2)
	require.NoError(t, err)
	require.NotEqual(t, hash, other)
	// Closing new admission never changes current frozen intent.
	t.Setenv("ANI_INSTANCE_CONFIG_V2_WRITES", "false")
	acceptance.binding = NewInstanceGpuBinding(nil, nil, "cluster")
	replay, err := acceptance.createRequestHash("tenant", "user", "7", nil, request, 2)
	require.NoError(t, err)
	require.Equal(t, hash, replay)
}

func TestInstanceConfigurationEmptyOptionalCollectionsHaveOneIntent(t *testing.T) {
	acceptance := &GpuAcceptance{binding: NewInstanceGpuBinding(nil, nil, "cluster")}
	request := &res.CreateInstanceData{Name: "workspace", ImageId: "image", ImageScope: "platform", PresetId: "preset"}
	omitted, err := acceptance.createRequestHash("tenant", "user", "7", nil, request, 2)
	require.NoError(t, err)
	request.Env, request.VolumeMounts = []*res.InstanceEnvVar{}, []*res.VolumeMount{}
	request.Labels, request.Annotations = map[string]string{}, map[string]string{}
	empty, err := acceptance.createRequestHash("tenant", "user", "7", nil, request, 2)
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
	t       *testing.T
	version uint32
}

func (p *instanceConfigurationReadPeer) Configuration(_ context.Context, tenant, actor, id string) (*res.InstanceConfiguration, error) {
	p.t.Helper()
	require.Equal(p.t, presetTenant, tenant)
	require.Equal(p.t, "governance:user:9", actor)
	return &res.InstanceConfiguration{InstanceId: id, InstanceContractVersion: p.version, WorkspaceMountPath: "/workspace"}, nil
}

func TestInstanceConfigurationQueryAcceptsOnlyCurrentContract(t *testing.T) {
	const id = "10000000-0000-4000-8000-000000000001"
	for _, version := range []uint32{0, 1, 2, 3} {
		owner := &instanceConfigurationReadPeer{t: t, version: version}
		authorization := &instanceNetworkViewAuthorization{t: t}
		service := NewInstanceService(nil, NewInstanceGpuBinding(owner, authorization, "cluster"), instancePresetTenantResolver{}, nil, nil)
		actual, err := service.GetInstanceConfiguration(instancePresetContext(7), &view.GetInstanceRequest{InstanceId: id})
		if version == 2 {
			require.NoError(t, err)
			require.EqualValues(t, 2, actual.InstanceContractVersion)
			require.Equal(t, "/workspace", actual.WorkspaceMountPath)
		} else {
			require.Error(t, err)
			require.Nil(t, actual)
		}
	}
}
