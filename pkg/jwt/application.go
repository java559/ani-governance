package jwt

import (
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
)

// 应用会话与令牌用途相关的 claim 字段名。
//
// 与既有 claim 命名风格保持一致（短名）：
//   - app：应用会话标识（console / boss），是会话隔离的必要声明，缺失即拒绝。
//   - sid：稳定逻辑会话 ID，刷新后 jti 变、sid 不变。
//   - tu：令牌用途（access / refresh），缺省或非法值拒绝，避免 AT/RT 混用。
const (
	ClaimFieldApplication = "app"
	ClaimFieldSid         = "sid"
	ClaimFieldTokenUse    = "tu"
)

// 令牌用途取值。仅允许此二值，缺省或其余取值一律视为非法。
const (
	TokenUseAccess  = "access"
	TokenUseRefresh = "refresh"
)

// applicationNames 是 ApplicationType → claim 字符串的映射。
// 仅 console / boss 两个业务值可被序列化为 claim，UNSPECIFIED 不产出。
var applicationNames = map[authenticationV1.ApplicationType]string{
	authenticationV1.ApplicationType_console: "console",
	authenticationV1.ApplicationType_boss:    "boss",
}

// applicationValues 是 claim 字符串 → ApplicationType 的反向映射。
var applicationValues = map[string]authenticationV1.ApplicationType{
	"console": authenticationV1.ApplicationType_console,
	"boss":    authenticationV1.ApplicationType_boss,
}

// ApplicationName 返回应用会话标识的 claim 字符串形式。
// 第二返回值为 false 表示该值不是合法的业务应用（含 UNSPECIFIED），不得写入令牌。
func ApplicationName(app authenticationV1.ApplicationType) (string, bool) {
	name, ok := applicationNames[app]
	return name, ok
}

// ParseApplication 将 claim 字符串解析为 ApplicationType。
// 第二返回值为 false 表示非法或缺失的应用标识，调用方必须拒绝。
func ParseApplication(name string) (authenticationV1.ApplicationType, bool) {
	app, ok := applicationValues[name]
	return app, ok
}

// IsValidTokenUse 判断令牌用途取值是否合法（access / refresh）。
func IsValidTokenUse(tokenUse string) bool {
	return tokenUse == TokenUseAccess || tokenUse == TokenUseRefresh
}
