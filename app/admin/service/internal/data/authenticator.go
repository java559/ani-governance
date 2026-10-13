package data

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	"go-wind-admin/pkg/localdeps/go-utils/jwtutil"
	"go-wind-admin/pkg/localdeps/go-utils/trans"

	authnEngine "go-wind-admin/pkg/localdeps/kratos-authn/engine"
	authnJwt "go-wind-admin/pkg/localdeps/kratos-authn/engine/jwt"

	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"

	"go-wind-admin/pkg/jwt"
)

// devSamplePrivateKeyFingerprint 是 configs/auth.yaml 内置开发示例 RSA 私钥的
// PEM body 指纹（首行），用于检测示例密钥是否仍生效。替换 yaml 或用 env 注入
// 生产密钥后即不再命中。
const devSamplePrivateKeyFingerprint = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQCL8EdeTLDTf8AY"

// applyJwtKeyOverrides 环境变量密钥覆盖 + 开发示例密钥安全检查（见调用处注释）。
func applyJwtKeyOverrides(logHelper *bLogger.Helper, jwtCfg *conf.Authentication_Jwt) *conf.Authentication_Jwt {
	if jwtCfg == nil {
		return jwtCfg
	}
	if v := os.Getenv("GWA_AUTH_JWT_PRIVATE_KEY"); v != "" {
		jwtCfg.PrivateKey = trans.Ptr(v)
	}
	if v := os.Getenv("GWA_AUTH_JWT_PUBLIC_KEY"); v != "" {
		jwtCfg.PublicKey = trans.Ptr(v)
	}
	if v := os.Getenv("GWA_AUTH_JWT_KEY"); v != "" {
		jwtCfg.Key = v
	}

	if strings.Contains(jwtCfg.GetPrivateKey(), devSamplePrivateKeyFingerprint) {
		// 仅打醒目告警，不阻断启动——测试/本地环境普遍沿用示例密钥，
		// 强行 panic 会断掉开发与联调链路。生产替换由部署流程保证。
		logHelper.Warnf(context.Background(), "⚠️ configs/auth.yaml 中的开发示例 JWT 私钥仍在生效，生产环境必须替换："+
			"通过环境变量 GWA_AUTH_JWT_PRIVATE_KEY / GWA_AUTH_JWT_PUBLIC_KEY 注入，"+
			"或修改 yaml（生成命令见 auth.yaml 内注释）。")
	}
	return jwtCfg
}

const (
	// DefaultAccessTokenExpires  默认访问令牌过期时间（配置未指定时使用）
	DefaultAccessTokenExpires = time.Minute * 15

	// DefaultRefreshTokenExpires 默认刷新令牌过期时间（配置未指定时使用）
	DefaultRefreshTokenExpires = time.Hour * 24 * 7
)

type Authenticator struct {
	log *bLogger.Helper

	AdminAuthenticator authnEngine.Authenticator

	// jwtCfg 保留 JWT 配置，用于读取令牌过期时间。
	jwtCfg *conf.Authentication_Jwt

	userTokenCache *UserTokenCache
}

func NewAuthenticator(
	ctx *bootstrap.Context,
	userTokenCache *UserTokenCache,
) *Authenticator {
	cfg := ctx.GetConfig()
	if cfg == nil || cfg.Authn == nil {
		return nil
	}

	jwtCfg := cfg.Authn.GetJwt()

	logHelper := ctx.NewLoggerHelper("authenticator/data/authentication-service")

	// 密钥注入与安全检查：
	// 1) 环境变量优先于 yaml——生产环境应通过 GWA_AUTH_JWT_PRIVATE_KEY /
	//    GWA_AUTH_JWT_PUBLIC_KEY（非对称）或 GWA_AUTH_JWT_KEY（对称）注入密钥，
	//    避免 yaml 中的开发示例密钥被带入生产。
	// 2) 检测到开发示例私钥仍生效时打醒目告警（不阻断启动，避免断开发/测试链路）。
	jwtCfg = applyJwtKeyOverrides(logHelper, jwtCfg)

	a := Authenticator{
		log:            logHelper,
		jwtCfg:         jwtCfg,
		userTokenCache: userTokenCache,
	}

	adminAuth, err := newAdminAuthenticator(jwtCfg)
	if err != nil {
		// 启动期密钥/算法配置错误属于不可恢复故障，直接 panic 以暴露问题。
		panic(fmt.Sprintf("init admin authenticator failed: %v", err))
	}
	a.AdminAuthenticator = adminAuth

	return &a
}

