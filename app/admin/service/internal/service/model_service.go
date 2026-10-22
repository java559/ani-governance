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
	"google.golang.org/protobuf/types/known/emptypb"
)

// ModelOwnerClient is the only downstream surface exposed to the BFF. It
// receives tenant and actor values resolved from the authenticated principal,
// never values supplied by a public request.
type ModelOwnerClient interface {
	CreateModel(context.Context, string, string, *modelv1.CreateModelRequest) (*modelv1.Model, error)
	GetModel(context.Context, string, string, *modelv1.GetModelRequest) (*modelv1.Model, error)
	ListModels(context.Context, string, string, *modelv1.ListModelsRequest) (*modelv1.ListModelsResponse, error)
	SearchExternalModels(context.Context, string, string, *modelv1.SearchExternalModelsRequest) (*modelv1.SearchExternalModelsResponse, error)
	DeleteModel(context.Context, string, string, *modelv1.DeleteModelRequest) (*emptypb.Empty, error)
	CreateModelVersion(context.Context, string, string, *modelv1.CreateModelVersionRequest) (*modelv1.ModelVersion, error)
	GetModelVersion(context.Context, string, string, *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error)
	ListModelVersions(context.Context, string, string, *modelv1.ListModelVersionsRequest) (*modelv1.ListModelVersionsResponse, error)
	DeleteModelVersion(context.Context, string, string, *modelv1.DeleteModelVersionRequest) (*modelv1.DeleteModelVersionResponse, error)
	GetUploadURL(context.Context, string, string, *modelv1.GetUploadURLRequest) (*modelv1.GetUploadURLResponse, error)
	ImportModel(context.Context, string, string, *modelv1.ImportModelRequest) (*modelv1.ImportTask, error)
	ImportModelBundle(context.Context, string, string, *modelv1.ImportModelBundleRequest) (*modelv1.ImportModelBundleResponse, error)
	GetImportTask(context.Context, string, string, *modelv1.GetImportTaskRequest) (*modelv1.GetImportTaskResponse, error)
	GetImportTaskLogs(context.Context, string, string, *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error)
	RetryImportTask(context.Context, string, string, *modelv1.RetryImportTaskRequest) (*modelv1.RetryImportTaskResponse, error)
	GetModelDownloadURL(context.Context, string, string, *modelv1.GetModelDownloadURLRequest) (*modelv1.GetModelDownloadURLResponse, error)
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
	if in == nil || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_REQUEST", "model name and idempotency_key are required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	d := in
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
	models, err := s.client.ListModels(ctx, tenant, actor, &modelv1.ListModelsRequest{Status: in.Status, Page: in.Page, PageSize: in.PageSize, Keyword: in.Keyword, Source: in.Source, Capability: in.Capability})
	if err != nil {
		return nil, mapModelError(err)
	}
	if models == nil || len(models.Items) > 100 {
		return nil, modelInvalidReply()
	}
	out := &catalogv1.ListModelsResponse{Items: make([]*catalogv1.Model, 0, len(models.Items)), Total: models.Total}
	for _, item := range models.Items {
		if err := validateModelReply(item, tenant, ""); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, wireModel(item))
	}
	return out, nil
}

func (s *ModelService) SearchExternalModels(ctx context.Context, in *catalogv1.SearchExternalModelsRequest) (*catalogv1.SearchExternalModelsResponse, error) {
	if in == nil || strings.TrimSpace(in.Source) == "" || strings.TrimSpace(in.Keyword) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_SEARCH", "source and keyword are required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.SearchExternalModels(ctx, tenant, actor, &modelv1.SearchExternalModelsRequest{
		Source: in.Source, Keyword: in.Keyword, Owner: in.Owner, Task: in.Task, Library: in.Library, Page: in.Page, PageSize: in.PageSize,
	})
	if err != nil {
		return nil, mapModelError(err)
	}
	if reply == nil || len(reply.Items) > 50 {
		return nil, modelInvalidReply()
	}
	out := &catalogv1.SearchExternalModelsResponse{Items: make([]*catalogv1.ExternalModelCandidate, 0, len(reply.Items)), HasMore: reply.HasMore}
	for _, candidate := range reply.Items {
		if candidate == nil || strings.TrimSpace(candidate.Source) == "" || strings.TrimSpace(candidate.RepoId) == "" || strings.TrimSpace(candidate.Name) == "" || strings.TrimSpace(candidate.Revision) == "" {
			return nil, modelInvalidReply()
		}
		out.Items = append(out.Items, &catalogv1.ExternalModelCandidate{
			Source: candidate.Source, RepoId: candidate.RepoId, Name: candidate.Name, DisplayName: candidate.DisplayName,
			Description: candidate.Description, Revision: candidate.Revision, SizeBytes: candidate.SizeBytes,
			Tags: append([]string(nil), candidate.Tags...), UpdatedAt: candidate.UpdatedAt,
		})
	}
	return out, nil
}

