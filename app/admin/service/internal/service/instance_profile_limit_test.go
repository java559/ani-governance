package service

import (
	"context"
	"testing"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// Only the ACC catalog RPC boundary is substituted. The production Gov resolver
// validates a higher-max Profile fixture and recomputes its fixed Spec digest.
type instanceProfileLimitAdmin struct{ plan *gpuPlanFixture }

func (p *instanceProfileLimitAdmin) AdminListProfiles(context.Context, *acc.AdminListProfilesRequest, ...grpc.CallOption) (*acc.ListProfilesResponse, error) {
	return &acc.ListProfilesResponse{Items: []*acc.GpuProfile{p.plan.Profile}}, nil
}
func (p *instanceProfileLimitAdmin) ListPools(context.Context, *acc.ListPoolsRequest, ...grpc.CallOption) (*acc.ListPoolsResponse, error) {
	return &acc.ListPoolsResponse{Items: []*acc.Pool{{PoolId: p.plan.Request.PoolId, ClusterId: p.plan.Request.ClusterId}}}, nil
}
func (p *instanceProfileLimitAdmin) ListSupplyGroups(context.Context, *acc.ListSupplyGroupsRequest, ...grpc.CallOption) (*acc.ListSupplyGroupsResponse, error) {
	s := p.plan.Profile.Spec
	return &acc.ListSupplyGroupsResponse{Items: []*acc.SupplyGroup{{GroupId: s.GroupId, PoolId: p.plan.Request.PoolId, ClusterId: p.plan.Request.ClusterId, Mode: s.Mode, ModelKey: s.ModelKey, Baseline: &acc.ProviderBaseline{BaselineId: p.plan.Profile.BaselineId}}}}, nil
}
func TestInstancePresetUsesProfileLimit(t *testing.T) {
	plan, _, _ := gpuVector(t)
	plan.Profile.Spec.Mode = acc.SupplyMode_WHOLE_EXCLUSIVE
	plan.Profile.Spec.MemoryMibPerDevice = 15360
	plan.Profile.Spec.CoreLimitPercent = 100
	plan.Profile.Spec.MaxDevicesPerReplica = 4
	plan.Profile.Spec.IsolationClass = "WHOLE_DEVICE_EXCLUSIVE"
	plan.Request.DevicesPerReplica = 4
	var err error
	plan.Profile.SpecDigest, err = gpuSpecDigest(plan.Profile)
	if err != nil {
		t.Fatal(err)
	}
	s := NewInstanceService(nil, nil, nil, nil, nil, InstanceCatalogConfig{ClusterID: plan.Request.ClusterId, Admin: &instanceProfileLimitAdmin{plan: plan}})
	input := &view.PresetGpuInput{ProfileId: plan.Profile.ProfileId, ProfileVersion: plan.Profile.ProfileVersion, DevicesPerInstance: plan.Request.DevicesPerReplica}
	ref, err := s.resolvePresetGPU(context.Background(), "governance:user:9", input)
	if err != nil {
		t.Fatal(err)
	}
	if ref.DevicesPerInstance != input.DevicesPerInstance || ref.SpecDigest != plan.Profile.SpecDigest {
		t.Fatal("managed Profile reference changed")
	}
	over := proto.Clone(input).(*view.PresetGpuInput)
	over.DevicesPerInstance++
	if _, err = s.resolvePresetGPU(context.Background(), "governance:user:9", over); kerrors.Code(err) != 400 {
		t.Fatalf("above Profile max: %v", err)
	}
}
