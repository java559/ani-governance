package service

import (
	"context"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/types/known/emptypb"
)

type modelOwnerProbe struct {
	create func(context.Context, string, string, *modelv1.CreateModelRequest) (*modelv1.Model, error)
	bundle func(context.Context, string, string, *modelv1.ImportModelBundleRequest) (*modelv1.ImportModelBundleResponse, error)
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
func (p *modelOwnerProbe) ImportModelBundle(ctx context.Context, tenant, actor string, req *modelv1.ImportModelBundleRequest) (*modelv1.ImportModelBundleResponse, error) {
	if p.bundle == nil {
		panic("unused")
	}
	return p.bundle(ctx, tenant, actor, req)
}
func (*modelOwnerProbe) GetImportTask(context.Context, string, string, *modelv1.GetImportTaskRequest) (*modelv1.GetImportTaskResponse, error) {
	panic("unused")
}
func (*modelOwnerProbe) GetImportTaskLogs(context.Context, string, string, *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error) {
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
	logs     func(context.Context, string, string, *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error)
	search   func(context.Context, string, string, *modelv1.SearchExternalModelsRequest) (*modelv1.SearchExternalModelsResponse, error)
	delete   func(context.Context, string, string, *modelv1.DeleteModelRequest) (*emptypb.Empty, error)
	version  func(context.Context, string, string, *modelv1.DeleteModelVersionRequest) (*modelv1.DeleteModelVersionResponse, error)
	upload   func(context.Context, string, string, *modelv1.GetUploadURLRequest) (*modelv1.GetUploadURLResponse, error)
	download func(context.Context, string, string, *modelv1.GetModelDownloadURLRequest) (*modelv1.GetModelDownloadURLResponse, error)
}

func (p *modelOwnerExtraProbe) GetImportTaskLogs(ctx context.Context, tenant, actor string, req *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error) {
	return p.logs(ctx, tenant, actor, req)
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

func TestModelServiceForwardsModelBundle(t *testing.T) {
	client := &modelOwnerProbe{bundle: func(_ context.Context, tenant, actor string, req *modelv1.ImportModelBundleRequest) (*modelv1.ImportModelBundleResponse, error) {
		if tenant != "11111111-1111-4111-8111-111111111111" || actor != "governance:user:7" || req.IdempotencyKey != "bundle-1" || req.Model.GetName() != "qwen" || req.Model.GetExternalModelId() != "Qwen/Qwen3-0.6B" || req.Version.GetStartupCommand() != "vllm serve /models --served-model-name qwen" || req.Source.GetProvider() != "modelscope" || req.Source.GetRepoId() != "Qwen/Qwen3-0.6B" {
			t.Fatalf("unexpected bundle delegation: tenant=%q actor=%q req=%#v", tenant, actor, req)
		}
		return &modelv1.ImportModelBundleResponse{
			Model:   &modelv1.Model{TenantId: tenant, Id: "model-1", Name: "qwen"},
			Version: &modelv1.ModelVersion{Id: "version-1", ModelId: "model-1", Version: "main"},
			Task:    &modelv1.ImportTask{TaskId: "task-1", ModelId: "model-1", ModelVersionId: "version-1"},
		}, nil
	}}
	s := NewModelService(client, modelTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	reply, err := s.ImportModelBundle(ctx, &catalogv1.ImportModelBundleRequest{Data: &catalogv1.ImportModelBundleData{
		IdempotencyKey: "bundle-1",
		Model:          &catalogv1.ImportModelBundleModelData{Name: "qwen", Capabilities: []string{"text-generation"}, ExternalModelId: "Qwen/Qwen3-0.6B"},
		Version:        &catalogv1.ImportModelBundleVersionData{Version: "main", Format: "safetensors", EngineType: "vllm", EngineImage: "registry/vllm:tag", StartupCommand: "vllm serve /models --served-model-name qwen"},
		Source:         &catalogv1.ImportModelBundleSourceData{Provider: "modelscope", RepoId: "Qwen/Qwen3-0.6B", Revision: "master"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reply.GetModel().GetId() != "model-1" || reply.GetVersion().GetId() != "version-1" || reply.GetTask().GetTaskId() != "task-1" {
		t.Fatalf("unexpected bundle response: %#v", reply)
	}
}

func TestModelServiceForwardsImportTaskLogsIdentityAndMapsReply(t *testing.T) {
	for _, tc := range []struct {
		name      string
		subject   auth.SubjectType
		actor     string
		status    string
		logs      string
		available bool
	}{
		{"user running logs", auth.SubjectUser, "governance:user:7", "running", "line 1\nline 2\n", true},
		{"API key pending logs", auth.SubjectAPIKey, "governance:access-key:7", "pending", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &modelOwnerExtraProbe{logs: func(_ context.Context, tenant, actor string, req *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error) {
				calls++
				if tenant != "11111111-1111-4111-8111-111111111111" || actor != tc.actor || req.GetTaskId() != "task-1" {
					t.Fatalf("unexpected logs delegation: tenant=%q actor=%q req=%#v", tenant, actor, req)
				}
				return &modelv1.GetImportTaskLogsResponse{TaskId: "task-1", Status: tc.status, Logs: tc.logs, Available: tc.available}, nil
			}}
			s := NewModelService(client, modelTenantProbe{})
			ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: tc.subject})
			reply, err := s.GetImportTaskLogs(ctx, &catalogv1.GetImportTaskLogsRequest{TaskId: "task-1"})
			if err != nil {
				t.Fatal(err)
			}
			if reply.GetTaskId() != "task-1" || reply.GetStatus() != tc.status || reply.GetLogs() != tc.logs || reply.GetAvailable() != tc.available || calls != 1 {
				t.Fatalf("unexpected logs response: %#v calls=%d", reply, calls)
			}
		})
	}
}

func TestModelServiceMapsCleanedUpImportTaskLogsToConflict(t *testing.T) {
	client := &modelOwnerExtraProbe{logs: func(context.Context, string, string, *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error) {
		return nil, kratoserrors.Conflict("IMPORT_LOGS_UNAVAILABLE", "private cleanup detail")
	}}
	s := NewModelService(client, modelTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	reply, err := s.GetImportTaskLogs(ctx, &catalogv1.GetImportTaskLogsRequest{TaskId: "task-1"})
	publicErr := kratoserrors.FromError(err)
	if reply != nil || publicErr == nil || publicErr.Code != 409 || publicErr.Reason != "IMPORT_LOGS_UNAVAILABLE" || publicErr.Message != "import task logs are no longer available" {
		t.Fatalf("unexpected cleanup response: reply=%#v error=%v", reply, err)
	}
}

func TestModelServiceRejectsInvalidImportTaskLogsRequest(t *testing.T) {
	authenticated := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		request *catalogv1.GetImportTaskLogsRequest
		code    int
		reason  string
	}{
		{"nil request", authenticated, nil, 400, "INVALID_MODEL_IMPORT"},
		{"empty task ID", authenticated, &catalogv1.GetImportTaskLogsRequest{}, 400, "INVALID_MODEL_IMPORT"},
		{"blank task ID", authenticated, &catalogv1.GetImportTaskLogsRequest{TaskId: " \t\n"}, 400, "INVALID_MODEL_IMPORT"},
		{"missing identity", context.Background(), &catalogv1.GetImportTaskLogsRequest{TaskId: "task-1"}, 401, "INVALID_LOGIN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &modelOwnerExtraProbe{logs: func(context.Context, string, string, *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error) {
				t.Fatal("invalid request reached Model")
				return nil, nil
			}}
			reply, err := NewModelService(client, modelTenantProbe{}).GetImportTaskLogs(tc.ctx, tc.request)
			if reply != nil || err == nil || kratoserrors.Code(err) != tc.code || kratoserrors.Reason(err) != tc.reason {
				t.Fatalf("unexpected invalid request result: reply=%#v error=%v", reply, err)
			}
		})
	}
}

func TestModelServiceRejectsInvalidImportTaskLogsReply(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply *modelv1.GetImportTaskLogsResponse
	}{
		{"nil reply", nil},
		{"empty task ID", &modelv1.GetImportTaskLogsResponse{Logs: "private logs"}},
		{"blank task ID", &modelv1.GetImportTaskLogsResponse{TaskId: " \t", Logs: "private logs"}},
		{"different task ID", &modelv1.GetImportTaskLogsResponse{TaskId: "another-task", Logs: "private logs", Available: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &modelOwnerExtraProbe{logs: func(context.Context, string, string, *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error) {
				return tc.reply, nil
			}}
			ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
			reply, err := NewModelService(client, modelTenantProbe{}).GetImportTaskLogs(ctx, &catalogv1.GetImportTaskLogsRequest{TaskId: "task-1"})
			publicErr := kratoserrors.FromError(err)
			if reply != nil || publicErr == nil || publicErr.Code != 503 || publicErr.Reason != "MODEL_INVALID_RESPONSE" || publicErr.Message != "invalid Model response" {
				t.Fatalf("invalid reply was exposed: reply=%#v error=%v", reply, err)
			}
		})
	}
}