func (s *ModelService) DeleteModel(ctx context.Context, in *catalogv1.DeleteModelRequest) (*emptypb.Empty, error) {
	if in == nil || strings.TrimSpace(in.ModelId) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_REQUEST", "model_id is required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.client.DeleteModel(ctx, tenant, actor, &modelv1.DeleteModelRequest{ModelId: in.ModelId}); err != nil {
		return nil, mapModelError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *ModelService) CreateModelVersion(ctx context.Context, in *catalogv1.CreateModelVersionRequest) (*catalogv1.CreateModelVersionResponse, error) {
	if in == nil || strings.TrimSpace(in.ModelId) == "" || strings.TrimSpace(in.Version) == "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_VERSION", "model_id, version and idempotency_key are required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	d := in
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
	reply, err := s.client.ListModelVersions(ctx, tenant, actor, &modelv1.ListModelVersionsRequest{ModelId: in.ModelId, Page: in.Page, PageSize: in.PageSize, Status: in.Status})
	if err != nil {
		return nil, mapModelError(err)
	}
	if reply == nil || len(reply.Items) > 100 {
		return nil, modelInvalidReply()
	}
	out := &catalogv1.ListModelVersionsResponse{Items: make([]*catalogv1.ModelVersion, 0, len(reply.Items)), Total: reply.Total}
	for _, version := range reply.Items {
		if err := validateModelVersionReply(version); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, wireModelVersion(version))
	}
	return out, nil
}

