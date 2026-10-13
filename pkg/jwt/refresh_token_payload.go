package jwt

import (
	"errors"
	"time"

	authn "go-wind-admin/pkg/localdeps/kratos-authn/engine"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
)

// Refresh token 的 claim 字段复用 access token 的 ClaimFieldUserID ("uid")
// 与 authn.ClaimFieldJwtID ("jti")，加上标准的 exp/iat。
// refresh token JWT 还携带 application（app）、稳定逻辑会话 ID（sid）
// 与令牌用途（tu=refresh），使服务端能独立判定"这枚 RT 属于哪个应用会话"，
// 不再依赖 cookie 名这一唯一防线。
//
// 注意：refresh token 不携带 sub/tid/roc/ds/ouid/ipa/ita/cid/did 等业务字段，
// 租户身份在刷新链上从数据库用户行反推（见 doGrantTypeRefreshToken）。

// NewRefreshTokenAuthClaims 创建刷新令牌的认证声明。
// 包含 uid、jti、exp、iat，以及应用会话隔离所需的 application、sid、token_use。
func NewRefreshTokenAuthClaims(
	userId uint32,
	jti string,
	application authenticationV1.ApplicationType,
	sid string,
	expirationTime *time.Time,
) (*authn.AuthClaims, error) {
	if userId == 0 {
		return nil, errors.New("refresh token claims: userId is empty")
	}
	if jti == "" {
		return nil, errors.New("refresh token claims: jti is empty")
	}
	appName, ok := ApplicationName(application)
	if !ok {
		return nil, errors.New("refresh token claims: invalid application")
	}
	if sid == "" {
		return nil, errors.New("refresh token claims: sid is empty")
	}

	authClaims := authn.AuthClaims{
		ClaimFieldUserID:         userId,
		authn.ClaimFieldJwtID:    jti,
		authn.ClaimFieldIssuedAt: time.Now().Unix(),
		ClaimFieldApplication:    appName,
		ClaimFieldSid:            sid,
		ClaimFieldTokenUse:       TokenUseRefresh,
	}

	if expirationTime != nil {
		authClaims[authn.ClaimFieldExpirationTime] = expirationTime.Unix()
	}

	return &authClaims, nil
}

// ParseRefreshTokenClaims 从已验签的 refresh token claims 中提取 uid、jti、
// application 与 sid。调用方须先用 authenticator.AuthenticateToken 验签取得
// *authn.AuthClaims，再调用本函数提取定位信息。
//
// 任一必要 claim 缺失或非法即返回错误：不允许把缺失 claim 的旧令牌默认归入
// 某应用（否则旧令牌会被静默接受，破坏隔离）。
func ParseRefreshTokenClaims(claims *authn.AuthClaims) (userId uint32, jti string, application authenticationV1.ApplicationType, sid string, err error) {
	if claims == nil {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", errors.New("refresh token claims: nil claims")
	}

	userId, err = claims.GetUint32(ClaimFieldUserID)
	if err != nil {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", err
	}
	if userId == 0 {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", errors.New("refresh token claims: uid is empty")
	}

	jti, err = claims.GetJwtID()
	if err != nil {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", err
	}
	if jti == "" {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", errors.New("refresh token claims: jti is empty")
	}

	// 令牌用途必须显式声明为 refresh，拒绝缺失或 access。
	tokenUse, tuErr := claims.GetString(ClaimFieldTokenUse)
	if tuErr != nil || tokenUse != TokenUseRefresh {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", errors.New("refresh token claims: missing or invalid token_use")
	}

	appName, appErr := claims.GetString(ClaimFieldApplication)
	if appErr != nil {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", errors.New("refresh token claims: missing application")
	}
	app, ok := ParseApplication(appName)
	if !ok {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", errors.New("refresh token claims: invalid application")
	}

	sid, sidErr := claims.GetString(ClaimFieldSid)
	if sidErr != nil || sid == "" {
		return 0, "", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "", errors.New("refresh token claims: missing sid")
	}

	return userId, jti, app, sid, nil
}