// newAdminAuthenticator 根据配置构造 JWT 认证器，同时支持对称与非对称签名算法。
//   - 对称算法（HS256/HS384/HS512）：key 为共享秘钥。
//   - 非对称算法（RS256/RS384/RS512、PS256/PS384/PS512、ES256/...、EdDSA）：
//     private_key 用于签发，public_key 用于校验。若仅配置了 private_key（如本服务
//     既签发又校验的单体场景），则回退为从私钥派生公钥。
func newAdminAuthenticator(jwtCfg *conf.Authentication_Jwt) (authnEngine.Authenticator, error) {
	if jwtCfg == nil {
		return nil, fmt.Errorf("jwt config is nil")
	}

	method := jwtCfg.GetMethod()
	if method == "" {
		// 与底层库默认行为一致。
		method = "HS256"
	}

	opts := []authnJwt.Option{
		authnJwt.WithSigningMethod(method),
	}

	if isAsymmetricMethod(method) {
		// 非对称算法：优先使用独立配置的 private_key / public_key。
		switch {
		case jwtCfg.GetPrivateKey() != "" && jwtCfg.GetPublicKey() != "":
			opts = append(opts,
				authnJwt.WithPrivateKeyFromPEM([]byte(jwtCfg.GetPrivateKey())),
				authnJwt.WithPublicKeyFromPEM([]byte(jwtCfg.GetPublicKey())),
			)
		case jwtCfg.GetPrivateKey() != "":
			// 仅配置私钥：从私钥派生公钥（本服务既签发又校验的场景）。
			privPEM := []byte(jwtCfg.GetPrivateKey())
			publicKey, err := publicKeyFromPrivateKeyPEM(privPEM)
			if err != nil {
				return nil, fmt.Errorf("derive public key from private key: %w", err)
			}
			opts = append(opts,
				authnJwt.WithPrivateKeyFromPEM(privPEM),
				authnJwt.WithVerificationKey(publicKey),
			)
		case jwtCfg.GetPublicKey() != "":
			// 仅配置公钥：只能用于校验，无法签发。
			opts = append(opts, authnJwt.WithPublicKeyFromPEM([]byte(jwtCfg.GetPublicKey())))
		default:
			return nil, fmt.Errorf("asymmetric method %q requires private_key and/or public_key in config", method)
		}
	} else {
		// 对称算法：key 即 HMAC 共享秘钥，签发与校验共用。
		opts = append(opts, authnJwt.WithKey([]byte(jwtCfg.GetKey())))
	}

	return authnJwt.NewAuthenticator(opts...)
}

// isAsymmetricMethod 判断给定的签名算法是否为非对称（基于公钥/私钥对）算法。
func isAsymmetricMethod(method string) bool {
	switch strings.ToUpper(method) {
	case "RS256", "RS384", "RS512", // RSA
		"PS256", "PS384", "PS512", // RSA-PSS
		"ES256", "ES384", "ES512", // ECDSA
		"EDDSA": // Ed25519
		return true
	default:
		return false
	}
}

// publicKeyFromPrivateKeyPEM 从 PEM 编码的 RSA 私钥中派生出公钥。
// 适用于仅配置了私钥、本服务既签发又校验令牌的场景。
func publicKeyFromPrivateKeyPEM(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from private key")
	}

	var privKey *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY": // PKCS#1
		parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#1 private key: %w", err)
		}
		privKey = parsed
	case "PRIVATE KEY": // PKCS#8
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
		}
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is not an RSA private key")
		}
		privKey = rsaKey
	default:
		return nil, fmt.Errorf("unsupported PEM block type %q (expected RSA PRIVATE KEY or PRIVATE KEY)", block.Type)
	}

	return &privKey.PublicKey, nil
}

