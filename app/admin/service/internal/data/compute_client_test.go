package data

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	instance "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type computeRPCObservation struct {
	method, connection string
	metadata           metadata.MD
	remaining          time.Duration
}

// Only Resource's RPC boundary is substituted. Both generated clients, the
// shared production factory, mTLS, metadata, deadlines and validation are real.
type computeRPCBoundary struct {
	compute.UnimplementedComputePresetServiceServer
	instance.UnimplementedInstanceServiceServer
	corrupt atomic.Int32
}

func (*computeRPCBoundary) GetInstance(context.Context, *instance.GetInstanceRequest) (*instance.Instance, error) {
	return &instance.Instance{}, nil
}
func (*computeRPCBoundary) ListPresets(context.Context, *compute.ListPresetsRequest) (*compute.ListPresetsResponse, error) {
	return &compute.ListPresetsResponse{}, nil
}
func (*computeRPCBoundary) AdminListPresets(context.Context, *compute.AdminListPresetsRequest) (*compute.ListPresetsResponse, error) {
	return &compute.ListPresetsResponse{}, nil
}
func (*computeRPCBoundary) AdminGetPreset(context.Context, *compute.AdminGetPresetRequest) (*compute.Preset, error) {
	return &compute.Preset{}, nil
}
func (*computeRPCBoundary) AdminCreatePreset(context.Context, *compute.AdminCreatePresetRequest) (*compute.Preset, error) {
	return &compute.Preset{}, nil
}
func (*computeRPCBoundary) AdminUpdatePreset(context.Context, *compute.AdminUpdatePresetRequest) (*compute.Preset, error) {
	return &compute.Preset{}, nil
}
func (*computeRPCBoundary) AdminSetPresetEnabled(context.Context, *compute.AdminSetPresetEnabledRequest) (*compute.Preset, error) {
	return &compute.Preset{}, nil
}
func (*computeRPCBoundary) AdminDeletePreset(context.Context, *compute.AdminDeletePresetRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
func (s *computeRPCBoundary) ResolveComputePreset(ctx context.Context, request *compute.ResolveComputePresetRequest) (*compute.ComputeSelection, error) {
	if s.corrupt.Load() == 4 {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	selection := &compute.ComputeSelection{SchemaVersion: compute.SelectionSchemaVersion, PresetId: request.PresetId, Code: "cpu-test", Name: "CPU", ComputeType: "cpu", CpuMillis: 1000, MemoryMib: 1024}
	selection.SelectionDigest, _ = compute.SelectionDigest(selection)
	switch s.corrupt.Load() {
	case 1:
		selection.SchemaVersion++
	case 2:
		selection.MemoryMib++
	case 3:
		selection.PresetId = "40000000-0000-4000-8000-000000000004"
		selection.SelectionDigest, _ = compute.SelectionDigest(selection)
	}
	return selection, nil
}

func TestComputeClientSharedConnectionIdentityAndSelection(t *testing.T) {
	f := resourceClientTestCertificates(t, nil, []string{"ani-network-service"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	seen := make(chan computeRPCObservation, 32)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(f.server)), grpc.UnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		remote, _ := peer.FromContext(ctx)
		deadline, _ := ctx.Deadline()
		seen <- computeRPCObservation{method: info.FullMethod, connection: remote.Addr.String(), metadata: md, remaining: time.Until(deadline)}
		return handler(ctx, request)
	}))
	boundary := &computeRPCBoundary{}
	compute.RegisterComputePresetServiceServer(server, boundary)
	instance.RegisterInstanceServiceServer(server, boundary)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); <-done })
	for key, value := range map[string]string{"ANI_INSTANCE_ADDR": listener.Addr().String(), "ANI_INSTANCE_CA": f.caFile, "ANI_INSTANCE_CERT": f.certFile, "ANI_INSTANCE_KEY": f.keyFile} {
		t.Setenv(key, value)
	}
	owner, catalog, cleanup, err := NewResourceClientsFromEnv()
	require.NoError(t, err)
	t.Cleanup(cleanup)
	const tenant, actor, preset = "20000000-0000-4000-8000-000000000002", "governance:user:9", "30000000-0000-4000-8000-000000000003"
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "private", "x-ani-tenant-id", "forged", "x-ani-scope", "platform", "x-ani-actor", "forged"))
	_, err = owner.Get(ctx, tenant, actor, "instance")
	require.NoError(t, err)
	first := <-seen
	_, err = catalog.ListPresets(ctx, tenant, actor, &compute.ListPresetsRequest{TenantId: tenant, Usage: compute.Usage_CONTAINER_INSTANCE})
	require.NoError(t, err)
	observation := <-seen
	require.Equal(t, "/compute.v1.ComputePresetService/ListPresets", observation.method)
	require.Equal(t, first.connection, observation.connection, "domain clients must share the one connection")
	require.Equal(t, []string{tenant}, observation.metadata.Get("x-ani-tenant-id"))
	require.Equal(t, []string{actor}, observation.metadata.Get("x-ani-actor"))
	require.Empty(t, observation.metadata.Get("authorization"))
	require.Empty(t, observation.metadata.Get("x-ani-scope"))
	for _, usage := range []compute.Usage{compute.Usage_CONTAINER_INSTANCE, compute.Usage_TRAINING, compute.Usage_INFERENCE} {
		selection, err := catalog.ResolveComputePreset(ctx, tenant, actor, &compute.ResolveComputePresetRequest{TenantId: tenant, PresetId: preset, Usage: usage})
		require.NoError(t, err)
		require.NoError(t, compute.ValidateSelection(selection))
		observation := <-seen
		require.Equal(t, "/compute.v1.ComputePresetService/ResolveComputePreset", observation.method)
		require.Greater(t, observation.remaining, time.Duration(0))
		require.LessOrEqual(t, observation.remaining, 3*time.Second)
	}
	for _, mode := range []int32{1, 2, 3} {
		boundary.corrupt.Store(mode)
		_, err = catalog.ResolveComputePreset(ctx, tenant, actor, &compute.ResolveComputePresetRequest{TenantId: tenant, PresetId: preset, Usage: compute.Usage_TRAINING})
		require.Equal(t, codes.Unavailable, status.Code(err), "reject version, digest and preset mismatch")
		<-seen
	}
	for _, request := range []*compute.ResolveComputePresetRequest{nil, {TenantId: "other", PresetId: preset, Usage: compute.Usage_TRAINING}, {TenantId: tenant, PresetId: preset}, {TenantId: tenant, PresetId: preset, Usage: compute.Usage(999)}} {
		_, err = catalog.ResolveComputePreset(ctx, tenant, actor, request)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	require.Empty(t, seen, "invalid requests must not reach Resource")
	boundary.corrupt.Store(4)
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = catalog.ResolveComputePreset(short, tenant, actor, &compute.ResolveComputePresetRequest{TenantId: tenant, PresetId: preset, Usage: compute.Usage_TRAINING})
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	observation = <-seen
	require.LessOrEqual(t, observation.remaining, 50*time.Millisecond)
	for _, call := range []struct {
		method string
		invoke func() error
	}{
		{"AdminListPresets", func() error {
			_, e := catalog.AdminListPresets(ctx, actor, &compute.AdminListPresetsRequest{})
			return e
		}},
		{"AdminGetPreset", func() error { _, e := catalog.AdminGetPreset(ctx, actor, &compute.AdminGetPresetRequest{}); return e }},
		{"AdminCreatePreset", func() error {
			_, e := catalog.AdminCreatePreset(ctx, actor, &compute.AdminCreatePresetRequest{})
			return e
		}},
		{"AdminUpdatePreset", func() error {
			_, e := catalog.AdminUpdatePreset(ctx, actor, &compute.AdminUpdatePresetRequest{})
			return e
		}},
		{"AdminSetPresetEnabled", func() error {
			_, e := catalog.AdminSetPresetEnabled(ctx, actor, &compute.AdminSetPresetEnabledRequest{})
			return e
		}},
		{"AdminDeletePreset", func() error {
			_, e := catalog.AdminDeletePreset(ctx, actor, &compute.AdminDeletePresetRequest{})
			return e
		}},
	} {
		require.NoError(t, call.invoke())
		observation := <-seen
		require.Equal(t, "/compute.v1.ComputePresetService/"+call.method, observation.method)
		require.Equal(t, []string{"platform"}, observation.metadata.Get("x-ani-scope"))
		require.Empty(t, observation.metadata.Get("x-ani-tenant-id"))
		require.Empty(t, observation.metadata.Get("authorization"))
	}
	cleanup()
	_, err = catalog.ListPresets(ctx, tenant, actor, &compute.ListPresetsRequest{TenantId: tenant, Usage: compute.Usage_CONTAINER_INSTANCE})
	require.Error(t, err, "one cleanup closes the shared connection")
	_, err = owner.Get(ctx, tenant, actor, "instance")
	require.Error(t, err)
}
