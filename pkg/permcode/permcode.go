// Package permcode 承载权限码规范（docs/contracts/permission-code.md）的运行时查表层。
//
// 架构决策（实施文档 §B3.3/§B4/§B8.3）：管理面 190 条与各业务域/Quota 119 条的期望码
// 已在生成期产出、经人工评审后固化进权威表；运行时只按 (method, path) 查表取码，
// 不重新执行推导。查不到即"缺项"，报错停止。
//
// 生成物（management_gen.go / capability_gen.go）由 scripts/permcode/gen-go-maps.py
// 从权威 TSV 产出，不手改。ConvertCodeByPath 等推导函数降级为候选值来源，永不直接写入授权目录。
package permcode

import (
	"fmt"
	"sort"
	"strings"
)

// Source 标记期望码的权威来源，便于对齐入口与校验区分"显式能力表 / 已批准管理 CRUD / 独立认证合同"。
const (
	SourceManagement = "管理面映射表"
	SourceCapability = "业务能力映射表"
)

// ScopeKind（作用范围）取值，对应权威表的 scope_kind 列。
//
// self / tenant-self 表示"操作者读写自己（或个人所属租户）的资源"，不属于可售业务模块，
// 因此不参与套餐模块白名单判定（见 app/admin/service/internal/data/tenant_access_checker.go）。
// shared / platform / tenant 属可售或平台侧范围，仍需过模块闸门。
const (
	ScopeKindSelf       = "self"        // 个人自助：读写自己的资料、会话、凭证
	ScopeKindTenantSelf = "tenant-self" // 租户自助：读本租户自己的配额等
)

// expectation 是权威表中一条 (method, path) 的期望。
type expectation struct {
	Operation      string // RPC operation（管理面表有，能力表为空）
	Code           string // 规范期望码
	Source         string // 原始 permission_source / code_source
	BusinessModule string
	ScopeKind      string
}

// Expectation 是解析结果（对外暴露，避免泄漏内部 map 结构）。
type Expectation struct {
	Method         string
	Path           string
	Code           string
	Source         string
	Operation      string
	BusinessModule string
	ScopeKind      string
}

// ConflictError 表示同一 (method, path) 在两张权威表返回不同码，按规范 §4 必须报错停止。
type ConflictError struct {
	Key            string
	ManagementCode string
	CapabilityCode string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("permission code conflict for %q: management=%s capability=%s", e.Key, e.ManagementCode, e.CapabilityCode)
}

// MissingError 表示 (method, path) 未命中任何权威表，且不属于可豁免的独立认证/特殊传输入口。
type MissingError struct {
	Key string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("permission code missing for %q: not found in authoritative tables", e.Key)
}

func key(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + strings.TrimSpace(path)
}

