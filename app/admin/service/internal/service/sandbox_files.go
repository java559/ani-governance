package service

import (
	"context"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/v1"
	view "go-wind-admin/api/gen/go/sandbox/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
)

type sandboxFileClient interface {
	QuerySandboxFiles(context.Context, string, string, *res.QueryFilesRequest) (*res.FileResult, error)
	MutateSandboxFiles(context.Context, string, string, *res.MutateFilesRequest) (*res.FileOperation, error)
	GetSandboxFileOperation(context.Context, string, string, *res.GetFileOperationRequest) (*res.FileOperation, error)
}

func sandboxFileResult(r *res.FileResult) (*view.SandboxFileResult, error) {
	if r == nil {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_INVALID_RESPONSE", "invalid file response")
	}
	out := &view.SandboxFileResult{Path: r.Path, Kind: r.Kind, SizeBytes: r.SizeBytes, ContentBase64: r.ContentBase64, Total: r.Total, NextCursor: r.NextCursor}
	for _, v := range r.Items {
		if v == nil {
			return nil, kratoserrors.ServiceUnavailable("SANDBOX_INVALID_RESPONSE", "invalid file response")
		}
		out.Items = append(out.Items, &view.SandboxFileEntry{Path: v.Path, Kind: v.Kind, SizeBytes: v.SizeBytes, UpdatedAtNs: v.UpdatedAtNs, UpdatedAt: time.Unix(0, v.UpdatedAtNs).UTC().Format(time.RFC3339Nano)})
	}
	return out, nil
}
func sandboxFileOperation(r *res.FileOperation, id, operation string) (*view.SandboxFileOperation, error) {
	if r == nil || r.SandboxId != id || r.OperationId != operation {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_INVALID_RESPONSE", "invalid file response")
	}
	if r.State == "failed" {
		switch r.Reason {
		case "conflict":
			return nil, kratoserrors.Conflict("SANDBOX_FILE_CONFLICT", "file already exists")
		case "not_found":
			return nil, kratoserrors.NotFound("SANDBOX_FILE_NOT_FOUND", "file not found")
		case "invalid":
			return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid file request")
		case "too_large":
			return nil, kratoserrors.New(413, "SANDBOX_FILE_TOO_LARGE", "file too large")
		}
	}
	result, err := sandboxFileResult(r.Result)
	if err != nil {
		return nil, err
	}
	return &view.SandboxFileOperation{OperationId: r.OperationId, SandboxId: r.SandboxId, State: r.State, Reason: r.Reason, Replayed: r.Replayed, Result: result}, nil
}
func (s *SandboxService) querySandboxFiles(ctx context.Context, id, path, action string, limit uint32, cursor, route string) (*view.SandboxFileResult, error) {
	if !sandboxUUID(id) {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid sandbox identity")
	}
	tenant, actor, err := s.sandboxDataScope(ctx, route, "GET", id)
	if err != nil {
		return nil, err
	}
	client, ok := s.client.(sandboxFileClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_UNAVAILABLE", "sandbox files not configured")
	}
	r, err := client.QuerySandboxFiles(ctx, tenant, actor, &res.QueryFilesRequest{TenantId: tenant, SandboxId: id, Action: action, Path: path, Limit: limit, Cursor: cursor})
	if err != nil {
		return nil, err
	}
	return sandboxFileResult(r)
}
func (s *SandboxService) ListSandboxFiles(ctx context.Context, r *view.ListSandboxFilesRequest) (*view.SandboxFileResult, error) {
	if r == nil {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid request")
	}
	return s.querySandboxFiles(ctx, r.SandboxId, r.Path, "list", r.Limit, r.Cursor, data.SandboxFilesListPath)
}
func (s *SandboxService) ReadSandboxFile(ctx context.Context, r *view.ReadSandboxFileRequest) (*view.SandboxFileResult, error) {
	if r == nil {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid request")
	}
	return s.querySandboxFiles(ctx, r.SandboxId, r.Path, "read", 0, "", data.SandboxFilesContentPath)
}
func (s *SandboxService) mutateSandboxFile(ctx context.Context, r *view.MutateSandboxFileData, action, method string) (*view.SandboxFileOperation, error) {
	if r == nil || !sandboxUUID(r.SandboxId) || sandboxKey(r.IdempotencyKey) != nil {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid file request")
	}
	tenant, actor, err := s.sandboxDataScope(ctx, data.SandboxFilesMutatePath, method, r.SandboxId)
	if err != nil {
		return nil, err
	}
	client, ok := s.client.(sandboxFileClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_UNAVAILABLE", "sandbox files not configured")
	}
	operation := stableSandboxID(tenant, r.SandboxId+"/"+r.IdempotencyKey, "file-operation")
	response, err := client.MutateSandboxFiles(ctx, tenant, actor, &res.MutateFilesRequest{TenantId: tenant, SandboxId: r.SandboxId, OperationId: operation, IdempotencyKey: r.IdempotencyKey, ExpectedVersion: r.ExpectedVersion, Action: action, Path: r.Path, ContentBase64: r.ContentBase64, Overwrite: r.Overwrite})
	if err != nil {
		return nil, err
	}
	return sandboxFileOperation(response, r.SandboxId, operation)
}
func (s *SandboxService) WriteSandboxFile(ctx context.Context, r *view.WriteSandboxFileRequest) (*view.SandboxFileOperation, error) {
	if r == nil {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid request")
	}
	return s.mutateSandboxFile(ctx, r.Data, "write", "POST")
}
func (s *SandboxService) DeleteSandboxFile(ctx context.Context, r *view.DeleteSandboxFileRequest) (*view.SandboxFileOperation, error) {
	if r == nil {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid request")
	}
	return s.mutateSandboxFile(ctx, r.Data, "delete", "DELETE")
}
func (s *SandboxService) GetSandboxFileOperation(ctx context.Context, r *view.GetSandboxFileOperationRequest) (*view.SandboxFileOperation, error) {
	if r == nil || !sandboxUUID(r.SandboxId) || !sandboxUUID(r.OperationId) {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid request")
	}
	tenant, actor, err := s.sandboxDataScope(ctx, data.SandboxFilesOperationPath, "GET", r.SandboxId)
	if err != nil {
		return nil, err
	}
	client, ok := s.client.(sandboxFileClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_UNAVAILABLE", "sandbox files not configured")
	}
	response, err := client.GetSandboxFileOperation(ctx, tenant, actor, &res.GetFileOperationRequest{TenantId: tenant, SandboxId: r.SandboxId, OperationId: r.OperationId})
	if err != nil {
		return nil, err
	}
	return sandboxFileOperation(response, r.SandboxId, r.OperationId)
}
