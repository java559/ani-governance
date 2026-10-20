package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type instanceNetworkViewOwner struct {
	InstanceOwnerClient
	t           *testing.T
	item        *res.Instance
	gets, lists int
}

func (p *instanceNetworkViewOwner) Get(_ context.Context, tenant, actor, id string) (*res.Instance, error) {
	p.t.Helper()
	require.Equal(p.t, presetTenant, tenant)
	require.Equal(p.t, "governance:user:9", actor)
	require.Equal(p.t, p.item.Id, id)
	p.gets++
	return proto.Clone(p.item).(*res.Instance), nil
}

func (p *instanceNetworkViewOwner) List(_ context.Context, tenant, actor string, request *res.ListInstancesRequest) (*res.ListInstancesResponse, error) {
	p.t.Helper()
	require.Equal(p.t, presetTenant, tenant)
	require.Equal(p.t, "governance:user:9", actor)
	require.Equal(p.t, tenant, request.TenantId)
	p.lists++
	return &res.ListInstancesResponse{Items: []*res.Instance{proto.Clone(p.item).(*res.Instance)}, Total: 1}, nil
}

type instanceNetworkViewAuthorization struct {
	t     *testing.T
	paths []string
}

func (a *instanceNetworkViewAuthorization) Authorize(_ context.Context, tenant, actor uint32, path, method string) error {
	a.t.Helper()
	require.EqualValues(a.t, 7, tenant)
	require.EqualValues(a.t, 9, actor)
	require.Equal(a.t, "GET", method)
	require.Contains(a.t, []string{data.InstanceGetPath, data.InstanceCreatePath}, path)
	a.paths = append(a.paths, path)
	return nil
}

func TestInstanceNetworkSafeStatusDetailAndList(t *testing.T) {
	for _, tc := range []struct {
		name, state, reason         string
		cleaned, referencesReleased bool
	}{
		{name: "unselected", state: "not_requested", cleaned: true},
		{name: "waiting", state: "pending", reason: "waiting_for_current_pod"},
		{name: "released", state: "released", cleaned: true, referencesReleased: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := &instanceNetworkViewOwner{t: t, item: &res.Instance{Id: "instance-network-view", NetworkState: tc.state, NetworkReason: tc.reason, NetworkCleaned: tc.cleaned, VolumeReferencesReleased: tc.referencesReleased}}
			authorization := &instanceNetworkViewAuthorization{t: t}
			service := NewInstanceService(nil, NewInstanceGpuBinding(owner, authorization, "cluster"), instancePresetTenantResolver{}, nil, nil)
			assertStatus := func(t *testing.T, actual *view.Instance) {
				t.Helper()
				raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(actual)
				require.NoError(t, err)
				var body map[string]any
				require.NoError(t, json.Unmarshal(raw, &body))
				require.Equal(t, tc.state, body["networkState"])
				require.Equal(t, tc.reason, body["networkReason"])
				require.Equal(t, tc.cleaned, body["networkCleaned"])
				require.Equal(t, tc.referencesReleased, body["volumeReferencesReleased"])
			}
			t.Run("detail", func(t *testing.T) {
				owner.t, authorization.t = t, t
				actual, err := service.GetInstance(instancePresetContext(7), &view.GetInstanceRequest{InstanceId: owner.item.Id})
				require.NoError(t, err)
				assertStatus(t, actual)
			})
			t.Run("list", func(t *testing.T) {
				owner.t, authorization.t = t, t
				actual, err := service.ListInstances(instancePresetContext(7), &view.ListInstancesRequest{})
				require.NoError(t, err)
				require.EqualValues(t, 1, actual.Total)
				require.Len(t, actual.Items, 1)
				assertStatus(t, actual.Items[0])
			})
			require.Equal(t, 1, owner.gets)
			require.Equal(t, 1, owner.lists)
			require.Equal(t, []string{data.InstanceGetPath, data.InstanceCreatePath}, authorization.paths)
		})
	}
}