// GetAccessTokenExpires 获取访问令牌过期时间。
// 优先使用配置中的 access_token_expires，未配置时回退到默认值。
func (a *Authenticator) GetAccessTokenExpires(clientType authenticationV1.ClientType) time.Duration {
	if a.jwtCfg != nil && a.jwtCfg.GetAccessTokenExpires() != nil {
		return a.jwtCfg.GetAccessTokenExpires().AsDuration()
	}
	switch clientType {
	case authenticationV1.ClientType_admin:
		return DefaultAccessTokenExpires
	default:
		return DefaultAccessTokenExpires
	}
}

// GetRefreshTokenExpires 获取刷新令牌过期时间。
// 优先使用配置中的 refresh_token_expires，未配置时回退到默认值。
func (a *Authenticator) GetRefreshTokenExpires(clientType authenticationV1.ClientType) time.Duration {
	if a.jwtCfg != nil && a.jwtCfg.GetRefreshTokenExpires() != nil {
		return a.jwtCfg.GetRefreshTokenExpires().AsDuration()
	}
	switch clientType {
	case authenticationV1.ClientType_admin:
		return DefaultRefreshTokenExpires
	default:
		return DefaultRefreshTokenExpires
	}
}

// Authenticate 根据不同的客户端类型验证 Token
func (a *Authenticator) Authenticate(ctx context.Context, req *authenticationV1.ValidateTokenRequest) (*authenticationV1.ValidateTokenResponse, error) {
	if req == nil {
		return nil, authenticationV1.ErrorBadRequest("validate token request is nil")
	}

	if req.GetToken() == "" {
		return nil, authenticationV1.ErrorBadRequest("token is empty")
	}

	authenticator, err := a.getAuthenticator(req.GetClientType())
	if err != nil {
		return nil, err
	}

	switch req.GetTokenCategory() {
	case authenticationV1.TokenCategory_ACCESS:
		// Authenticate Token
		var claims *authnEngine.AuthClaims
		claims, err = authenticator.AuthenticateToken(req.GetToken())
		if err != nil {
			return nil, authenticationV1.ErrorUnauthorized("authenticate token failed: [%v]", err)
		}

		// Check Token Expiration
		if jwt.IsTokenExpired(claims) {
			return &authenticationV1.ValidateTokenResponse{
				IsValid: false,
			}, authenticationV1.ErrorUnauthorized("access token is expired")
		}

		// Check Token Validity
		//if !jwt.IsTokenNotValidYet(claims) {
		//	return &authenticationV1.ValidateTokenResponse{
		//		IsValid: false,
		//	}, authenticationV1.ErrorUnauthorized("access token is not valid yet")
		//}

		// Parse Token Payload
		var payload *authenticationV1.UserTokenPayload
		payload, err = jwt.NewUserTokenPayloadWithClaims(claims)
		if err != nil {
			return &authenticationV1.ValidateTokenResponse{
				IsValid: false,
			}, err
		}

		// Check token validity in cache
		if !req.GetSkipRedis() {
			var valid bool
			if valid, err = a.userTokenCache.IsValidAccessToken(ctx, req.GetClientType(), payload.GetUserId(), payload.GetJti(), req.GetToken()); err != nil {
				return &authenticationV1.ValidateTokenResponse{
					IsValid: false,
				}, authenticationV1.ErrorUnauthorized("invalid access token: [%v]", err)
			}
			if !valid {
				return &authenticationV1.ValidateTokenResponse{
					IsValid: false,
				}, authenticationV1.ErrorUnauthorized("access token is revoked or expired")
			}
		}

		// Check if token is blocked
		if !req.GetSkipBlacklist() {
			if a.userTokenCache.IsBlockedAccessToken(ctx, payload.GetJti()) {
				return &authenticationV1.ValidateTokenResponse{
					IsValid: false,
				}, authenticationV1.ErrorUnauthorized("access token is blocked")
			}
		}

		return &authenticationV1.ValidateTokenResponse{
			IsValid: true,
			Payload: payload,
		}, nil

	case authenticationV1.TokenCategory_REFRESH:
		// refresh token 现为自描述 JWT，身份信息从 token 自身解析
		rtAuthenticator, rtErr := a.getAuthenticator(req.GetClientType())
		if rtErr != nil {
			return &authenticationV1.ValidateTokenResponse{IsValid: false}, rtErr
		}
		rtClaims, rtAuthErr := rtAuthenticator.AuthenticateToken(req.GetToken())
		if rtAuthErr != nil {
			return &authenticationV1.ValidateTokenResponse{IsValid: false},
				authenticationV1.ErrorUnauthorized("refresh token authentication failed")
		}
		if jwt.IsTokenExpired(rtClaims) {
			return &authenticationV1.ValidateTokenResponse{IsValid: false},
				authenticationV1.ErrorUnauthorized("refresh token is expired")
		}
		rtUserId, _, _, _, rtParseErr := jwt.ParseRefreshTokenClaims(rtClaims)
		if rtParseErr != nil {
			return &authenticationV1.ValidateTokenResponse{IsValid: false},
				authenticationV1.ErrorUnauthorized("invalid refresh token")
		}
		exist, _, err := a.userTokenCache.IsExistRefreshToken(ctx, req.GetClientType(), rtUserId, req.GetToken())
		if err != nil {
			a.log.Errorf(ctx, "check refresh token exist failed: %s", err.Error())
		}
		if err != nil || !exist {
			return &authenticationV1.ValidateTokenResponse{
				IsValid: false,
			}, authenticationV1.ErrorUnauthorized("refresh token not found for user")
		}

		return &authenticationV1.ValidateTokenResponse{
			IsValid: true,
		}, nil

	default:
		return nil, authenticationV1.ErrorBadRequest("invalid token category")
	}
}

