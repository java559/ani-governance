package service

import (
	"context"
	"strings"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ModelOwnerClient is the only downstream surface exposed to the BFF. It
// receives tenant and actor values resolved from the authenticated principal,
// never values supplied by a public request.
type ModelOwnerClient interface {
	CreateModel(context.Context, string, string, *modelv1.CreateModelRequest) (*modelv1.Model, error)
	GetModel(context.Context, string, string, *modelv1.GetModelRequest) (*modelv1.Model, error)
	ListModels(context.Context, string, string, *modelv1.ListModelsRequest) (*modelv1.ListModelsResponse, error)
	CreateModelVersion(context.Context, string, string, *modelv1.CreateModelVersionRequest) (*modelv1.ModelVersion, error)
	GetModelVersion(context.Context, string, string, *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error)
	ListModelVersions(context.Context, string, string, *modelv1.ListModelVersionsRequest) (*modelv1.ListModelVersionsResponse, error)
	ImportModel(context.Context, string, string, *modelv1.ImportModelRequest) (*modelv1.ImportTask, error)
	GetImportTask(context.Context, string, string, *modelv1.GetImportTaskRequest) (*modelv1.GetImportTaskResponse, error)
	RetryImportTask(context.Context, string, string, *modelv1.RetryImportTaskRequest) (*modelv1.RetryImportTaskResponse, error)
}

type ModelService struct {
	adminv1.UnimplementedModelServiceServer
	client  ModelOwnerClient
	tenants ResourceTenantResolver
}

func NewModelService(client ModelOwnerClient, tenants ResourceTenantResolver) *ModelService {
	return &ModelService{client: client, tenants: tenants}
}

func (s *ModelService) trustedModelOperator(ctx context.Context) (string, string, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return "", "", kratoserrors.Unauthorized("INVALID_LOGIN", "login required")
	}
	if p.TenantID == 0 {
		return "", "", kratoserrors.Forbidden("TENANT_REQUIRED", "tenant identity required")
	}
	if s == nil || s.client == nil || s.tenants == nil {
		return "", "", kratoserrors.ServiceUnavailable("MODEL_UNAVAILABLE", "Model access is not configured")
	}
	actor, err := p.Actor()
	if err != nil {
		return "", "", kratoserrors.Unauthorized("INVALID_LOGIN", "login required")
	}
	tenant, err := s.tenants.ResourceTenantID(ctx, p.TenantID)
	if err != nil || strings.TrimSpace(tenant) == "" {
		return "", "", kratoserrors.ServiceUnavailable("TENANT_MAPPING_UNAVAILABLE", "tenant mapping unavailable")
	}
	return tenant, actor, nil
}

func (s *ModelService) CreateModel(ctx context.Context, in *catalogv1.CreateModelRequest) (*catalogv1.CreateModelResponse, error) {
	if in == nil || in.Data == nil || strings.TrimSpace(in.Data.Name) == "" || strings.TrimSpace(in.Data.IdempotencyKey) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_REQUEST", "model name and idempotency_key are required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	d := in.Data
	model, err := s.client.CreateModel(ctx, tenant, actor, &modelv1.CreateModelRequest{TenantId: tenant, Name: d.Name, DisplayName: d.DisplayName, Description: d.Description, Capabilities: d.Capabilities, IdempotencyKey: d.IdempotencyKey, ModelId: d.ModelId})
	if err != nil {
		return nil, mapModelError(err)
	}
	if err := validateModelReply(model, tenant, ""); err != nil {
		return nil, err
	}
	return &catalogv1.CreateModelResponse{Model: wireModel(model)}, nil
}

func (s *ModelService) GetModel(ctx context.Context, in *catalogv1.GetModelRequest) (*catalogv1.GetModelResponse, error) {
	if in == nil || strings.TrimSpace(in.ModelId) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_REQUEST", "model_id is required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	model, err := s.client.GetModel(ctx, tenant, actor, &modelv1.GetModelRequest{ModelId: in.ModelId})
	if err != nil {
		return nil, mapModelError(err)
	}
	if err := validateModelReply(model, tenant, in.ModelId); err != nil {
		return nil, err
	}
	return &catalogv1.GetModelResponse{Model: wireModel(model)}, nil
}

