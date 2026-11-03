package data

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"go-wind-admin/pkg/middleware/auth"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// SandboxClient is intentionally carried by InstanceClient so all Resource
// calls share the single mTLS connection created by NewResourceClientsFromEnv.
type SandboxClient struct{ client pb.SandboxServiceClient }

func sandboxCall[T any](c *InstanceClient, ctx context.Context, tenant, actor string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.sandbox == nil {
		return zero, status.Error(codes.Unavailable, "SANDBOX_ADAPTER_UNAVAILABLE")
	}
	if !validResourceIdentity(tenant, actor) && !validSandboxTokenIdentity(tenant, actor) {
		return zero, status.Error(codes.Unauthenticated, "INVALID_SANDBOX_CALLER")
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return fn(call)
}

func (c *InstanceClient) ResolveSandbox(ctx context.Context, tenant, actor string, r *pb.ResolveCreateSpecRequest) (*pb.ResolveCreateSpecResponse, error) {
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.ResolveCreateSpecResponse, error) {
		return c.sandbox.ResolveCreateSpec(call, r)
	})
}

func (c *InstanceClient) AcceptSandboxCreate(ctx context.Context, tenant, actor string, r *pb.AcceptCreateRequest) (*pb.AcceptCreateResponse, error) {
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.AcceptCreateResponse, error) {
		return c.sandbox.AcceptCreate(call, r)
	})
}

func (c *InstanceClient) GetSandbox(ctx context.Context, tenant, actor string, r *pb.GetSandboxRequest) (*pb.GetSandboxResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.GetSandboxResponse, error) { return c.sandbox.GetSandbox(call, r) })
}

func (c *InstanceClient) ListSandboxes(ctx context.Context, tenant, actor string, r *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.ListSandboxesResponse, error) { return c.sandbox.ListSandboxes(call, r) })
}

func (c *InstanceClient) GetSandboxOperation(ctx context.Context, tenant, actor string, r *pb.GetOperationRequest) (*pb.GetOperationResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.GetOperationResponse, error) { return c.sandbox.GetOperation(call, r) })
}

func (c *InstanceClient) PauseSandbox(ctx context.Context, tenant, actor string, r *pb.PauseRequest) (*pb.PauseResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.PauseResponse, error) { return c.sandbox.Pause(call, r) })
}

func (c *InstanceClient) ResumeSandbox(ctx context.Context, tenant, actor string, r *pb.ResumeRequest) (*pb.ResumeResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.ResumeResponse, error) { return c.sandbox.Resume(call, r) })
}

func (c *InstanceClient) DeleteSandbox(ctx context.Context, tenant, actor string, r *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.DeleteResponse, error) { return c.sandbox.Delete(call, r) })
}

func (c *InstanceClient) RunSandboxCode(ctx context.Context, tenant, actor string, r *pb.RunCodeRequest) (*pb.CodeRun, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	response, err := sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.RunCodeResponse, error) { return c.sandbox.RunCode(call, r) })
	if err != nil {
		return nil, err
	}
	return response.GetRun(), nil
}

func (c *InstanceClient) GetSandboxCodeRun(ctx context.Context, tenant, actor string, r *pb.GetCodeRunRequest) (*pb.CodeRun, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	response, err := sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.GetCodeRunResponse, error) { return c.sandbox.GetCodeRun(call, r) })
	if err != nil {
		return nil, err
	}
	return response.GetRun(), nil
}

func (c *InstanceClient) QuerySandboxFiles(ctx context.Context, tenant, actor string, r *pb.QueryFilesRequest) (*pb.FileResult, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	v, e := sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.QueryFilesResponse, error) { return c.sandbox.QueryFiles(call, r) })
	if e != nil {
		return nil, e
	}
	return v.GetResult(), nil
}
func (c *InstanceClient) MutateSandboxFiles(ctx context.Context, tenant, actor string, r *pb.MutateFilesRequest) (*pb.FileOperation, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	v, e := sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.MutateFilesResponse, error) { return c.sandbox.MutateFiles(call, r) })
	if e != nil {
		return nil, e
	}
	return v.GetOperation(), nil
}
func (c *InstanceClient) GetSandboxFileOperation(ctx context.Context, tenant, actor string, r *pb.GetFileOperationRequest) (*pb.FileOperation, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	v, e := sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.GetFileOperationResponse, error) {
		return c.sandbox.GetFileOperation(call, r)
	})
	if e != nil {
		return nil, e
	}
	return v.GetOperation(), nil
}

