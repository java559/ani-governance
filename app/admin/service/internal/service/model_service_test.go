package service

import (
	"context"
	"testing"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
)

type modelOwnerProbe struct {
	create func(context.Context, string, string, *modelv1.CreateModelRequest) (*modelv1.Model, error)
}

func (p *modelOwnerProbe) CreateModel(ctx context.Context, tenant, actor string, request *modelv1.CreateModelRequest) (*modelv1.Model, error) {
	return p.create(ctx, tenant, actor, request)
}
func (*modelOwnerProbe) GetModel(context.Context, string, string, *modelv1.GetModelRequest) (*modelv1.Model, error) {
	panic("unused")
}
func (*modelOwnerProbe) ListModels(context.Context, string, string, *modelv1.ListModelsRequest) (*modelv1.ListModelsResponse, error) {
	panic("unused")
}
func (*modelOwnerProbe) CreateModelVersion(context.Context, string, string, *modelv1.CreateModelVersionRequest) (*modelv1.ModelVersion, error) {
	panic("unused")
}
func (*modelOwnerProbe) GetModelVersion(context.Context, string, string, *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error) {
	panic("unused")
}
func (*modelOwnerProbe) ListModelVersions(context.Context, string, string, *modelv1.ListModelVersionsRequest) (*modelv1.ListModelVersionsResponse, error) {
	panic("unused")
}
func (*modelOwnerProbe) ImportModel(context.Context, string, string, *modelv1.ImportModelRequest) (*modelv1.ImportTask, error) {
	panic("unused")
}
func (*modelOwnerProbe) GetImportTask(context.Context, string, string, *modelv1.GetImportTaskRequest) (*modelv1.GetImportTaskResponse, error) {
	panic("unused")
}
func (*modelOwnerProbe) RetryImportTask(context.Context, string, string, *modelv1.RetryImportTaskRequest) (*modelv1.RetryImportTaskResponse, error) {
	panic("unused")
}

type modelTenantProbe struct{}

func (modelTenantProbe) ResourceTenantID(context.Context, uint32) (string, error) {
	return "11111111-1111-4111-8111-111111111111", nil
}

func TestModelServiceUsesAuthenticatedTenantAndActor(t *testing.T) {
	var gotTenant, gotActor string
	client := &modelOwnerProbe{create: func(_ context.Context, tenant, actor string, request *modelv1.CreateModelRequest) (*modelv1.Model, error) {
		gotTenant, gotActor = tenant, actor
		if request.TenantId != tenant || request.Name != "qwen2.5-1.5b-instruct" {
			t.Fatalf("unexpected downstream request: %#v", request)
		}
		return &modelv1.Model{TenantId: tenant, Id: "model-1", Name: request.Name}, nil
	}}
	s := NewModelService(client, modelTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	reply, err := s.CreateModel(ctx, &catalogv1.CreateModelRequest{Data: &catalogv1.CreateModelData{Name: "qwen2.5-1.5b-instruct", IdempotencyKey: "create-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotTenant != "11111111-1111-4111-8111-111111111111" || gotActor != "governance:user:7" || reply.GetModel().GetName() != "qwen2.5-1.5b-instruct" {
		t.Fatalf("unexpected identity or reply: tenant=%q actor=%q reply=%#v", gotTenant, gotActor, reply)
	}
}

func TestModelServiceCreatesVersionWithCallerSuppliedEngine(t *testing.T) {
	var gotTenant, gotActor string
	clientVersion := &modelOwnerVersionProbe{
		create: func(_ context.Context, tenant, actor string, request *modelv1.CreateModelVersionRequest) (*modelv1.ModelVersion, error) {
			gotTenant, gotActor = tenant, actor
			if request.ModelId != "qwen2.5-1.5b-instruct" || request.EngineType != "vllm" || request.StartupCommand != "vllm --served-model-name qwen2.5-1.5b-instruct" || request.EngineImage != "registry/vllm:latest" {
				t.Fatalf("engine input changed: %#v", request)
			}
			return &modelv1.ModelVersion{Id: "version-1", Version: "main", ModelId: request.ModelId, EngineImage: request.EngineImage}, nil
		},
	}
	s := NewModelService(clientVersion, modelTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	reply, err := s.CreateModelVersion(ctx, &catalogv1.CreateModelVersionRequest{Data: &catalogv1.CreateModelVersionData{
		ModelId: "qwen2.5-1.5b-instruct", Version: "main", IdempotencyKey: "version-1", EngineType: "vllm", StartupCommand: "vllm --served-model-name qwen2.5-1.5b-instruct", EngineImage: "registry/vllm:latest",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if gotTenant != "11111111-1111-4111-8111-111111111111" || gotActor != "governance:user:7" || reply.GetVersion().GetId() != "version-1" || reply.GetVersion().GetEngineImage() != "registry/vllm:latest" {
		t.Fatalf("unexpected identity or reply: tenant=%q actor=%q reply=%#v", gotTenant, gotActor, reply)
	}
}

type modelOwnerVersionProbe struct {
	modelOwnerProbe
	create func(context.Context, string, string, *modelv1.CreateModelVersionRequest) (*modelv1.ModelVersion, error)
}

func (p *modelOwnerVersionProbe) CreateModelVersion(ctx context.Context, tenant, actor string, request *modelv1.CreateModelVersionRequest) (*modelv1.ModelVersion, error) {
	return p.create(ctx, tenant, actor, request)
}

func (*modelOwnerVersionProbe) GetModelVersion(context.Context, string, string, *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error) {
	panic("unused")
}

func (*modelOwnerVersionProbe) ListModelVersions(context.Context, string, string, *modelv1.ListModelVersionsRequest) (*modelv1.ListModelVersionsResponse, error) {
	panic("unused")
}

var _ ResourceTenantResolver = modelTenantProbe{}
var _ ModelOwnerClient = (*modelOwnerProbe)(nil)