func (s *ModelService) ListModels(ctx context.Context, in *catalogv1.ListModelsRequest) (*catalogv1.ListModelsResponse, error) {
	if in == nil {
		in = &catalogv1.ListModelsRequest{}
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	var page *modelv1.CursorPageRequest
	if in.Page != nil {
		page = &modelv1.CursorPageRequest{Limit: in.Page.Limit, Cursor: in.Page.Cursor}
	}
	models, err := s.client.ListModels(ctx, tenant, actor, &modelv1.ListModelsRequest{Status: in.Status, Page: page, Keyword: in.Keyword, Source: in.Source, Capability: in.Capability})
	if err != nil {
		return nil, mapModelError(err)
	}
	if models == nil || len(models.Models) > 100 {
		return nil, modelInvalidReply()
	}
	out := &catalogv1.ListModelsResponse{Models: make([]*catalogv1.Model, 0, len(models.Models))}
	for _, item := range models.Models {
		if err := validateModelReply(item, tenant, ""); err != nil {
			return nil, err
		}
		out.Models = append(out.Models, wireModel(item))
	}
	if models.Meta != nil {
		out.Meta = &catalogv1.CursorPageMeta{NextCursor: models.Meta.NextCursor, HasMore: models.Meta.HasMore}
	}
	return out, nil
}

func (s *ModelService) CreateModelVersion(ctx context.Context, in *catalogv1.CreateModelVersionRequest) (*catalogv1.CreateModelVersionResponse, error) {
	if in == nil || in.Data == nil || strings.TrimSpace(in.Data.ModelId) == "" || strings.TrimSpace(in.Data.Version) == "" || strings.TrimSpace(in.Data.IdempotencyKey) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_VERSION", "model_id, version and idempotency_key are required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	d := in.Data
	version, err := s.client.CreateModelVersion(ctx, tenant, actor, &modelv1.CreateModelVersionRequest{
		ModelId: d.ModelId, Version: d.Version, Format: d.Format, StoragePath: d.StoragePath,
		ChecksumSha256: d.ChecksumSha256, SizeBytes: d.SizeBytes, IsEncrypted: d.IsEncrypted,
		EncryptAlgo: d.EncryptAlgo, EncryptHint: d.EncryptHint, IdempotencyKey: d.IdempotencyKey,
		EngineType: d.EngineType, StartupCommand: d.StartupCommand, EngineImage: d.EngineImage,
	})
	if err != nil {
		return nil, mapModelError(err)
	}
	if err := validateModelVersionReply(version); err != nil {
		return nil, err
	}
	return &catalogv1.CreateModelVersionResponse{Version: wireModelVersion(version)}, nil
}

func (s *ModelService) GetModelVersion(ctx context.Context, in *catalogv1.GetModelVersionRequest) (*catalogv1.GetModelVersionResponse, error) {
	if in == nil || (strings.TrimSpace(in.ModelVersionId) == "" && (strings.TrimSpace(in.ModelId) == "" || strings.TrimSpace(in.Version) == "")) {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_VERSION", "model_version_id or model_id and version are required")
	}
	if strings.TrimSpace(in.ModelVersionId) != "" && (strings.TrimSpace(in.ModelId) != "" || strings.TrimSpace(in.Version) != "") {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_VERSION", "model_version_id cannot be combined with model_id/version")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	d := &modelv1.GetModelVersionRequest{ModelVersionId: in.ModelVersionId, ModelId: in.ModelId, Version: in.Version}
	reply, err := s.client.GetModelVersion(ctx, tenant, actor, d)
	if err != nil {
		return nil, mapModelError(err)
	}
	if reply == nil || reply.Model == nil || reply.Model.TenantId != tenant || reply.Version == nil {
		return nil, modelInvalidReply()
	}
	if err := validateModelVersionReply(reply.Version); err != nil {
		return nil, err
	}
	return &catalogv1.GetModelVersionResponse{Model: wireModel(reply.Model), Version: wireModelVersion(reply.Version)}, nil
}

func (s *ModelService) ListModelVersions(ctx context.Context, in *catalogv1.ListModelVersionsRequest) (*catalogv1.ListModelVersionsResponse, error) {
	if in == nil || strings.TrimSpace(in.ModelId) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_VERSION", "model_id is required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	var page *modelv1.CursorPageRequest
	if in.Page != nil {
		page = &modelv1.CursorPageRequest{Limit: in.Page.Limit, Cursor: in.Page.Cursor}
	}
	reply, err := s.client.ListModelVersions(ctx, tenant, actor, &modelv1.ListModelVersionsRequest{ModelId: in.ModelId, Page: page})
	if err != nil {
		return nil, mapModelError(err)
	}
	if reply == nil || len(reply.Versions) > 100 {
		return nil, modelInvalidReply()
	}
	out := &catalogv1.ListModelVersionsResponse{Versions: make([]*catalogv1.ModelVersion, 0, len(reply.Versions))}
	for _, version := range reply.Versions {
		if err := validateModelVersionReply(version); err != nil {
			return nil, err
		}
		out.Versions = append(out.Versions, wireModelVersion(version))
	}
	if reply.Meta != nil {
		out.Meta = &catalogv1.CursorPageMeta{NextCursor: reply.Meta.NextCursor, HasMore: reply.Meta.HasMore}
	}
	return out, nil
}

func (s *ModelService) ImportModel(ctx context.Context, in *catalogv1.ImportModelRequest) (*catalogv1.ImportModelResponse, error) {
	if in == nil || in.Data == nil || strings.TrimSpace(in.Data.Source) == "" || strings.TrimSpace(in.Data.RepoId) == "" || strings.TrimSpace(in.Data.IdempotencyKey) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_IMPORT", "source, repo_id and idempotency_key are required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	d := in.Data
	task, err := s.client.ImportModel(ctx, tenant, actor, &modelv1.ImportModelRequest{Source: d.Source, RepoId: d.RepoId, Revision: d.Revision, IdempotencyKey: d.IdempotencyKey, WebhookUrl: d.WebhookUrl, ModelId: d.ModelId, ModelVersionId: d.ModelVersionId})
	if err != nil {
		return nil, mapModelError(err)
	}
	if err := validateImportTask(task); err != nil {
		return nil, err
	}
	return &catalogv1.ImportModelResponse{Task: wireImportTask(task)}, nil
}

func (s *ModelService) GetImportTask(ctx context.Context, in *catalogv1.GetImportTaskRequest) (*catalogv1.GetImportTaskResponse, error) {
	if in == nil || strings.TrimSpace(in.TaskId) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_IMPORT", "task_id is required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetImportTask(ctx, tenant, actor, &modelv1.GetImportTaskRequest{TaskId: in.TaskId})
	if err != nil {
		return nil, mapModelError(err)
	}
	if reply == nil || validateImportTask(reply.Task) != nil {
		return nil, modelInvalidReply()
	}
	return &catalogv1.GetImportTaskResponse{Task: wireImportTask(reply.Task)}, nil
}

func (s *ModelService) RetryImportTask(ctx context.Context, in *catalogv1.RetryImportTaskRequest) (*catalogv1.RetryImportTaskResponse, error) {
	if in == nil || strings.TrimSpace(in.TaskId) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_IMPORT", "task_id is required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.RetryImportTask(ctx, tenant, actor, &modelv1.RetryImportTaskRequest{TaskId: in.TaskId})
	if err != nil {
		return nil, mapModelError(err)
	}
	if reply == nil || validateImportTask(reply.Task) != nil {
		return nil, modelInvalidReply()
	}
	return &catalogv1.RetryImportTaskResponse{Task: wireImportTask(reply.Task)}, nil
}

func validateModelReply(model *modelv1.Model, tenant, expectedID string) error {
	if model == nil || model.Id == "" || model.TenantId != tenant || (expectedID != "" && model.Id != expectedID && model.ModelId != expectedID) {
		return modelInvalidReply()
	}
	return nil
}

func validateImportTask(task *modelv1.ImportTask) error {
	if task == nil || strings.TrimSpace(task.TaskId) == "" {
		return modelInvalidReply()
	}
	return nil
}

func validateModelVersionReply(version *modelv1.ModelVersion) error {
	if version == nil || strings.TrimSpace(version.Id) == "" || strings.TrimSpace(version.Version) == "" {
		return modelInvalidReply()
	}
	return nil
}

func modelInvalidReply() error {
	return kratoserrors.ServiceUnavailable("MODEL_INVALID_RESPONSE", "invalid Model response")
}

func mapModelError(err error) error {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return kratoserrors.BadRequest("MODEL_INVALID_REQUEST", "invalid Model request")
	case codes.Unauthenticated:
		return kratoserrors.Unauthorized("MODEL_UNAUTHENTICATED", "Model authentication failed")
	case codes.PermissionDenied:
		return kratoserrors.Forbidden("MODEL_FORBIDDEN", "Model access denied")
	case codes.NotFound:
		return kratoserrors.NotFound("MODEL_NOT_FOUND", "Model resource not found")
	case codes.AlreadyExists:
		return kratoserrors.Conflict("MODEL_CONFLICT", "Model resource already exists")
	case codes.Canceled:
		return kratoserrors.New(499, "MODEL_CANCELED", "Model request canceled")
	case codes.DeadlineExceeded, codes.Unavailable:
		return kratoserrors.ServiceUnavailable("MODEL_UNAVAILABLE", "Model service unavailable")
	default:
		return kratoserrors.ServiceUnavailable("MODEL_UNAVAILABLE", "Model service unavailable")
	}
}

func wireModel(model *modelv1.Model) *catalogv1.Model {
	out := &catalogv1.Model{Id: model.Id, Name: model.Name, DisplayName: model.DisplayName, Description: model.Description, Source: model.Source, SourceRepoId: model.SourceRepoId, Capabilities: append([]string(nil), model.Capabilities...), Status: model.Status, ErrorMessage: model.ErrorMessage, TotalSizeBytes: model.TotalSizeBytes, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt, ModelId: model.ModelId}
	if len(model.Versions) != 0 {
		out.Versions = make([]*catalogv1.ModelVersion, 0, len(model.Versions))
		for _, version := range model.Versions {
			if version == nil {
				continue
			}
			out.Versions = append(out.Versions, wireModelVersion(version))
		}
	}
	return out
}

func wireModelVersion(version *modelv1.ModelVersion) *catalogv1.ModelVersion {
	return &catalogv1.ModelVersion{Id: version.Id, ModelId: version.ModelId, Version: version.Version, Format: version.Format, IsEncrypted: version.IsEncrypted, EncryptAlgo: version.EncryptAlgo, EncryptHint: version.EncryptHint, SizeBytes: version.SizeBytes, ChecksumSha256: version.ChecksumSha256, StoragePath: version.StoragePath, CreatedAt: version.CreatedAt, Status: version.Status, EngineType: version.EngineType, StartupCommand: version.StartupCommand, EngineImage: version.EngineImage}
}

func wireImportTask(task *modelv1.ImportTask) *catalogv1.ImportTask {
	return &catalogv1.ImportTask{TaskId: task.TaskId, TaskType: task.TaskType, Status: task.Status, LocationUrl: task.LocationUrl, ModelId: task.ModelId, ModelVersionId: task.ModelVersionId, AttemptCount: task.AttemptCount, ProgressPct: task.ProgressPct, ErrorMessage: task.ErrorMessage, CreatedAt: task.CreatedAt, CompletedAt: task.CompletedAt}
}