// CreateUserToken 创建用户令牌对（访问令牌和刷新令牌）。
// 登录路径使用：生成 jti、签发令牌对、写入令牌对并绑定逻辑会话 sid。
// payload 必须已带 Application 与 Sid（由调用方按入口/上下文确定）。
func (a *Authenticator) CreateUserToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	tokenPayload *authenticationV1.UserTokenPayload,
) (accessToken, refreshToken string, err error) {
	accessToken, refreshToken, err = a.PrepareUserToken(ctx, clientType, tokenPayload)
	if err != nil {
		return "", "", err
	}

	// 绑定逻辑会话并写入令牌对：登录无旧会话，直接写入。
	sid := tokenPayload.GetSid()
	if sid == "" {
		return "", "", authenticationV1.ErrorBadRequest("token payload missing sid")
	}
	if err = a.userTokenCache.BindSessionAndStore(
		ctx,
		clientType,
		tokenPayload.GetUserId(),
		sid,
		tokenPayload.GetJti(),
		accessToken,
		refreshToken,
		a.GetAccessTokenExpires(clientType),
		a.GetRefreshTokenExpires(clientType),
	); err != nil {
		return "", "", err
	}

	return accessToken, refreshToken, nil
}

// PrepareUserToken 生成 jti 并签发令牌对，但**不写入任何存储**。
// 用于"先准备后提交"的刷新路径：准备阶段失败不消费旧会话。
// payload 必须已带 Application 与 Sid。
func (a *Authenticator) PrepareUserToken(
	_ context.Context,
	clientType authenticationV1.ClientType,
	tokenPayload *authenticationV1.UserTokenPayload,
) (accessToken, refreshToken string, err error) {
	if tokenPayload == nil {
		return "", "", authenticationV1.ErrorBadRequest("token payload is nil")
	}
	if tokenPayload.GetSid() == "" {
		return "", "", authenticationV1.ErrorBadRequest("token payload missing sid")
	}
	if _, ok := jwt.ApplicationName(tokenPayload.GetApplication()); !ok {
		return "", "", authenticationV1.ErrorBadRequest("token payload missing application")
	}

	var jti string
	if jti = a.newJwtId(); jti == "" {
		return "", "", authenticationV1.ErrorServiceUnavailable("create jwt id failed")
	}
	tokenPayload.Jti = trans.Ptr(jti)

	if accessToken, err = a.newAccessToken(clientType, tokenPayload); accessToken == "" || err != nil {
		return "", "", authenticationV1.ErrorServiceUnavailable("create access token failed")
	}
	if refreshToken, err = a.newRefreshToken(clientType, tokenPayload); refreshToken == "" || err != nil {
		return "", "", authenticationV1.ErrorServiceUnavailable("create refresh token failed")
	}

	return accessToken, refreshToken, nil
}