// normalizePath 把路径模板中的参数段名归一化为 {}，用于跨来源（proto 蛇形 vs 生成驼峰）
// 匹配同一路由。非参数段保持原样。
//
// 例：/api/v1/admin/users/{user_id}/password 与 /api/v1/admin/users/{userId}/password
// 归一化后同为 /api/v1/admin/users/{}/password。
func normalizePath(path string) string {
	if !strings.ContainsRune(path, '{') {
		return path
	}
	var b strings.Builder
	depth := 0
	for _, r := range path {
		switch r {
		case '{':
			if depth == 0 {
				b.WriteString("{}")
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func nkey(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + normalizePath(strings.TrimSpace(path))
}

// 归一化索引：normalized key -> expectation。若同一 normalized key 命中多个不同码，
// 标记为冲突（在 init 时记录）。
var (
	normalizedManagement    map[string]expectation
	normalizedCapability    map[string]expectation
	normalizedConflicts     []ConflictError
	exemptReasonsNormalized map[string]string
)

func init() {
	normalizedManagement, normalizedCapability, normalizedConflicts = buildNormalized()
	exemptReasonsNormalized = make(map[string]string, len(exemptReasons))
	for k, reason := range exemptReasons {
		method, path := SplitKey(k)
		n := nkey(method, path)
		if _, ok := exemptReasonsNormalized[n]; !ok {
			exemptReasonsNormalized[n] = reason
		}
	}
}

func buildNormalized() (map[string]expectation, map[string]expectation, []ConflictError) {
	var conflicts []ConflictError
	add := func(dst map[string]expectation, src map[string]expectation, label string) {
		for k, v := range src {
			n := normalizePath(k)
			if prev, ok := dst[n]; ok {
				if prev.Code != v.Code {
					conflicts = append(conflicts, ConflictError{Key: n + " (" + label + ": " + k + ")", ManagementCode: prev.Code, CapabilityCode: v.Code})
				}
				continue
			}
			dst[n] = v
		}
	}
	m := make(map[string]expectation, len(managementOperations))
	c := make(map[string]expectation, len(capabilityOperations))
	add(m, managementOperations, "management")
	add(c, capabilityOperations, "capability")
	// 跨表归一化冲突：同一 normalized key 管理面与能力面不同码。
	for n, mv := range m {
		if cv, ok := c[n]; ok && cv.Code != mv.Code {
			conflicts = append(conflicts, ConflictError{Key: n + " (cross-table normalized)", ManagementCode: mv.Code, CapabilityCode: cv.Code})
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Key < conflicts[j].Key })
	return m, c, conflicts
}

// Conflicts 返回两张权威表对同一键返回不同码的项（生成物已在生成期校验，此处为运行时自检）。
func Conflicts() []ConflictError {
	out := append([]ConflictError(nil), normalizedConflicts...)
	for k, m := range managementOperations {
		if c, ok := capabilityOperations[k]; ok && c.Code != m.Code {
			out = append(out, ConflictError{Key: k, ManagementCode: m.Code, CapabilityCode: c.Code})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Resolve 按 (method, path) 查两张权威表取期望码。
//
// 精确匹配优先；未命中则按"参数段名归一化"（{x} == {y}）再匹配，以吸收 proto
// 蛇形参数名与生成/库内驼峰参数名的差异（如 /users/{user_id} vs /users/{userId}）。
//
// 返回：
//   - (*Expectation, nil)：命中，取该码；
//   - (nil, *ConflictError)：两张表同键不同码，必须报冲突停止；
//   - (nil, *MissingError)：均未命中，标记缺项。
func Resolve(method, path string) (*Expectation, error) {
	k := key(method, path)
	m, hasM := managementOperations[k]
	c, hasC := capabilityOperations[k]
	if !hasM && !hasC {
		n := nkey(method, path)
		m, hasM = normalizedManagement[n]
		c, hasC = normalizedCapability[n]
	}
	if hasM && hasC && m.Code != c.Code {
		return nil, &ConflictError{Key: k, ManagementCode: m.Code, CapabilityCode: c.Code}
	}
	if hasM {
		return &Expectation{Method: strings.ToUpper(strings.TrimSpace(method)), Path: path, Code: m.Code,
			Source: SourceManagement, Operation: m.Operation, BusinessModule: m.BusinessModule, ScopeKind: m.ScopeKind}, nil
	}
	if hasC {
		return &Expectation{Method: strings.ToUpper(strings.TrimSpace(method)), Path: path, Code: c.Code,
			Source: SourceCapability, BusinessModule: c.BusinessModule, ScopeKind: c.ScopeKind}, nil
	}
	return nil, &MissingError{Key: k}
}

// Lookup 是 Resolve 的简化形式：命中返回码，未命中或冲突返回 ok=false。
// 供聚合层批量取码使用；调用方需自行区分"缺项"与"冲突"时使用 Resolve。
func Lookup(method, path string) (string, bool) {
	exp, err := Resolve(method, path)
	if err != nil || exp == nil {
		return "", false
	}
	return exp.Code, true
}

// IsExempt 报告 (method, path) 是否属于独立认证/会话、特殊传输或业务域接口
// （规范 §4.1：采用其自身认证合同，不适用业务推导）。对齐入口据此与"缺项"区别处理。
// 同样支持参数段名归一化匹配。
func IsExempt(method, path string) (string, bool) {
	k := key(method, path)
	if reason, ok := exemptReasons[k]; ok {
		return reason, true
	}
	reason, ok := exemptReasonsNormalized[nkey(method, path)]
	return reason, ok
}

// AllKeys 返回两张权威表的全部 (METHOD path) 键（并集、去重、排序）。
// 供对齐入口遍历"本批纳入范围"，不包含任何范围外的业务/认证路由。
func AllKeys() []string {
	set := make(map[string]struct{}, len(managementOperations)+len(capabilityOperations))
	for k := range managementOperations {
		set[k] = struct{}{}
	}
	for k := range capabilityOperations {
		set[k] = struct{}{}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SplitKey 拆分 AllKeys 返回的键为方法与路径。
func SplitKey(k string) (method, path string) {
	i := strings.IndexByte(k, ' ')
	if i < 0 {
		return k, ""
	}
	return k[:i], k[i+1:]
}

// ManagementCount 返回管理面权威表条目数（供 dry-run 摘要与测试）。
func ManagementCount() int { return len(managementOperations) }

// CapabilityCount 返回各业务域/Quota 权威表条目数。
func CapabilityCount() int { return len(capabilityOperations) }
