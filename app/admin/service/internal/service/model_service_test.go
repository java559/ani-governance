package service

import (
	"context"
	"testing"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/types/known/emptypb"
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
func (*modelOwnerProbe) SearchExternalModels(context.Context, string, string, *modelv1.SearchExternalModelsRequest) (*modelv1.SearchExternalModelsResponse, error) {
	panic("unused")
}
func (*modelOwnerProbe) DeleteModel(context.Context, string, string, *modelv1.DeleteModelRequest) (*emptypb.Empty, error) {
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
func (*modelOwnerProbe) DeleteModelVersion(context.Context, string, string, *modelv1.DeleteModelVersionRequest) (*modelv1.DeleteModelVersionResponse, error) {
	panic("unused")
}
func (*modelOwnerProbe) GetUploadURL(context.Context, string, string, *modelv1.GetUploadURLRequest) (*modelv1.GetUploadURLResponse, error) {
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
func (*modelOwnerProbe) GetModelDownloadURL(context.Context, string, string, *modelv1.GetModelDownloadURLRequest) (*modelv1.GetModelDownloadURLResponse, error) {
	panic("unused")
}

type modelOwnerExtraProbe struct {
	modelOwnerProbe
	search   func(context.Context, string, string, *modelv1.SearchExternalModelsRequest) (*modelv1.SearchExternalModelsResponse, error)
	delete   func(context.Context, string, string, *modelv1.DeleteModelRequest) (*emptypb.Empty, error)
	version  func(context.Context, string, string, *modelv1.DeleteModelVersionRequest) (*modelv1.DeleteModelVersionResponse, error)
	upload   func(context.Context, string, string, *modelv1.GetUploadURLRequest) (*modelv1.GetUploadURLResponse, error)
	download func(context.Context, string, string, *modelv1.GetModelDownloadURLRequest) (*modelv1.GetModelDownloadURLResponse, error)
}

func (p *modelOwnerExtraProbe) SearchExternalModels(ctx context.Context, tenant, actor string, req *modelv1.SearchExternalModelsRequest) (*modelv1.SearchExternalModelsResponse, error) {
	return p.search(ctx, tenant, actor, req)
}
func (p *modelOwnerExtraProbe) DeleteModel(ctx context.Context, tenant, actor string, req *modelv1.DeleteModelRequest) (*emptypb.Empty, error) {
	return p.delete(ctx, tenant, actor, req)
}
func (p *modelOwnerExtraProbe) DeleteModelVersion(ctx context.Context, tenant, actor string, req *modelv1.DeleteModelVersionRequest) (*modelv1.DeleteModelVersionResponse, error) {
	return p.version(ctx, tenant, actor, req)
}
func (p *modelOwnerExtraProbe) GetUploadURL(ctx context.Context, tenant, actor string, req *modelv1.GetUploadURLRequest) (*modelv1.GetUploadURLResponse, error) {
	return p.upload(ctx, tenant, actor, req)
}
func (p *modelOwnerExtraProbe) GetModelDownloadURL(ctx context.Context, tenant, actor string, req *modelv1.GetModelDownloadURLRequest) (*modelv1.GetModelDownloadURLResponse, error) {
	return p.download(ctx, tenant, actor, req)
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

func TestModelServiceForwardsExternalSearchIdentityAndFilters(t *testing.T) {
	client := &modelOwnerExtraProbe{search: func(_ context.Context, tenant, actor string, req *modelv1.SearchExternalModelsRequest) (*modelv1.SearchExternalModelsResponse, error) {
		if tenant != "11111111-1111-4111-8111-111111111111" || actor != "governance:user:7" || req.Source != "modelscope" || req.Keyword != "qwen" || req.Owner != "Qwen" || req.Page.GetLimit() != 10 {
			t.Fatalf("unexpected search delegation: tenant=%q actor=%q req=%#v", tenant, actor, req)
		}
		return &modelv1.SearchExternalModelsResponse{Candidates: []*modelv1.ExternalModelCandidate{{Source: "modelscope", RepoId: "Qwen/Qwen2-0.5B", Name: "Qwen2-0.5B", Revision: "master"}}, Meta: &modelv1.CursorPageMeta{HasMore: true, NextCursor: "next"}}, nil
	}}
	s := NewModelService(client, modelTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	reply, err := s.SearchExternalModels(ctx, &catalogv1.SearchExternalModelsRequest{Source: "modelscope", Keyword: "qwen", Owner: "Qwen", Page: &catalogv1.CursorPage{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.GetCandidates()) != 1 || reply.GetCandidates()[0].GetRepoId() != "Qwen/Qwen2-0.5B" || !reply.GetMeta().GetHasMore() || reply.GetMeta().GetNextCursor() != "next" {
		t.Fatalf("unexpected search response: %#v", reply)
	}
}

func TestModelServiceForwardsModelDeletionAndUpload(t *testing.T) {
	var deletedModel, deletedVersion string
	client := &modelOwnerExtraProbe{
		delete: func(_ context.Context, tenant, actor string, req *modelv1.DeleteModelRequest) (*emptypb.Empty, error) {
			if tenant != "11111111-1111-4111-8111-111111111111" || actor != "governance:user:7" {
				t.Fatalf("unexpected delete identity: %q %q", tenant, actor)
			}
			deletedModel = req.ModelId
			return &emptypb.Empty{}, nil
		},
		version: func(_ context.Context, tenant, actor string, req *modelv1.DeleteModelVersionRequest) (*modelv1.DeleteModelVersionResponse, error) {
			if tenant != "11111111-1111-4111-8111-111111111111" || actor != "governance:user:7" {
				t.Fatalf("unexpected version deletion identity: %q %q", tenant, actor)
			}
			deletedVersion = req.ModelVersionId
			return &modelv1.DeleteModelVersionResponse{}, nil
		},
		upload: func(_ context.Context, tenant, actor string, req *modelv1.GetUploadURLRequest) (*modelv1.GetUploadURLResponse, error) {
			if tenant != "11111111-1111-4111-8111-111111111111" || actor != "governance:user:7" || req.ModelId != "model-1" || req.Version != "v1" || req.ChecksumSha256 == "" {
				t.Fatalf("unexpected upload delegation: %q %q %#v", tenant, actor, req)
			}
			return &modelv1.GetUploadURLResponse{UploadUrl: "https://storage.example/upload", StoragePath: "object://models/model-1/v1/model.bin", DocId: "doc-1"}, nil
		},
	}
	s := NewModelService(client, modelTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	if _, err := s.DeleteModel(ctx, &catalogv1.DeleteModelRequest{ModelId: "model-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteModelVersion(ctx, &catalogv1.DeleteModelVersionRequest{ModelVersionId: "version-1"}); err != nil {
		t.Fatal(err)
	}
	upload, err := s.GetUploadURL(ctx, &catalogv1.GetUploadURLRequest{ModelId: "model-1", Data: &catalogv1.GetUploadURLData{Version: "v1", FileName: "model.bin", SizeBytes: 8, IdempotencyKey: "upload-1", ChecksumSha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}})
	if err != nil {
		t.Fatal(err)
	}
	if deletedModel != "model-1" || deletedVersion != "version-1" || upload.GetUploadUrl() == "" || upload.GetStoragePath() == "" {
		t.Fatalf("unexpected delegation result: model=%q version=%q upload=%#v", deletedModel, deletedVersion, upload)
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