// RevokeUserToken 撤销用户令牌
func (a *Authenticator) RevokeUserToken(ctx context.Context, clientType authenticationV1.ClientType, userId uint32) error {
	if a.userTokenCache == nil {
		a.log.Error(ctx, "userTokenCache is nil")
		return authenticationV1.ErrorServiceUnavailable("token cache unavailable")
	}

	if userId == 0 {
		return authenticationV1.ErrorBadRequest("invalid user id")
	}

	if _, err := a.getAuthenticator(clientType); err != nil {
		return err
	}

	if err := a.userTokenCache.RevokeToken(ctx, clientType, userId); err != nil {
		a.log.Errorf(ctx, "revoke user token failed: %v", err)
		return err
	}

	return nil
}

func (a *Authenticator) RevokeTokenByJti(ctx context.Context, clientType *authenticationV1.ClientType, userId uint32, jti string) error {
	if clientType != nil {
		if _, err := a.getAuthenticator(*clientType); err != nil {
			return err
		}
		return a.userTokenCache.RevokeTokenByJti(ctx, *clientType, userId, jti)
	}

	if err := a.userTokenCache.RevokeTokenByJti(ctx, authenticationV1.ClientType_admin, userId, jti); err != nil {
		return err
	}
	return a.userTokenCache.RevokeTokenByJti(ctx, authenticationV1.ClientType_app, userId, jti)
}

// VerifyRefreshToken 验签 refresh token 并解析出 uid/jti/application/sid。
//
// 本函数**不再消费**旧会话：改为"先准备后提交"——它只验签与解析，
// 由调用方在准备阶段（查用户/算权限/签发新令牌）全部成功后，
// 再调用 CommitRotateSession 做一次原子提交。任一准备步骤失败都不会
// 提前消费旧会话，用户可重试。
//
// 令牌必须携带 application 与 token_use=refresh，缺失即拒绝，
// 不把旧缺 claim 令牌默认归入某应用。返回值中的 application 是**服务端
// 签名的可信来源**，调用方须以它为准，与请求参数、cookie 名比对。
func (a *Authenticator) VerifyRefreshToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	refreshToken string,
) (userId uint32, jti string, application authenticationV1.ApplicationType, sid string, err error) {
	if a.userTokenCache == nil {
		a.log.Error(ctx, "userTokenCache is nil")
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", authenticationV1.ErrorServiceUnavailable("token cache unavailable")
	}
	if refreshToken == "" {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", authenticationV1.ErrorBadRequest("refresh token is empty")
	}

	authenticator, err := a.getAuthenticator(clientType)
	if err != nil {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", err
	}

	claims, authErr := authenticator.AuthenticateToken(refreshToken)
	if authErr != nil {
		a.log.Errorf(ctx, "authenticate refresh token failed: [%s]", authErr)
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", authenticationV1.ErrorIncorrectRefreshToken("invalid refresh token")
	}
	if jwt.IsTokenExpired(claims) {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", authenticationV1.ErrorIncorrectRefreshToken("refresh token is expired")
	}
	userId, jti, application, sid, err = jwt.ParseRefreshTokenClaims(claims)
	if err != nil {
		a.log.Errorf(ctx, "parse refresh token claims failed: [%s]", err)
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", authenticationV1.ErrorIncorrectRefreshToken("invalid refresh token")
	}

	return userId, jti, application, sid, nil
}

