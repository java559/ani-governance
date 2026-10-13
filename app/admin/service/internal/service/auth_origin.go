package service

import (
	"context"
	"net/url"
	"os"
	"strings"

	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"

	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	"go-wind-admin/pkg/netutil"
)

// 认证来源白名单的应用专属环境变量（逗号分隔精确 origin）。
// 与 GWA_AUTH_JWT_* 同款模式：conf 结构只有全局 origins 列表，无法表达
// application → 集合绑定，故专属集合走显式环境变量，正式环境必须配置，
// 未配置的应用回退全局 CORS origins（保持既有语义）。
const (
	consoleOriginsEnv = "GWA_AUTH_CONSOLE_ORIGINS"
	bossOriginsEnv    = "GWA_AUTH_BOSS_ORIGINS"
)

// authOriginValidator 认证写端点（登录/MFA/csrf/refresh/logout）的来源校验器。
//
// 白名单按 application 绑定：console/boss 各有独立精确 Origin 集合，
// 一个应用的来源不得选择另一应用的会话通道（计划合同 §6）。
// 校验规则：
//   - Origin 头优先；无 Origin 时接受 Referer 归一化后的 origin；
//   - 均缺失视为非浏览器客户端（脚本/服务间调用），CSRF 风险不适用，放行
//     （裁决记录：计划原文要求拒绝，2026-10-13 确认保持放行现状）；
//   - 白名单未配置时降级告警放行（CSRF 证明仍独立保护刷新），避免漏配置
//     直接打断全部浏览器会话；
//   - 命中比较为 scheme://host[:port] 精确匹配（大小写不敏感），
//     不使用后缀、通配符或字符串 contains。
type authOriginValidator struct {
	log     *bLogger.Helper
	global  []string // 全局回退集合（CORS origins，单一事实源）
	console []string // console 应用专属集合
	boss    []string // boss 应用专属集合
}

func newAuthOriginValidator(ctx *bootstrap.Context) *authOriginValidator {
	return &authOriginValidator{
		log:     ctx.NewLoggerHelper("authn/origin-validator"),
		global:  authAllowedOriginsFromConfig(ctx.GetConfig()),
		console: parseOriginsEnv(consoleOriginsEnv),
		boss:    parseOriginsEnv(bossOriginsEnv),
	}
}

// authAllowedOriginsFromConfig 从启动配置读取 CORS origins 列表作为全局回退白名单。
// 两者共用一份配置：能通过 CORS 携带凭据访问本服务的来源，才允许发起认证写请求。
func authAllowedOriginsFromConfig(cfg *conf.Bootstrap) []string {
	return cfg.GetServer().GetRest().GetCors().GetOrigins()
}

// parseOriginsEnv 解析逗号分隔的 origin 环境变量；空/未配置返回 nil。
func parseOriginsEnv(name string) []string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// allowlistFor 返回该应用的来源白名单：应用专属集合优先，未配置时回退全局集合。
func (v *authOriginValidator) allowlistFor(application authenticationV1.ApplicationType) []string {
	switch application {
	case authenticationV1.ApplicationType_console:
		if len(v.console) > 0 {
			return v.console
		}
	case authenticationV1.ApplicationType_boss:
		if len(v.boss) > 0 {
			return v.boss
		}
	}
	return v.global
}

// validate 校验请求来源是否在指定应用的白名单内。
// 拒绝返回 403 且无副作用（调用方必须在任何令牌消费/会话变更之前调用）。
func (v *authOriginValidator) validate(ctx context.Context, endpoint string, application authenticationV1.ApplicationType) error {
	header := netutil.HeaderFromContext(ctx)
	if header == nil {
		return nil
	}

	// Origin 头在跨站 POST 与同站 POST（现代浏览器）均会携带，取值形如
	// scheme://host[:port]，无路径；Referer 含路径，需归一化为 scheme://host。
	raw := strings.TrimSpace(header.Get("Origin"))
	if raw == "" {
		if ref := strings.TrimSpace(header.Get("Referer")); ref != "" {
			if u, perr := url.Parse(ref); perr == nil && u.Host != "" {
				raw = u.Scheme + "://" + u.Host
			}
		}
	}
	if raw == "" {
		return nil
	}

	allowlist := v.allowlistFor(application)
	if len(allowlist) == 0 {
		v.log.Warnf(ctx, "auth origin check skipped on [%s/%v]: allowlist not configured", endpoint, application)
		return nil
	}

	for _, o := range allowlist {
		if strings.EqualFold(strings.TrimSpace(o), raw) {
			return nil
		}
	}

	v.log.Warnf(ctx, "auth origin rejected on [%s/%v]: origin [%s] not in allowlist", endpoint, application, raw)
	return authenticationV1.ErrorForbidden("request origin is not allowed")
}
