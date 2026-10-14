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

// TestDecideDisableProtectsExemptRouteCodes 覆盖"豁免路由绑定的既有码不被误停用"这一修复：
// 阶段B align 曾把业务域（如 modeldev:*）的既有权限当作"库中多余码"置 OFF。
func TestDecideDisableProtectsExemptRouteCodes(t *testing.T) {
	const (
		apiNormal = uint64(100) // 非豁免路由的 api
		apiExempt = uint64(200) // 豁免路由的 api
		apiOther  = uint64(101)
	)

	perms := []permissionRow{
		{ID: 1, Code: "user:view", Status: "ON"},                                             // 已在期望集 → 不处置
		{ID: 2, Code: "modeldev:pool:list", Status: "ON"},                                    // 绑定豁免路由 → 保护
		{ID: 3, Code: "stale:code", Status: "ON"},                                            // 普通陈旧码 → 停用
		{ID: 4, Code: constants.SystemPermissionCodePrefix + "platform_admin", Status: "ON"}, // sys: → 不处置
		{ID: 5, Code: "old:code", Status: "OFF"},                                             // 非 ON → 不处置
		{ID: 6, Code: "mixed:code", Status: "ON"},                                            // 同时绑豁免与非豁免 → 保护
		{ID: 7, Code: "orphan:code", Status: "ON"},                                           // 无关联 → 停用
	}
	links := map[uint64]map[uint64]bool{
		1: {apiNormal: true},
		2: {apiExempt: true},
		3: {apiNormal: true},
		4: {apiNormal: true},
		5: {apiNormal: true},
		6: {apiExempt: true, apiOther: true},
		7: {},
	}
	exemptAPIIDs := map[uint64]bool{apiExempt: true}
	expected := map[string][]uint64{"user:view": {apiNormal}}

	d := decideDisable(perms, links, exemptAPIIDs, expected)

	require.Equal(t, []string{"mixed:code", "modeldev:pool:list"}, codesOf(d.Protect),
		"绑定豁免路由的码必须被保护，不被停用")
	require.Equal(t, []string{"orphan:code", "stale:code"}, codesOf(d.Disable),
		"其余非 sys:、不在期望集、当前 ON 的码仍应停用")
}

// TestDecideDisableEmptyInputs 边界：无权限或无关联网时不应报错或误判。
func TestDecideDisableEmptyInputs(t *testing.T) {
	d := decideDisable(nil, nil, nil, nil)
	require.Empty(t, d.Disable)
	require.Empty(t, d.Protect)

	// 有关联表但无豁免 api：普通陈旧码仍停用。
	perms := []permissionRow{{ID: 1, Code: "stale:code", Status: "ON"}}
	d = decideDisable(perms, map[uint64]map[uint64]bool{1: {100: true}}, map[uint64]bool{}, map[string][]uint64{})
	require.Equal(t, []string{"stale:code"}, codesOf(d.Disable))
	require.Empty(t, d.Protect)
}