// CommitRotateSession 原子提交一次逻辑会话轮换：
// 校验 sid 指针仍指向 oldJti（否则说明已被登出或并发轮换抢走），
// 在同一 Lua 脚本内删除旧令牌对/旧会话记录、写入新令牌对/新会话记录，
// 并把 sid 指针更新为 newJti。
//
// 返回 false 表示该 sid 已失效（登出或并发），调用方须拒绝本次刷新，
// 不得让已准备好的新令牌生效。
func (a *Authenticator) CommitRotateSession(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
	oldJti string,
	newJti string,
	accessToken string,
	refreshToken string,
	newMeta *SessionMeta,
) (bool, error) {
	if a.userTokenCache == nil {
		return false, authenticationV1.ErrorServiceUnavailable("token cache unavailable")
	}
	atTTL := a.GetAccessTokenExpires(clientType)
	rtTTL := a.GetRefreshTokenExpires(clientType)
	return a.userTokenCache.RotateSession(
		ctx, clientType, userId, sid, oldJti, newJti,
		accessToken, refreshToken, newMeta, atTTL, rtTTL,
	)
}

// RevokeSession 退出当前应用的当前逻辑会话（按 sid 精确定位，不波及另一应用
// 或其他设备）。返回 false 表示会话已不存在（幂等）。
func (a *Authenticator) RevokeSession(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
) (bool, error) {
	if a.userTokenCache == nil {
		return false, authenticationV1.ErrorServiceUnavailable("token cache unavailable")
	}
	return a.userTokenCache.RevokeSession(ctx, clientType, userId, sid)
}

// SaveCsrfToken 为逻辑会话写入 CSRF 证明（SetNX，已存在则返回原值）。
// 供 csrf bootstrap 与登录/MFA 签发后调用；返回值即最终生效的证明。
func (a *Authenticator) SaveCsrfToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
	token string,
) (string, error) {
	if a.userTokenCache == nil {
		return "", authenticationV1.ErrorServiceUnavailable("token cache unavailable")
	}
	return a.userTokenCache.SetCsrfToken(ctx, clientType, userId, sid, token, a.GetRefreshTokenExpires(clientType))
}

// GetCsrfToken 读取逻辑会话的 CSRF 证明；无证明返回空串（不视为错误）。
func (a *Authenticator) GetCsrfToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
) (string, error) {
	if a.userTokenCache == nil {
		return "", authenticationV1.ErrorServiceUnavailable("token cache unavailable")
	}
	return a.userTokenCache.GetCsrfToken(ctx, clientType, userId, sid)
}

// NewSessionId 生成新的稳定逻辑会话 ID（sid）。
// 复用与 refresh token 同源的加密随机串，保证不可预测且全局唯一。
func NewSessionId() (string, error) {
	return jwtutil.NewRefreshToken()
}

// GetAccessTokens 获取用户的所有访问令牌
func (a *Authenticator) GetAccessTokens(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
) []string {
	return a.userTokenCache.GetAccessTokens(ctx, clientType, userId)
}

// BlockToken 封禁访问令牌
func (a *Authenticator) BlockToken(
	ctx context.Context,
	req *authenticationV1.BlockTokenRequest,
) (err error) {
	var jti string
	switch req.Target.(type) {
	case *authenticationV1.BlockTokenRequest_Token:
		var exist bool
		exist, jti, err = a.userTokenCache.IsExistAccessToken(ctx, req.GetClientType(), req.GetUserId(), req.GetToken())
		if err != nil {
			a.log.Errorf(ctx, "check access token existence failed: [%v]", err)
			return authenticationV1.ErrorServiceUnavailable("check access token existence failed")
		}
		if !exist {
			a.log.Warnf(ctx, "access token not found for user [%d]", req.GetUserId())
			return authenticationV1.ErrorAccessTokenNotFound("access token not found")
		}

	case *authenticationV1.BlockTokenRequest_Jti:
		var exist bool
		exist, err = a.userTokenCache.IsExistAccessTokenByJti(ctx, req.GetClientType(), req.GetUserId(), req.GetJti())
		if err != nil {
			a.log.Errorf(ctx, "check access token existence by jti failed: [%v]", err)
			return authenticationV1.ErrorServiceUnavailable("check access token existence failed")
		}
		if !exist {
			a.log.Warnf(ctx, "access token not found for user [%d] by jti", req.GetUserId())
			return authenticationV1.ErrorAccessTokenNotFound("access token not found")
		}
		jti = req.GetJti()

	default:
		a.log.Error(ctx, "invalid block token request target")
		return authenticationV1.ErrorBadRequest("invalid block token request target")
	}

	return a.userTokenCache.AddBlockedAccessToken(ctx, jti, req.GetReason(), req.GetDuration().AsDuration())
}

