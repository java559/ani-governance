package service

import (
	"context"
	"testing"
	"time"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/stretchr/testify/require"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type modelQueryOwnerProbe struct {
	modelOwnerProbe
	model      *modelv1.Model
	reply      *modelv1.ListModelOperationsResponse
	operations func(string, string, *modelv1.ListModelOperationsRequest)
	versions   func(string, string, *modelv1.ListModelVersionsRequest)
	getError   error
}

func (p *modelQueryOwnerProbe) GetModel(context.Context, string, string, *modelv1.GetModelRequest) (*modelv1.Model, error) {
	return p.model, p.getError
}
func (p *modelQueryOwnerProbe) ListModelOperations(_ context.Context, tenant, actor string, req *modelv1.ListModelOperationsRequest) (*modelv1.ListModelOperationsResponse, error) {
	if p.operations != nil {
		p.operations(tenant, actor, req)
	}
	return p.reply, nil
}
func (p *modelQueryOwnerProbe) ListModelVersions(_ context.Context, tenant, actor string, req *modelv1.ListModelVersionsRequest) (*modelv1.ListModelVersionsResponse, error) {
	p.versions(tenant, actor, req)
	return &modelv1.ListModelVersionsResponse{Total: 12}, nil
}

func TestModelQueriesForwardTrustedIdentityAndPagination(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111111"
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	event := &modelv1.ModelOperation{Id: "event-1", ModelId: "internal-1", Action: "import", Actor: "governance:user:7", Result: "accepted", CreatedAt: timestamppb.New(time.Now()), TaskId: "task-1"}
	p := &modelQueryOwnerProbe{model: &modelv1.Model{Id: "internal-1", ModelId: "qwen", TenantId: tenant}, reply: &modelv1.ListModelOperationsResponse{Items: []*modelv1.ModelOperation{event}, Total: 12}}
	p.operations = func(tid, actor string, req *modelv1.ListModelOperationsRequest) {
		require.Equal(t, tenant, tid)
		require.Equal(t, "governance:user:7", actor)
		require.Equal(t, "internal-1", req.ModelId)
		require.Equal(t, int32(3), req.GetPage())
		require.Equal(t, int32(2), req.GetPageSize())
	}
	s := NewModelService(p, modelTenantProbe{})
	out, err := s.ListModelOperations(ctx, &catalogv1.ListModelOperationsRequest{ModelId: "qwen", Page: proto.Int32(3), PageSize: proto.Int32(2)})
	require.NoError(t, err)
	require.Equal(t, int64(12), out.Total)
	require.Equal(t, "accepted", out.Items[0].Result)
	require.Equal(t, "task-1", out.Items[0].TaskId)
	p.versions = func(tid, actor string, req *modelv1.ListModelVersionsRequest) {
		require.Equal(t, tenant, tid)
		require.Equal(t, "governance:user:7", actor)
		require.Equal(t, "failed", req.Status)
		require.Equal(t, int32(3), req.GetPage())
		require.Equal(t, int32(2), req.GetPageSize())
	}
	versions, err := s.ListModelVersions(ctx, &catalogv1.ListModelVersionsRequest{ModelId: "qwen", Status: "failed", Page: proto.Int32(3), PageSize: proto.Int32(2)})
	require.NoError(t, err)
	require.Equal(t, int64(12), versions.Total)
	require.NotNil(t, versions.Items)
	p.operations = nil
	p.reply = &modelv1.ListModelOperationsResponse{Total: 12}
	empty, err := s.ListModelOperations(ctx, &catalogv1.ListModelOperationsRequest{ModelId: "qwen", Page: proto.Int32(99)})
	require.NoError(t, err)
	require.NotNil(t, empty.Items)
	require.Empty(t, empty.Items)
	require.Equal(t, int64(12), empty.Total)
}

func TestModelOperationsRejectInvalidScopeAndReplies(t *testing.T) {
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	p := &modelQueryOwnerProbe{model: &modelv1.Model{Id: "model-1", TenantId: "another-tenant"}}
	s := NewModelService(p, modelTenantProbe{})
	request := &catalogv1.ListModelOperationsRequest{ModelId: "model-1"}
	_, err := s.ListModelOperations(ctx, request)
	require.Error(t, err)
	p.model.TenantId = "11111111-1111-4111-8111-111111111111"
	p.getError = status.Error(codes.NotFound, "hidden model")
	_, err = s.ListModelOperations(ctx, request)
	require.Error(t, err)
	p.getError = nil
	for _, reply := range []*modelv1.ListModelOperationsResponse{nil, {Total: -1}, {Items: []*modelv1.ModelOperation{nil}, Total: 1}, {Items: []*modelv1.ModelOperation{{Id: "e1", ModelId: "another-model", Action: "create", Result: "succeeded", CreatedAt: timestamppb.Now()}}, Total: 1}} {
		p.reply = reply
		_, err = s.ListModelOperations(ctx, request)
		require.Error(t, err)
	}
	_, err = s.ListModelOperations(context.Background(), request)
	require.Error(t, err)
	for _, req := range []*catalogv1.ListModelOperationsRequest{nil, {}, {ModelId: "model-1", Page: proto.Int32(0)}, {ModelId: "model-1", PageSize: proto.Int32(101)}} {
		_, err = s.ListModelOperations(ctx, req)
		require.Error(t, err)
	}
}
