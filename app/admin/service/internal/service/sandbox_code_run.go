package service

import (
	"context"
	"strings"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/v1"
	view "go-wind-admin/api/gen/go/sandbox/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
)

type sandboxCodeClient interface {
	RunSandboxCode(context.Context, string, string, *res.RunCodeRequest) (*res.CodeRun, error)
	GetSandboxCodeRun(context.Context, string, string, *res.GetCodeRunRequest) (*res.CodeRun, error)
}

func sandboxUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func sandboxCodeResponse(response *res.CodeRun, sandboxID, runID string) (*view.SandboxCodeRun, error) {
	if response == nil || response.SandboxId != sandboxID || response.RunId != runID {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_INVALID_RESPONSE", "invalid code-run response")
	}
	return &view.SandboxCodeRun{RunId: response.RunId, SandboxId: response.SandboxId, State: response.State,
		Stdout: strings.ToValidUTF8(string(response.Stdout), "\uFFFD"), Stderr: strings.ToValidUTF8(string(response.Stderr), "\uFFFD"),
		ExitCode: response.ExitCode, Truncated: response.Truncated, Replayed: response.Replayed, Reason: response.Reason}, nil
}

func (s *SandboxService) RunSandboxCode(ctx context.Context, request *view.RunSandboxCodeRequest) (*view.SandboxCodeRun, error) {
	if request == nil || request.Data == nil || !sandboxUUID(request.Data.SandboxId) || sandboxKey(request.Data.IdempotencyKey) != nil {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid code-run request")
	}
	tenant, actor, err := s.sandboxDataScope(ctx, data.SandboxCodeRunPath, "POST", request.Data.SandboxId)
	if err != nil {
		return nil, err
	}
	client, ok := s.client.(sandboxCodeClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_UNAVAILABLE", "sandbox code execution is not configured")
	}
	runID := stableSandboxID(tenant, request.Data.SandboxId+"/"+request.Data.IdempotencyKey, "code-run")
	response, err := client.RunSandboxCode(ctx, tenant, actor, &res.RunCodeRequest{
		TenantId: tenant, SandboxId: request.Data.SandboxId, RunId: runID, IdempotencyKey: request.Data.IdempotencyKey,
		Language: request.Data.Language, Code: request.Data.Code, Stdin: request.Data.Stdin, TimeoutSeconds: request.Data.TimeoutSeconds, ExpectedVersion: request.Data.ExpectedVersion,
	})
	if err != nil {
		return nil, err
	}
	result, err := sandboxCodeResponse(response, request.Data.SandboxId, runID)
	if err != nil {
		return nil, err
	}
	// A run grant does not imply permission to read a previous run's output.
	// Clients retrieve output through the separately authorized GET action.
	result.Stdout, result.Stderr = "", ""
	return result, nil
}

func (s *SandboxService) GetSandboxCodeRun(ctx context.Context, request *view.GetSandboxCodeRunRequest) (*view.SandboxCodeRun, error) {
	if request == nil || !sandboxUUID(request.SandboxId) || !sandboxUUID(request.RunId) {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid code-run identity")
	}
	tenant, actor, err := s.sandboxDataScope(ctx, data.SandboxCodeRunGetPath, "GET", request.SandboxId)
	if err != nil {
		return nil, err
	}
	client, ok := s.client.(sandboxCodeClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_UNAVAILABLE", "sandbox code execution is not configured")
	}
	response, err := client.GetSandboxCodeRun(ctx, tenant, actor, &res.GetCodeRunRequest{TenantId: tenant, SandboxId: request.SandboxId, RunId: request.RunId})
	if err != nil {
		return nil, err
	}
	return sandboxCodeResponse(response, request.SandboxId, request.RunId)
}