func (a *Authenticator) UnblockToken(
	ctx context.Context,
	req *authenticationV1.UnblockTokenRequest,
) (err error) {
	var jti string
	switch req.Target.(type) {
	case *authenticationV1.UnblockTokenRequest_Token:
		var exist bool
		exist, jti, err = a.userTokenCache.IsExistAccessToken(ctx, req.GetClientType(), req.GetUserId(), req.GetToken())
		if err != nil {
			a.log.Errorf(ctx, "check access token existence failed: [%v]", err)
			return authenticationV1.ErrorServiceUnavailable("check access token existence failed")
		}
		if !exist {
			a.log.Warnf(ctx, "access token not found for user [%d]", req.GetUserId())
			return authenticationV1.ErrorAccessTokenNotFound("access token not found")
		}

	case *authenticationV1.UnblockTokenRequest_Jti:
		var exist bool
		exist, err = a.userTokenCache.IsExistAccessTokenByJti(ctx, req.GetClientType(), req.GetUserId(), req.GetJti())
		if err != nil {
			a.log.Errorf(ctx, "check access token existence by jti failed: [%v]", err)
			return authenticationV1.ErrorServiceUnavailable("check access token existence failed")
		}
		if !exist {
			a.log.Warnf(ctx, "access token not found for user [%d] by jti", req.GetUserId())
			return authenticationV1.ErrorAccessTokenNotFound("access token not found")
		}
		jti = req.GetJti()

	default:
		a.log.Error(ctx, "invalid block token request target")
		return authenticationV1.ErrorBadRequest("invalid block token request target")
	}

	return a.userTokenCache.RevokeTokenByJti(ctx, req.GetClientType(), req.GetUserId(), jti)
}

// ==============================
// 会话元数据（在线用户功能）
// ==============================

// SaveSessionMeta 记录会话元数据，TTL 与 refresh token 对齐。
// 在登录 / 刷新轮换 / MFA 验证通过签发新令牌对后由 service 层调用。
func (a *Authenticator) SaveSessionMeta(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
	meta *SessionMeta,
	expires time.Duration,
) error {
	return a.userTokenCache.SaveSessionMeta(ctx, clientType, userId, jti, meta, expires)
}

// GetSessionMeta 读取单个会话元数据；会话不存在返回 nil（不视为错误）。
func (a *Authenticator) GetSessionMeta(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
) (*SessionMeta, error) {
	return a.userTokenCache.GetSessionMeta(ctx, clientType, userId, jti)
}

// ListSessionEntries 扫描全部在线会话。
func (a *Authenticator) ListSessionEntries(ctx context.Context) ([]SessionEntry, error) {
	return a.userTokenCache.ListSessionEntries(ctx)
}