func TestModelServiceForwardsExternalSearchIdentityAndFilters(t *testing.T) {
	client := &modelOwnerExtraProbe{search: func(_ context.Context, tenant, actor string, req *modelv1.SearchExternalModelsRequest) (*modelv1.SearchExternalModelsResponse, error) {
		if tenant != "11111111-1111-4111-8111-111111111111" || actor != "governance:user:7" || req.Source != "modelscope" || req.Keyword != "qwen" || req.Owner != "Qwen" || req.Page == nil || *req.Page != 1 || req.PageSize == nil || *req.PageSize != 10 {
			t.Fatalf("unexpected search delegation: tenant=%q actor=%q req=%#v", tenant, actor, req)
		}
		return &modelv1.SearchExternalModelsResponse{Items: []*modelv1.ExternalModelCandidate{{Source: "modelscope", RepoId: "Qwen/Qwen2-0.5B", Name: "Qwen2-0.5B", Revision: "master"}}, HasMore: true}, nil
	}}
	s := NewModelService(client, modelTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{ID: 7, TenantID: 42, Type: auth.SubjectUser})
	page, pageSize := int32(1), int32(10)
	reply, err := s.SearchExternalModels(ctx, &catalogv1.SearchExternalModelsRequest{Source: "modelscope", Keyword: "qwen", Owner: "Qwen", Page: &page, PageSize: &pageSize})
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.GetItems()) != 1 || reply.GetItems()[0].GetRepoId() != "Qwen/Qwen2-0.5B" || !reply.GetHasMore() {
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
