package jwt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authn "go-wind-admin/pkg/localdeps/kratos-authn/engine"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
)

// TestNewRefreshTokenAuthClaims_Validation 创建刷新令牌 claims 前必须
// 校验定位要素：userId 与 jti 缺一不可（二者是定位 Redis 刷新令牌记录、
// 完成轮换与吊销的最小信息）；应用会话隔离后还必须显式声明 application
// 与 sid（缺失会被静默归入错误应用或无法按逻辑会话吊销）。缺失时必须报错，
// 而不是产出一条无法定位会话记录的 claims。
func TestNewRefreshTokenAuthClaims_Validation(t *testing.T) {
	cases := []struct {
		name        string
		userId      uint32
		jti         string
		application authenticationV1.ApplicationType
		sid         string
		errMsg      string
	}{
		{"missing userId", 0, "jti-1", authenticationV1.ApplicationType_console, "sid-1", "refresh token claims: userId is empty"},
		{"missing jti", 7, "", authenticationV1.ApplicationType_console, "sid-1", "refresh token claims: jti is empty"},
		{"missing application", 7, "jti-1", authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, "sid-1", "refresh token claims: invalid application"},
		{"missing sid", 7, "jti-1", authenticationV1.ApplicationType_console, "", "refresh token claims: sid is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := NewRefreshTokenAuthClaims(tc.userId, tc.jti, tc.application, tc.sid, nil)
			require.Error(t, err, "缺少定位要素时必须报错")
			assert.Nil(t, claims)
			assert.EqualError(t, err, tc.errMsg)
		})
	}
}

// TestNewRefreshTokenAuthClaims_MinimalShape 合法入参产出的 claims 只携带
// uid/jti/iat/application/sid/token_use（可选 exp），不得夹带任何业务权限字段
// ——刷新令牌能脱离 access token 独立鉴权，若混入 roc/ds 等字段会形成权限旁路面。
func TestNewRefreshTokenAuthClaims_MinimalShape(t *testing.T) {
	claims, err := NewRefreshTokenAuthClaims(7, "jti-abc", authenticationV1.ApplicationType_boss, "sid-abc", nil)
	require.NoError(t, err)
	require.NotNil(t, claims)

	assert.EqualValues(t, 7, (*claims)[ClaimFieldUserID], "uid 应为 7")
	assert.Equal(t, "jti-abc", (*claims)[authn.ClaimFieldJwtID], "jti 应原样落位")
	assert.Equal(t, "boss", (*claims)[ClaimFieldApplication], "application 应落位为名字形态")
	assert.Equal(t, "sid-abc", (*claims)[ClaimFieldSid], "sid 应原样落位")
	assert.Equal(t, TokenUseRefresh, (*claims)[ClaimFieldTokenUse], "token_use 应为 refresh")
	assert.Contains(t, *claims, authn.ClaimFieldIssuedAt, "iat 必须存在")
	assert.NotContains(t, *claims, authn.ClaimFieldExpirationTime,
		"未传过期时间时不得自带 exp")
	// 刷新令牌不得携带业务字段
	for _, forbidden := range []string{
		authn.ClaimFieldSubject,
		ClaimFieldTenantID,
		ClaimFieldRoleCodes,
		ClaimFieldDataScope,
		ClaimFieldOrgUnitID,
		ClaimFieldClientID,
		ClaimFieldDeviceID,
		ClaimFieldDataScopes,
		ClaimFieldDataScopeUnits,
		ClaimFieldHiddenFields,
		ClaimFieldIsPlatformAdmin,
		ClaimFieldIsTenantAdmin,
	} {
		assert.NotContains(t, *claims, forbidden,
			"刷新令牌 claims 不应携带业务字段 %q", forbidden)
	}
}

// TestNewRefreshTokenAuthClaims_WithExpiration 显式传入过期时间时，
// exp 声明必须按该时间落位（刷新令牌的绝对有效期由创建时的 exp 决定）。
func TestNewRefreshTokenAuthClaims_WithExpiration(t *testing.T) {
	exp := time.Now().Add(7 * 24 * time.Hour)
	claims, err := NewRefreshTokenAuthClaims(9, "jti-def", authenticationV1.ApplicationType_console, "sid-def", &exp)
	require.NoError(t, err)
	require.NotNil(t, claims)
	assert.EqualValues(t, exp.Unix(), (*claims)[authn.ClaimFieldExpirationTime])
}