// RevokeUserTokenAllClientTypes 吊销用户全部客户端类型的所有令牌与会话记录。
// 用于改密 / 重置密码后的强制下线：被改密用户的所有已签发令牌立即失效。
func (a *Authenticator) RevokeUserTokenAllClientTypes(ctx context.Context, userId uint32) error {
	// 显式枚举合法客户端类型（与 RevokeTokenByJti 的 nil 分支同形）。
	// 此前遍历 ClientType_value 名字表：后续枚举若新增无效/未实现类型会被
	// 静默带进吊销循环；叠加 map 迭代序随机与遇错早退，会得到「实际吊销了
	// 哪几类取决于运行时顺序」的部分吊销——对改密强制下线这一安全语义不可
	// 接受。改为逐类全部尝试、汇总错误：任一类失败不阻断其余类别的吊销。
	var errs []error
	for _, ct := range []authenticationV1.ClientType{
		authenticationV1.ClientType_admin,
		authenticationV1.ClientType_app,
	} {
		if err := a.RevokeUserToken(ctx, ct, userId); err != nil {
			a.log.Errorf(ctx, "revoke user [%d] tokens of client type [%d] failed: %v", userId, ct, err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// getAuthenticator 根据客户端类型获取认证器
func (a *Authenticator) getAuthenticator(clientType authenticationV1.ClientType) (authnEngine.Authenticator, error) {
	var authenticator authnEngine.Authenticator
	switch clientType {
	case authenticationV1.ClientType_admin:
		authenticator = a.AdminAuthenticator
	case authenticationV1.ClientType_app:
	default:
		a.log.Error(context.Background(), "invalid client type: [%v]", clientType)
		return nil, authenticationV1.ErrorBadRequest("invalid client type")
	}
	return authenticator, nil
}

// newAccessToken 创建访问令牌
func (a *Authenticator) newAccessToken(
	clientType authenticationV1.ClientType,
	tokenPayload *authenticationV1.UserTokenPayload,
) (accessToken string, err error) {
	if tokenPayload == nil {
		a.log.Error(context.Background(), "token payload is nil")
		return "", authenticationV1.ErrorBadRequest("token payload is nil")
	}

	expTime := time.Now().Add(a.GetAccessTokenExpires(clientType))
	authClaims := jwt.NewUserTokenAuthClaims(tokenPayload, &expTime)

	authenticator, err := a.getAuthenticator(clientType)
	if err != nil {
		return "", err
	}

	accessToken, err = authenticator.CreateIdentity(*authClaims)
	if err != nil {
		a.log.Error(context.Background(), "create access token failed: [%v]", err)
		return "", authenticationV1.ErrorServiceUnavailable("create access token failed")
	}

	return accessToken, nil
}

// newRefreshToken 创建刷新令牌（自描述 JWT，仅含 uid/jti/exp/iat）。
// refresh token 以 HttpOnly Cookie 传输，需脱离 access token 独立鉴权，
// 因此自身携带定位 Redis 记录所需的最小 claims（uid + jti），不含业务权限字段。
func (a *Authenticator) newRefreshToken(
	clientType authenticationV1.ClientType,
	tokenPayload *authenticationV1.UserTokenPayload,
) (refreshToken string, err error) {
	if tokenPayload == nil {
		a.log.Error(context.Background(), "refresh token payload is nil")
		return "", authenticationV1.ErrorBadRequest("refresh token payload is nil")
	}

	expTime := time.Now().Add(a.GetRefreshTokenExpires(clientType))
	authClaims, err := jwt.NewRefreshTokenAuthClaims(
		tokenPayload.GetUserId(),
		tokenPayload.GetJti(),
		tokenPayload.GetApplication(),
		tokenPayload.GetSid(),
		&expTime,
	)
	if err != nil {
		a.log.Error(context.Background(), "create refresh token claims failed: [%v]", err)
		return "", authenticationV1.ErrorServiceUnavailable("create refresh token failed")
	}

	authenticator, err := a.getAuthenticator(clientType)
	if err != nil {
		return "", err
	}

	refreshToken, err = authenticator.CreateIdentity(*authClaims)
	if err != nil {
		a.log.Error(context.Background(), "create refresh token failed: [%v]", err)
		return "", authenticationV1.ErrorServiceUnavailable("create refresh token failed")
	}
	return refreshToken, nil
}

// newJwtId 创建 JWT ID
func (a *Authenticator) newJwtId() string {
	return jwtutil.NewJWTId()
}
