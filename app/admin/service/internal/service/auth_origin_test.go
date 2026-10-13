package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

// newOriginTestValidator 构造带固定白名单的校验器（不经 bootstrap.Context，
// 单测聚焦 allowlist 选择与匹配语义）。
func newOriginTestValidator(global, console, boss []string) *authOriginValidator {
	return &authOriginValidator{
		log:     bLogger.NewHelper(bLogger.NopLogger()),
		global:  global,
		console: console,
		boss:    boss,
	}
}

// originCtx 构造带 Origin 头的服务端传输上下文（复用同包 headerCtx 桩）。
func originCtx(origin string) context.Context {
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	return headerCtx(context.Background(), header)
}

// contentTypeCtx 构造带 Content-Type 头的服务端传输上下文。
func contentTypeCtx(contentType string) context.Context {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return headerCtx(context.Background(), header)
}

// TestValidateAuthOrigin_PerApplicationBinding 回归偏差 1 修复：
// 白名单按 application 绑定——console 专属集合只放行 console 通道，
// 同一来源走 boss 通道必须被来源层拒绝（S03），而不是落到 claim 裁决兜底。
func TestValidateAuthOrigin_PerApplicationBinding(t *testing.T) {
	v := newOriginTestValidator(
		[]string{"https://global.example"},
		[]string{"https://console.example"},
		[]string{"https://boss.example"},
	)

	// console 专属集合内：console 通道放行
	require.NoError(t, v.validate(originCtx("https://console.example"), "refresh", authenticationV1.ApplicationType_console))
	// 同一来源走 boss 通道：来源层 403（专属集合不匹配，不回退 global）
	err := v.validate(originCtx("https://console.example"), "refresh", authenticationV1.ApplicationType_boss)
	require.Error(t, err, "console 来源不得选择 boss 通道")
	require.True(t, authenticationV1.IsForbidden(err))
	// global 集合来源：专属集合生效时不再回退，同样拒绝
	err = v.validate(originCtx("https://global.example"), "csrf", authenticationV1.ApplicationType_console)
	require.Error(t, err, "专属集合生效时不回退全局集合")
	require.True(t, authenticationV1.IsForbidden(err))
}

// TestValidateAuthOrigin_FallbackAndDegradation 未配置专属集合时回退全局集合；
// 两级均未配置时降级告警放行；Origin 缺失（非浏览器客户端）放行（偏差 2 裁决保持）。
func TestValidateAuthOrigin_FallbackAndDegradation(t *testing.T) {
	// 未配置专属集合：回退全局
	v := newOriginTestValidator([]string{"https://global.example"}, nil, nil)
	require.NoError(t, v.validate(originCtx("https://global.example"), "refresh", authenticationV1.ApplicationType_boss))
	err := v.validate(originCtx("https://other.example"), "refresh", authenticationV1.ApplicationType_boss)
	require.True(t, authenticationV1.IsForbidden(err))

	// 两级均未配置：降级告警放行
	empty := newOriginTestValidator(nil, nil, nil)
	require.NoError(t, empty.validate(originCtx("https://anything.example"), "refresh", authenticationV1.ApplicationType_console))

	// Origin 缺失：放行（非浏览器客户端，与偏差 2 裁决一致）
	require.NoError(t, v.validate(originCtx(""), "refresh", authenticationV1.ApplicationType_console))

	// 大小写不敏感精确匹配（host 归一）
	scheme := newOriginTestValidator([]string{"https://Console.Example"}, nil, nil)
	require.NoError(t, scheme.validate(originCtx("https://console.example"), "refresh", authenticationV1.ApplicationType_console))
}

// TestParseOriginsEnv 逗号分隔解析：空白项剔除、未配置返回 nil。
func TestParseOriginsEnv(t *testing.T) {
	t.Setenv("GWA_TEST_ORIGINS", " https://a.example , https://b.example ,,")
	assert.Equal(t, []string{"https://a.example", "https://b.example"}, parseOriginsEnv("GWA_TEST_ORIGINS"))

	t.Setenv("GWA_TEST_ORIGINS", "  ")
	assert.Nil(t, parseOriginsEnv("GWA_TEST_ORIGINS"))

	// 未设置：必须返回 nil（不能读到其它测试的残留环境）
	assert.Nil(t, parseOriginsEnv("GWA_TEST_ORIGINS_UNSET"))
}

// TestRequireJSONContentType 认证写端点的 JSON Content-Type 闸门（偏差 3 修复）：
// application/json（含 charset 参数）放行；text/plain 等简单请求 403；
// 非 HTTP 传输（内部调用/测试直调）跳过。
func TestRequireJSONContentType(t *testing.T) {
	require.NoError(t, requireJSONContentType(contentTypeCtx("application/json")))
	require.NoError(t, requireJSONContentType(contentTypeCtx("application/json; charset=utf-8")))
	require.NoError(t, requireJSONContentType(contentTypeCtx("APPLICATION/JSON")))

	err := requireJSONContentType(contentTypeCtx("text/plain"))
	require.Error(t, err, "text/plain 简单请求必须被拒绝（防绕过预检）")
	require.True(t, authenticationV1.IsForbidden(err))

	err = requireJSONContentType(contentTypeCtx(""))
	require.Error(t, err, "缺失 Content-Type 必须被拒绝")
	require.True(t, authenticationV1.IsForbidden(err))

	// 非 HTTP 传输：跳过（登录引擎内部调用/包内测试路径）
	require.NoError(t, requireJSONContentType(context.Background()))
}
