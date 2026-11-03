package data

import (
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func environmentReadyFixture(tenant string) *modeldevv1.TenantExecutionEnvironmentView {
	uid, digest, experiment := uuid.NewString(), strings.Repeat("a", 64), uuid.NewString()
	return &modeldevv1.TenantExecutionEnvironmentView{EnvironmentId: uuid.NewString(), ClusterId: uuid.NewString(), NamespaceName: "tenant-" + tenant, NamespaceUid: &uid, State: modeldevv1.TenantExecutionEnvironmentState(2), Phase: modeldevv1.TenantExecutionEnvironmentPhase(6), BindingDigest: &digest, ExperimentId: &experiment, UpdatedAt: timestamppb.Now(), LastVerifiedAt: timestamppb.Now()}
}

type environmentPeer struct {
	modeldevv1.UnimplementedModelDevTenantEnvironmentServiceServer
	view     *modeldevv1.TenantExecutionEnvironmentView
	observed chan metadata.MD
	budgets  chan time.Duration
	missing  atomic.Bool
}

func (p *environmentPeer) observe(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	p.observed <- md.Copy()
	deadline, _ := ctx.Deadline()
	p.budgets <- time.Until(deadline)
}
func (p *environmentPeer) EnsureTenantExecutionEnvironment(ctx context.Context, in *modeldevv1.EnsureTenantExecutionEnvironmentRequest) (*modeldevv1.EnsureTenantExecutionEnvironmentResponse, error) {
	p.observe(ctx)
	return &modeldevv1.EnsureTenantExecutionEnvironmentResponse{Environment: proto.Clone(p.view).(*modeldevv1.TenantExecutionEnvironmentView)}, nil
}
func (p *environmentPeer) GetTenantExecutionEnvironment(ctx context.Context, in *modeldevv1.GetTenantExecutionEnvironmentRequest) (*modeldevv1.GetTenantExecutionEnvironmentResponse, error) {
	p.observe(ctx)
	if p.missing.Load() {
		md, _ := metadata.FromIncomingContext(ctx)
		e, _ := status.New(codes.NotFound, "private-sentinel").WithDetails(&modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_REGISTERED, SafeMessage: "private-sentinel", CorrelationId: md.Get("x-ani-request-id")[0]})
		return nil, e.Err()
	}
	return &modeldevv1.GetTenantExecutionEnvironmentResponse{Environment: proto.Clone(p.view).(*modeldevv1.TenantExecutionEnvironmentView)}, nil
}
func startEnvironmentPeer(t *testing.T, tenant string) (*ModelDevClient, *environmentPeer) {
	t.Helper()
	config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	config.Address = listener.Addr().String()
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	peer := &environmentPeer{view: environmentReadyFixture(tenant), observed: make(chan metadata.MD, 16), budgets: make(chan time.Duration, 16)}
	modeldevv1.RegisterModelDevTenantEnvironmentServiceServer(server, peer)
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	return newModelDevBoundaryClient(t, config), peer
}
func TestModelDevEnvironmentRebuildsMetadataAndFiniteBudgets(t *testing.T) {
	tenant := uuid.NewString()
	client, peer := startEnvironmentPeer(t, tenant)
	scope := ModelDevResolveScope{ResourceTenantID: tenant, Actor: "governance:user:42"}
	poison := metadata.Pairs("x-ani-tenant-id", "foreign", "x-ani-data-scope", "platform-all", "authorization", "private-sentinel")
	ctx := metadata.NewOutgoingContext(context.Background(), poison)
	_, err := client.EnsureTenantExecutionEnvironment(ctx, scope, &modeldevv1.EnsureTenantExecutionEnvironmentRequest{OperationId: uuid.NewString()})
	require.NoError(t, err)
	ensureMD := <-peer.observed
	budget := <-peer.budgets
	require.LessOrEqual(t, budget, 28*time.Second)
	require.Greater(t, budget, 26*time.Second)
	require.Equal(t, []string{tenant}, ensureMD.Get("x-ani-tenant-id"))
	require.Equal(t, []string{"tenant-all"}, ensureMD.Get("x-ani-data-scope"))
	require.Empty(t, ensureMD.Get("authorization"))
	require.Equal(t, []string{modeldevv1.ModelDevTenantEnvironmentService_EnsureTenantExecutionEnvironment_FullMethodName}, ensureMD.Get("x-ani-authorized-method"))
	_, err = client.GetTenantExecutionEnvironment(ctx, scope, &modeldevv1.GetTenantExecutionEnvironmentRequest{})
	require.NoError(t, err)
	getMD := <-peer.observed
	budget = <-peer.budgets
	require.LessOrEqual(t, budget, 3*time.Second)
	require.Greater(t, budget, time.Second)
	require.NotEqual(t, ensureMD.Get("x-ani-request-id"), getMD.Get("x-ani-request-id"))
	require.Equal(t, []string{"foreign"}, poison.Get("x-ani-tenant-id"))
	peer.missing.Store(true)
	_, err = client.GetTenantExecutionEnvironment(ctx, scope, &modeldevv1.GetTenantExecutionEnvironmentRequest{})
	require.Equal(t, codes.NotFound, status.Code(err))
	require.NotContains(t, err.Error(), "private-sentinel")
}
func TestModelDevEnvironmentRejectsUnknownAndIncompleteREADY(t *testing.T) {
	tenant := uuid.NewString()
	for _, c := range []struct {
		name   string
		mutate func(*modeldevv1.TenantExecutionEnvironmentView)
	}{{"missingUID", func(v *modeldevv1.TenantExecutionEnvironmentView) { v.NamespaceUid = nil }}, {"missingDigest", func(v *modeldevv1.TenantExecutionEnvironmentView) { v.BindingDigest = nil }}, {"foreignName", func(v *modeldevv1.TenantExecutionEnvironmentView) { v.NamespaceName = "foreign" }}, {"badState", func(v *modeldevv1.TenantExecutionEnvironmentView) { v.State = 99 }}, {"badPhase", func(v *modeldevv1.TenantExecutionEnvironmentView) { v.Phase = 0 }}, {"unknownView", func(v *modeldevv1.TenantExecutionEnvironmentView) {
		v.ProtoReflect().SetUnknown([]byte{0x80, 0x08, 0x01})
	}}, {"unknownTimestamp", func(v *modeldevv1.TenantExecutionEnvironmentView) {
		v.UpdatedAt.ProtoReflect().SetUnknown([]byte{0x80, 0x08, 0x01})
	}}} {
		t.Run(c.name, func(t *testing.T) {
			v := environmentReadyFixture(tenant)
			c.mutate(v)
			require.False(t, modelDevValidEnvironment(v, tenant))
		})
	}
}

