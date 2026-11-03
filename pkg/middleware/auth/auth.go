package auth

import (
	"context"
	"github.com/go-kratos/kratos/v2/errors"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	http "github.com/go-kratos/kratos/v2/transport/http"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go.opentelemetry.io/otel/trace"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"

	authnEngine "go-wind-admin/pkg/localdeps/kratos-authn/engine"
	authzEngine "go-wind-admin/pkg/localdeps/kratos-authz/engine"

	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/metadata"
)

var defaultAction = authzEngine.Action("ANY")

// Server 衔接认证和鉴权
func Server(opts ...Option) middleware.Middleware {
	op := options{
		log: bLogger.NewHelper(bLogger.GetLogger().With("module", "auth/middleware")),

		injectOperatorId: false,
		injectTenantId:   false,
		enableAuthz:      true,
		injectEnt:        true,
		injectMetadata:   true,
	}
	for _, o := range opts {
		o(&op)
	}

	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				op.log.Errorf(ctx, "auth middleware: missing transport in context")
				return nil, ErrWrongContext
			}

			var tokenPayload *authenticationV1.UserTokenPayload
			var principal *Principal
			var err error
			htr, isHTTP := tr.(*http.Transport)
			if isHTTP && hasSigningHeaders(htr.Request()) {
				if len(htr.Request().Header.Values("Authorization")) > 0 {
					return nil, errors.BadRequest("MIXED_AUTHENTICATION", "choose one authentication method")
				}
				principal, err = verifySignature(ctx, htr.Request(), tr.Operation(), op.signingKeyStore, time.Now())
				if principal != nil {
					ctx = NewPrincipalContext(ctx, principal)
				}
				if err != nil {
					return nil, err
				}
				tokenPayload = &authenticationV1.UserTokenPayload{TenantId: &principal.TenantID, Roles: principal.Roles}
				ctx = NewPrincipalContext(ctx, principal)
				ctx = NewContext(ctx, tokenPayload)
			} else {
				if isHTTP && len(htr.Request().Header.Values("Authorization")) != 1 {
					return nil, ErrMissingBearerToken
				}
				if isHTTP {
					fields := strings.Fields(htr.Request().Header.Get("Authorization"))
					if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") && strings.HasPrefix(fields[1], SandboxTokenPrefix) {
						if len(fields[1]) > 4096 {
							return nil, errors.Unauthorized("SANDBOX_TOKEN_INVALID", "invalid sandbox token")
						}
						sandboxID, scope, routeErr := sandboxTokenRoute(htr.Request(), tr.Operation())
						if routeErr != nil {
							return nil, routeErr
						}
						if op.sandboxTokenVerifier == nil || op.tenantAccessChecker == nil {
							return nil, errors.ServiceUnavailable("SANDBOX_TOKEN_UNAVAILABLE", "sandbox token validation unavailable")
						}
						subject, verifyErr := op.sandboxTokenVerifier.VerifySandboxToken(ctx, fields[1], sandboxID, scope)
						if verifyErr != nil {
							return nil, verifyErr
						}
						if !SandboxTokenMatches(subject, sandboxID, scope) {
							return nil, errors.Unauthorized("SANDBOX_TOKEN_INVALID", "invalid sandbox token")
						}
						ctx = NewPrincipalContext(ctx, subject)
						// Tenant-only viewer: it carries no user identity or roles.
						// The handler separately checks the issuer's current grant.
						if op.injectEnt {
							ctx = viewer.WithContext(ctx, appViewer.NewUserViewer(0, uint64(subject.TenantID), 0, "", nil))
						}
						if err = op.tenantAccessChecker.CheckTenantAccess(ctx, subject.TenantID, htr.PathTemplate(), htr.Request().Method); err != nil {
							return nil, err
						}
						op.log.Infof(ctx, "sandbox token validated tenant=%d sandbox=%s token_id=%s issuer=%d scope=%s", subject.TenantID, subject.SandboxID, subject.SandboxTokenID, subject.IssuerID, scope)
						return handler(ctx, req)
					}
				}
				token, tokenErr := authnEngine.AuthFromMD(ctx, authnEngine.BearerWord, authnEngine.ContextTypeKratosMetaData)
				if tokenErr != nil {
					return nil, ErrMissingBearerToken
				}
				if op.accessTokenChecker == nil {
					return nil, ErrAccessTokenCheckerNotConfigured
				}
				valid, payload := op.accessTokenChecker.IsValidAccessToken(ctx, token, false)
				if !valid || payload == nil || payload.GetUserId() == 0 {
					return nil, ErrAccessTokenExpired
				}
				tokenPayload = payload
				ctx = NewContext(ctx, tokenPayload)
				principal, _ = PrincipalFromContext(ctx)
				if isHTTP && (IsNetworkOperation(tr.Operation()) || IsPlatformNetworkOperation(tr.Operation())) {
					if err := ValidateNetworkRequest(htr.Request(), tr.Operation()); err != nil {
						return nil, err
					}
				}
			}

			if op.injectOperatorId && principal.Type == SubjectUser {
				if err = setRequestOperationId(req, tokenPayload); err != nil {
					op.log.Errorf(ctx, "auth middleware: invalid token payload in context [%s]", err.Error())
					return nil, err
				}
			}
			if op.injectTenantId {
				if err = setRequestTenantId(req, tokenPayload); err != nil {
					op.log.Errorf(ctx, "auth middleware: invalid token payload in context [%s]", err.Error())
					return nil, err
				}
			}

			if op.injectEnt {
				var traceID string
				spanContext := trace.SpanContextFromContext(ctx)
				if spanContext.HasTraceID() {
					traceID = spanContext.TraceID().String()
				}

				userViewer := appViewer.NewUserViewer(
					uint64(tokenPayload.GetUserId()),
					uint64(tokenPayload.GetTenantId()),
					uint64(tokenPayload.GetOrgUnitId()),
					traceID,
					appViewer.BuildDataScopes(
						tokenPayload.GetDataScopes(),
						tokenPayload.GetDataScopeUnitIds(),
						tokenPayload.GetDataScope(),
					),
				)
				ctx = viewer.WithContext(ctx, userViewer)
			}

			if op.injectMetadata && principal.Type == SubjectUser {
				ctx, err = metadata.NewContext(ctx,
					&authenticationV1.OperatorMetadata{
						UserId:           uint64(tokenPayload.GetUserId()),
						TenantId:         uint64(tokenPayload.GetTenantId()),
						OrgUnitId:        uint64(tokenPayload.GetOrgUnitId()),
						DataScope:        tokenPayload.GetDataScope(),
						DataScopes:       tokenPayload.GetDataScopes(),
						DataScopeUnitIds: tokenPayload.GetDataScopeUnitIds(),
					},
				)
				if err != nil {
					op.log.Errorf(ctx, "auth middleware: invalid token payload in context [%s]", err.Error())
					return nil, err
				}
			}

			// 租户级访问检查：仅对租户用户（tenantId>0）生效，平台管理员（tenantId=0）直接放行。
			// 检查项：租户状态（OFF/EXPIRED/FREEZE 拒绝）、到期只读策略（仅放行 GET/HEAD/OPTIONS）、
			// 套餐模块白名单（请求所属业务模块不在白名单则拒绝）。
			if op.tenantAccessChecker != nil && tokenPayload.GetTenantId() > 0 {
				var path, method string
				if htr, ok := tr.(*http.Transport); ok {
					path = htr.PathTemplate()
					method = htr.Request().Method
				} else {
					path = tr.Operation()
					method = ""
				}
				var accessErr error
				if checker, ok := op.tenantAccessChecker.(TenantRequestAccessChecker); ok {
					accessErr = checker.CheckTenantRequestAccess(ctx, tokenPayload.GetTenantId(), path, method, req)
				} else {
					accessErr = op.tenantAccessChecker.CheckTenantAccess(ctx, tokenPayload.GetTenantId(), path, method)
				}
				if accessErr != nil {
					return nil, accessErr
				}
			}

			if op.enableAuthz {
				ctx, err = processAuthz(ctx, tr, tokenPayload)
				if err != nil {
					op.log.Errorf(ctx, "auth middleware: invalid token payload in context [%s]", err.Error())
					return nil, err
				}
			}

			return handler(ctx, req)
		}
	}
}
