package bootstrap

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"go-wind-admin/pkg/constants"
)

// codesOf 提取判定结果中的 code 集合（排序，便于断言）。
func codesOf(rows []permissionRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Code)
	}
	sort.Strings(out)
	return out
}

// TestDecideDisableProtectsOutOfScopeCodes 覆盖修复：阶段B align 曾把不属其处置范围的既有
// 权限当作"库中多余码"停用——包括业务域（如 modeldev:*，绑定豁免路由）与不依附任何 HTTP
// 路由、由 CLI 按码直接检查的业务能力（如 modeldev:manage_release_binding），后者会导致
// `admin modeldev-pause` 直接失效。
func TestDecideDisableProtectsOutOfScopeCodes(t *testing.T) {
	const (
		apiInScope = uint64(100) // 纳入范围路由（Resolve 命中权威表）
		apiExempt  = uint64(200) // 豁免路由（业务域/独立认证）
	)

	perms := []permissionRow{
		{ID: 1, Code: "user:view", Status: "ON"},                                             // 已在期望集 → 不处置
		{ID: 2, Code: "modeldev:pool:list", Status: "ON"},                                    // 只绑豁免路由 → 保护
		{ID: 3, Code: "stale:code", Status: "ON"},                                            // 绑纳入范围路由且码不匹配 → 停用
		{ID: 4, Code: constants.SystemPermissionCodePrefix + "platform_admin", Status: "ON"}, // sys: → 不处置
		{ID: 5, Code: "old:code", Status: "OFF"},                                             // 非 ON → 不处置
		{ID: 6, Code: "modeldev:manage_release_binding", Status: "ON"},                       // 不绑任何路由（CLI 按码检查）→ 保护
		{ID: 7, Code: "mixed:code", Status: "ON"},                                            // 同时绑纳入范围与豁免路由 → 停用
	}
	links := map[uint64]map[uint64]bool{
		1: {apiInScope: true},
		2: {apiExempt: true},
		3: {apiInScope: true},
		4: {apiInScope: true},
		5: {apiInScope: true},
		6: {}, // 无任何 API 绑定
		7: {apiInScope: true, apiExempt: true},
	}
	inScopeAPIIDs := map[uint64]bool{apiInScope: true}
	expected := map[string][]uint64{"user:view": {apiInScope}}

	d := decideDisable(perms, links, inScopeAPIIDs, expected)

	require.Equal(t, []string{"modeldev:manage_release_binding", "modeldev:pool:list"}, codesOf(d.Protect),
		"超出 align 处置范围（只绑豁免路由或无任何绑定）的码必须被保护，不被停用")
	require.Equal(t, []string{"mixed:code", "stale:code"}, codesOf(d.Disable),
		"绑定纳入范围路由且不在期望集的码仍应停用")
}

// TestDecideDisableEmptyInputs 边界：无权限、无关联网、无纳入范围路由时的判定。
func TestDecideDisableEmptyInputs(t *testing.T) {
	d := decideDisable(nil, nil, nil, nil)
	require.Empty(t, d.Disable)
	require.Empty(t, d.Protect)

	perms := []permissionRow{{ID: 1, Code: "stale:code", Status: "ON"}}
	links := map[uint64]map[uint64]bool{1: {100: true}}

	// 无纳入范围路由集合：该码不属 align 范围 → 保护。
	d = decideDisable(perms, links, map[uint64]bool{}, map[string][]uint64{})
	require.Empty(t, d.Disable)
	require.Equal(t, []string{"stale:code"}, codesOf(d.Protect))

	// 该路由纳入范围且码不在期望集 → 停用。
	d = decideDisable(perms, links, map[uint64]bool{100: true}, map[string][]uint64{})
	require.Equal(t, []string{"stale:code"}, codesOf(d.Disable))
	require.Empty(t, d.Protect)
}