// TestParseRefreshTokenClaims_RoundTrip 生成 → 解析往返一致性：
// uid/jti/application/sid 必须无损还原（带 exp 与不带 exp 两条路径）。
func TestParseRefreshTokenClaims_RoundTrip(t *testing.T) {
	t.Run("without expiration", func(t *testing.T) {
		claims, err := NewRefreshTokenAuthClaims(11, "jti-roundtrip", authenticationV1.ApplicationType_console, "sid-roundtrip", nil)
		require.NoError(t, err)
		userId, jti, application, sid, err := ParseRefreshTokenClaims(claims)
		require.NoError(t, err)
		assert.EqualValues(t, 11, userId)
		assert.Equal(t, "jti-roundtrip", jti)
		assert.Equal(t, authenticationV1.ApplicationType_console, application)
		assert.Equal(t, "sid-roundtrip", sid)
	})
	t.Run("with expiration", func(t *testing.T) {
		exp := time.Now().Add(time.Hour)
		claims, err := NewRefreshTokenAuthClaims(12, "jti-roundtrip-2", authenticationV1.ApplicationType_boss, "sid-roundtrip-2", &exp)
		require.NoError(t, err)
		userId, jti, application, sid, err := ParseRefreshTokenClaims(claims)
		require.NoError(t, err)
		assert.EqualValues(t, 12, userId)
		assert.Equal(t, "jti-roundtrip-2", jti)
		assert.Equal(t, authenticationV1.ApplicationType_boss, application)
		assert.Equal(t, "sid-roundtrip-2", sid)
	})
}

// TestParseRefreshTokenClaims_RejectsIncomplete 解析侧对称校验：
// nil claims、缺 uid、uid 为 0、uid/jti 类型错误、缺 jti、空 jti、
// 缺 token_use、token_use 为 access、缺 application、application 非法、
// 缺 sid 一律拒绝。任一定位要素不完整的旧令牌放行，都会破坏应用隔离
// 或导致 Redis 定位静默失败。
func TestParseRefreshTokenClaims_RejectsIncomplete(t *testing.T) {
	cases := []struct {
		name   string
		claims *authn.AuthClaims
	}{
		{"nil claims", nil},
		{"empty claims", &authn.AuthClaims{}},
		{"uid wrong type", &authn.AuthClaims{ClaimFieldUserID: "not-a-number"}},
		{"jti missing", &authn.AuthClaims{ClaimFieldUserID: uint32(3)}},
		{"jti wrong type", &authn.AuthClaims{ClaimFieldUserID: uint32(3), authn.ClaimFieldJwtID: 42}},
		{"jti empty", &authn.AuthClaims{ClaimFieldUserID: uint32(3), authn.ClaimFieldJwtID: ""}},
		{"token_use missing", &authn.AuthClaims{
			ClaimFieldUserID: uint32(3), authn.ClaimFieldJwtID: "jti-1",
		}},
		{"token_use is access", &authn.AuthClaims{
			ClaimFieldUserID: uint32(3), authn.ClaimFieldJwtID: "jti-1", ClaimFieldTokenUse: TokenUseAccess,
		}},
		{"application missing", &authn.AuthClaims{
			ClaimFieldUserID: uint32(3), authn.ClaimFieldJwtID: "jti-1", ClaimFieldTokenUse: TokenUseRefresh,
		}},
		{"application invalid", &authn.AuthClaims{
			ClaimFieldUserID: uint32(3), authn.ClaimFieldJwtID: "jti-1",
			ClaimFieldTokenUse: TokenUseRefresh, ClaimFieldApplication: "miniapp",
		}},
		{"sid missing", &authn.AuthClaims{
			ClaimFieldUserID: uint32(3), authn.ClaimFieldJwtID: "jti-1",
			ClaimFieldTokenUse: TokenUseRefresh, ClaimFieldApplication: "console",
		}},
		{"sid empty", &authn.AuthClaims{
			ClaimFieldUserID: uint32(3), authn.ClaimFieldJwtID: "jti-1",
			ClaimFieldTokenUse: TokenUseRefresh, ClaimFieldApplication: "console", ClaimFieldSid: "",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userId, jti, application, sid, err := ParseRefreshTokenClaims(tc.claims)
			require.Error(t, err, "定位要素不完整时必须报错")
			assert.EqualValues(t, 0, userId)
			assert.Empty(t, jti)
			assert.Equal(t, authenticationV1.ApplicationType_APPLICATION_TYPE_UNSPECIFIED, application)
			assert.Empty(t, sid)
		})
	}
}