func TestModelDevEnvironmentRejectsBusinessNamespaceSuffix(t *testing.T) {
	tenant := uuid.NewString()
	v := environmentReadyFixture(tenant)
	require.True(t, modelDevValidEnvironment(v, tenant))
	v.NamespaceName += "-inference"
	require.False(t, modelDevValidEnvironment(v, tenant))
}
func TestModelDevEnvironmentErrorWhitelistRejectsExtras(t *testing.T) {
	const correlation = "11111111-1111-4111-8111-111111111111"
	for reason := range modelDevEnvironmentReasons {
		detail := &modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, Kind: "tenant_environment", CorrelationId: correlation, Violations: []*modeldevv1.FieldViolation{{Field: "environment", Reason: reason}}}
		upstream, _ := status.New(codes.Unavailable, "private-sentinel").WithDetails(detail)
		got := modelDevResolutionError(upstream.Err(), correlation)
		require.Equal(t, codes.Unavailable, status.Code(got))
		require.Len(t, status.Convert(got).Details(), 1)
		clean, ok := status.Convert(got).Details()[0].(*modeldevv1.ErrorDetail)
		require.True(t, ok)
		require.Equal(t, reason, clean.Violations[0].Reason)
		require.NotContains(t, got.Error(), "private-sentinel")
	}
	for _, mutate := range []func(*modeldevv1.ErrorDetail){func(d *modeldevv1.ErrorDetail) { d.Kind = "other" }, func(d *modeldevv1.ErrorDetail) {
		d.Violations = append(d.Violations, &modeldevv1.FieldViolation{Field: "environment", Reason: "TENANT_NAMESPACE_NOT_FOUND"})
	}, func(d *modeldevv1.ErrorDetail) { d.Violations[0].Reason = "UNKNOWN" }, func(d *modeldevv1.ErrorDetail) { d.Violations[0].ProtoReflect().SetUnknown([]byte{0x80, 0x08, 0x01}) }, func(d *modeldevv1.ErrorDetail) { d.CorrelationId = "foreign" }} {
		d := &modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, Kind: "tenant_environment", CorrelationId: correlation, Violations: []*modeldevv1.FieldViolation{{Field: "environment", Reason: "TENANT_NAMESPACE_NOT_FOUND"}}}
		mutate(d)
		upstream, _ := status.New(codes.Unavailable, "private-sentinel").WithDetails(d)
		require.Empty(t, status.Convert(modelDevResolutionError(upstream.Err(), correlation)).Details())
	}
}