func (s *ModelService) DeleteModelVersion(ctx context.Context, in *catalogv1.DeleteModelVersionRequest) (*emptypb.Empty, error) {
	if in == nil || strings.TrimSpace(in.ModelVersionId) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_VERSION", "model_version_id is required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.client.DeleteModelVersion(ctx, tenant, actor, &modelv1.DeleteModelVersionRequest{ModelVersionId: in.ModelVersionId}); err != nil {
		return nil, mapModelError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *ModelService) GetUploadURL(ctx context.Context, in *catalogv1.GetUploadURLRequest) (*catalogv1.GetUploadURLResponse, error) {
	if in == nil || strings.TrimSpace(in.ModelId) == "" || in.Data == nil || strings.TrimSpace(in.Data.Version) == "" || strings.TrimSpace(in.Data.FileName) == "" || strings.TrimSpace(in.Data.IdempotencyKey) == "" || in.Data.SizeBytes <= 0 || strings.TrimSpace(in.Data.ChecksumSha256) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_UPLOAD", "model_id and upload metadata are required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	d := in.Data
	reply, err := s.client.GetUploadURL(ctx, tenant, actor, &modelv1.GetUploadURLRequest{
		ModelId: in.ModelId, Version: d.Version, FileName: d.FileName, SizeBytes: d.SizeBytes,
		IdempotencyKey: d.IdempotencyKey, ChecksumSha256: d.ChecksumSha256,
	})
	if err != nil {
		return nil, mapModelError(err)
	}
	if reply == nil || strings.TrimSpace(reply.UploadUrl) == "" || strings.TrimSpace(reply.StoragePath) == "" {
		return nil, modelInvalidReply()
	}
	return &catalogv1.GetUploadURLResponse{
		UploadUrl: reply.UploadUrl, StoragePath: reply.StoragePath, DocId: reply.DocId, ExpiresAt: reply.ExpiresAt,
		UploadHeaders: reply.UploadHeaders, ModelVersionId: reply.ModelVersionId,
	}, nil
}

func (s *ModelService) ImportModel(ctx context.Context, in *catalogv1.ImportModelRequest) (*catalogv1.ImportModelResponse, error) {
	if in == nil || strings.TrimSpace(in.Source) == "" || strings.TrimSpace(in.RepoId) == "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_IMPORT", "source, repo_id and idempotency_key are required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	d := in
	task, err := s.client.ImportModel(ctx, tenant, actor, &modelv1.ImportModelRequest{Source: d.Source, RepoId: d.RepoId, Revision: d.Revision, IdempotencyKey: d.IdempotencyKey, WebhookUrl: d.WebhookUrl, ModelId: d.ModelId, ModelVersionId: d.ModelVersionId})
	if err != nil {
		return nil, mapModelError(err)
	}
	if err := validateImportTask(task); err != nil {
		return nil, err
	}
	return &catalogv1.ImportModelResponse{Task: wireImportTask(task)}, nil
}

func (s *ModelService) ImportModelBundle(ctx context.Context, in *catalogv1.ImportModelBundleRequest) (*catalogv1.ImportModelBundleResponse, error) {
	if in == nil {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_IMPORT_BUNDLE", "request is required")
	}
	d := in
	if strings.TrimSpace(d.IdempotencyKey) == "" || d.Model == nil || strings.TrimSpace(d.Model.Name) == "" || len(d.Model.Capabilities) == 0 || d.Version == nil || strings.TrimSpace(d.Version.Version) == "" || strings.TrimSpace(d.Version.Format) == "" || strings.TrimSpace(d.Version.EngineType) == "" || strings.TrimSpace(d.Version.EngineImage) == "" || strings.TrimSpace(d.Version.StartupCommand) == "" || d.Source == nil || strings.TrimSpace(d.Source.Provider) == "" || strings.TrimSpace(d.Source.RepoId) == "" || strings.TrimSpace(d.Source.Revision) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_IMPORT_BUNDLE", "idempotency_key, model, version, source and complete runtime input are required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	v, src := d.Version, d.Source
	reply, err := s.client.ImportModelBundle(ctx, tenant, actor, &modelv1.ImportModelBundleRequest{
		IdempotencyKey: d.IdempotencyKey,
		Model: &modelv1.ImportModelBundleModelData{
			Name: d.Model.Name, DisplayName: d.Model.DisplayName, Description: d.Model.Description,
			Capabilities: append([]string(nil), d.Model.Capabilities...), ExternalModelId: d.Model.ExternalModelId,
		},
		Version: &modelv1.ImportModelBundleVersionData{
			Version: v.Version, Format: v.Format, EngineType: v.EngineType,
			EngineImage: v.EngineImage, StartupCommand: v.StartupCommand,
		},
		Source: &modelv1.ImportModelBundleSourceData{Provider: src.Provider, RepoId: src.RepoId, Revision: src.Revision},
	})
	if err != nil {
		return nil, mapModelError(err)
	}
	if reply == nil || validateModelReply(reply.Model, tenant, "") != nil || validateModelVersionReply(reply.Version) != nil || validateImportTask(reply.Task) != nil {
		return nil, modelInvalidReply()
	}
	return &catalogv1.ImportModelBundleResponse{Model: wireModel(reply.Model), Version: wireModelVersion(reply.Version), Task: wireImportTask(reply.Task)}, nil
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

func (s *ModelService) GetImportTaskLogs(ctx context.Context, in *catalogv1.GetImportTaskLogsRequest) (*catalogv1.GetImportTaskLogsResponse, error) {
	if in == nil || strings.TrimSpace(in.TaskId) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_IMPORT", "task_id is required")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetImportTaskLogs(ctx, tenant, actor, &modelv1.GetImportTaskLogsRequest{TaskId: in.TaskId})
	if err != nil {
		return nil, mapImportTaskLogsError(err)
	}
	if reply == nil || strings.TrimSpace(reply.TaskId) == "" || reply.TaskId != in.TaskId {
		return nil, modelInvalidReply()
	}
	return &catalogv1.GetImportTaskLogsResponse{TaskId: reply.TaskId, Status: reply.Status, Logs: reply.Logs, Available: reply.Available}, nil
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

func (s *ModelService) GetModelDownloadURL(ctx context.Context, in *catalogv1.GetModelDownloadURLRequest) (*catalogv1.GetModelDownloadURLResponse, error) {
	if in == nil || strings.TrimSpace(in.ModelVersionId) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_VERSION", "model_version_id is required")
	}
	if strings.TrimSpace(in.FilePath) != "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_DOWNLOAD", "file_path is not supported by the Model contract")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetModelDownloadURL(ctx, tenant, actor, &modelv1.GetModelDownloadURLRequest{ModelVersionId: in.ModelVersionId})
	if err != nil {
		return nil, mapModelError(err)
	}
	if reply == nil || strings.TrimSpace(reply.DownloadUrl) == "" || strings.TrimSpace(reply.StoragePath) == "" {
		return nil, modelInvalidReply()
	}
	return &catalogv1.GetModelDownloadURLResponse{DownloadUrl: reply.DownloadUrl, StoragePath: reply.StoragePath, IsEncrypted: reply.IsEncrypted, EncryptAlgo: reply.EncryptAlgo, EncryptHint: reply.EncryptHint, ExpiresAt: reply.ExpiresAt}, nil
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

func mapImportTaskLogsError(err error) error {
	modelErr := kratoserrors.FromError(err)
	if modelErr != nil && modelErr.Code == 409 && modelErr.Reason == "IMPORT_LOGS_UNAVAILABLE" {
		return kratoserrors.Conflict("IMPORT_LOGS_UNAVAILABLE", "import task logs are no longer available")
	}
	return mapModelError(err)
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