func validSandboxTokenIdentity(tenant, actor string) bool {
	tid, e := uuid.Parse(tenant)
	if e != nil || tid == uuid.Nil || tid.String() != tenant {
		return false
	}
	const prefix = "governance:sandbox-token:"
	if !strings.HasPrefix(actor, prefix) {
		return false
	}
	id, e := uuid.Parse(strings.TrimPrefix(actor, prefix))
	return e == nil && id != uuid.Nil && actor == prefix+id.String()
}
func (c *InstanceClient) CreateSandboxToken(ctx context.Context, tenant, actor string, r *pb.CreateTokenRequest) (*pb.SandboxAccessToken, error) {
	if r == nil || r.TenantId != tenant || !validResourceIdentity(tenant, actor) {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	v, e := sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.CreateTokenResponse, error) { return c.sandbox.CreateToken(call, r) })
	if e != nil {
		return nil, e
	}
	return v.GetToken(), nil
}
func (c *InstanceClient) RevokeSandboxToken(ctx context.Context, tenant, actor string, r *pb.RevokeTokenRequest) error {
	if r == nil || r.TenantId != tenant || !validResourceIdentity(tenant, actor) {
		return status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	_, e := sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.RevokeTokenResponse, error) { return c.sandbox.RevokeToken(call, r) })
	return e
}

// The payload is untrusted here. It selects a tenant and token ID only for the
// authenticated Resource verification call; no authorization uses it directly.
func (c *InstanceClient) VerifySandboxToken(ctx context.Context, value, sandboxID, scope string) (*auth.Principal, error) {
	denied := func() (*auth.Principal, error) {
		return nil, status.Error(codes.Unauthenticated, "SANDBOX_TOKEN_INVALID")
	}
	if c == nil || c.sandbox == nil {
		return nil, status.Error(codes.Unavailable, "SANDBOX_TOKEN_UNAVAILABLE")
	}
	if len(value) > 4096 || !strings.HasPrefix(value, auth.SandboxTokenPrefix) {
		return denied()
	}
	body, _, ok := strings.Cut(strings.TrimPrefix(value, auth.SandboxTokenPrefix), ".")
	if !ok {
		return denied()
	}
	raw, e := base64.RawURLEncoding.Strict().DecodeString(body)
	if e != nil || len(raw) > 2048 {
		return denied()
	}
	var hint struct {
		Tenant  string `json:"tid"`
		Sandbox string `json:"sid"`
		ID      string `json:"jti"`
	}
	if json.Unmarshal(raw, &hint) != nil || hint.Sandbox != sandboxID {
		return denied()
	}
	actor := "governance:sandbox-token:" + hint.ID
	if !validSandboxTokenIdentity(hint.Tenant, actor) {
		return denied()
	}
	r, e := sandboxCall(c, ctx, hint.Tenant, actor, func(call context.Context) (*pb.ValidateTokenResponse, error) {
		return c.sandbox.ValidateToken(call, &pb.ValidateTokenRequest{TenantId: hint.Tenant, SandboxId: sandboxID, Value: value, RequiredScope: scope})
	})
	if e != nil {
		return nil, e
	}
	if r == nil || r.TenantId != hint.Tenant || r.SandboxId != sandboxID || r.TokenId != hint.ID || r.GovTenantId == 0 || r.IssuerId == 0 || r.ExpiresAt <= time.Now().Unix() {
		return denied()
	}
	return &auth.Principal{Type: auth.SubjectSandbox, TenantID: r.GovTenantId, IssuerID: r.IssuerId, SandboxID: r.SandboxId, SandboxTokenID: r.TokenId, SandboxExpiresAt: r.ExpiresAt, ResourceTenantID: r.TenantId, SandboxScopes: append([]string(nil), r.Scopes...)}, nil
}
