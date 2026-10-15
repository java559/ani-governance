package bootstrap

// permissions_align.go 实现"增量对齐入口" admin permissions-align 的核心逻辑（实施文档 §B5.1）。
//
// 与 SyncAPIs / SyncPermissions 的职责边界：
//   - SyncAPIs：只对齐 sys_apis 目录（按 (method,path) ADD/UPDATE，不含 path），打印 "permissions unchanged"。
//   - SyncPermissions：硬删重建（TruncateBizPermissions），会丢 permission_id 与角色绑定，仅开发重建用。
//   - AlignPermissions：以两张权威表为期望码来源，按 (method,path) 取码、保留既有 permission_id/状态/关联，
//     冲突或缺项整批拒绝并回滚，幂等可重跑。运行时不重新推导，只查表（§B3.3/§B4/§B8.3）。
//
// 写入内容（单事务，任一步失败整批回滚）：
//  1. 期望码在库中不存在 → INSERT 新权限；
//  2. 期望码已存在       → 保留 id/status/group，不重建；
//  3. 库中非 sys: 且不在期望集 → 置 status=OFF（保 id，不删除）；
//     但仅限"绑定了纳入范围（非豁免）路由"的码：绑定豁免路由（独立认证/会话、特殊
//     传输、业务域）或不绑定任何路由的码（如 `modeldev:manage_*` 这类由 CLI 按码
//     直接检查、不依附 HTTP 路由的业务能力）保持不动，不得误停用；
//  4. sys_permission_apis 关联按期望集增/删。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go-wind-admin/pkg/constants"
	"go-wind-admin/pkg/localdeps/go-utils/stringcase"
	"go-wind-admin/pkg/permcode"
)

// permissionRow 是库中一条权限的现状。
type permissionRow struct {
	ID     uint64
	Code   string
	Status string
}

// AlignResult 汇总一次对齐的差异与写入结果。
type AlignResult struct {
	Changes  []string // 人类可读的变更行（dry-run 与实写共用）
	Inserted int
	Disabled int
	Kept     int // 因绑定豁免路由而被保护、未处置的既有码
	Linked   int
	Unlinked int
	Matched  int
}

// AlignPermissions 以权威表为期望码，增量对齐 sys_permissions 与 sys_permission_apis。
//
// dryRun=true 时只计算差异并回滚（调用方用只读事务），不写任何数据。
// 缺项或冲突时返回具名错误，整批拒绝，不写入部分结果。
func AlignPermissions(ctx context.Context, tx *sql.Tx, dryRun bool) (*AlignResult, error) {
	apis, err := loadActiveApis(ctx, tx)
	if err != nil {
		return nil, err
	}

	// 1) 逐条 (method,path) 查表取期望码；缺项/冲突整批拒绝。
	expected := make(map[string][]uint64) // code -> api_id 集合
	// 纳入范围路由的 api_id 集合（Resolve 命中权威表者）。align 的停用判定只针对
	// 绑定这类路由的码；豁免路由（业务域/独立认证）与不依附任何范围路由的码不属本批范围。
	inScopeAPIIDs := make(map[uint64]bool)
	var missing, conflicts []string
	for _, a := range apis {
		exp, rerr := permcode.Resolve(a.method, a.path)
		if rerr != nil {
			var ce *permcode.ConflictError
			if errors.As(rerr, &ce) {
				conflicts = append(conflicts, ce.Error())
			} else if _, exempt := permcode.IsExempt(a.method, a.path); !exempt {
				missing = append(missing, fmt.Sprintf("%s %s", a.method, a.path))
			}
			continue
		}
		inScopeAPIIDs[a.id] = true
		expected[exp.Code] = append(expected[exp.Code], a.id)
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return nil, fmt.Errorf("permission code conflict(s), refusing to write: %s", strings.Join(conflicts, "; "))
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("permission code missing for %d route(s), refusing to write (register explicit mapping or mark exempt): %s",
			len(missing), strings.Join(missing, ", "))
	}

	// 2) 加载库现状。
	perms, err := loadPermissions(ctx, tx)
	if err != nil {
		return nil, err
	}
	links, err := loadPermissionApis(ctx, tx)
	if err != nil {
		return nil, err
	}
	permByCode := make(map[string]permissionRow, len(perms))
	for _, p := range perms {
		permByCode[p.Code] = p
	}

	res := &AlignResult{}

	// 3) 期望码：存在则保留 id/status 并同步关联；不存在则 INSERT + 关联。
	for code, apiIDs := range expected {
		sort.Slice(apiIDs, func(i, j int) bool { return apiIDs[i] < apiIDs[j] })
		if p, ok := permByCode[code]; ok {
			res.Matched++
			want := make(map[uint64]struct{}, len(apiIDs))
			for _, id := range apiIDs {
				want[id] = struct{}{}
				if !links[p.ID][id] {
					res.Changes = append(res.Changes, fmt.Sprintf("LINK %s <- api %d", code, id))
					res.Linked++
					if !dryRun {
						if err = insertLink(ctx, tx, p.ID, id); err != nil {
							return nil, err
						}
					}
				}
			}
			for apiID := range links[p.ID] {
				if _, keep := want[apiID]; keep {
					continue
				}
				res.Changes = append(res.Changes, fmt.Sprintf("UNLINK %s -> api %d", code, apiID))
				res.Unlinked++
				if !dryRun {
					if err = deleteLink(ctx, tx, p.ID, apiID); err != nil {
						return nil, err
					}
				}
			}
			continue
		}
		res.Changes = append(res.Changes, fmt.Sprintf("INSERT permission %s (%d api)", code, len(apiIDs)))
		res.Inserted++
		if dryRun {
			continue
		}
		id, ierr := insertPermission(ctx, tx, code)
		if ierr != nil {
			return nil, ierr
		}
		for _, apiID := range apiIDs {
			if err = insertLink(ctx, tx, id, apiID); err != nil {
				return nil, err
			}
			res.Linked++
		}
	}

	// 4) 库中非 sys:、不在期望集、当前 ON，且绑定纳入范围路由的码 → 置 OFF（保 id）。
	//    仅绑定豁免路由或不绑定任何路由的码保持不动（避免误停用业务域能力，如 modeldev:*）。
	decision := decideDisable(perms, links, inScopeAPIIDs, expected)
	for _, p := range decision.Protect {
		res.Changes = append(res.Changes, fmt.Sprintf("KEEP permission %s (id=%d, out of align scope: no in-scope route binding, left unchanged)", p.Code, p.ID))
		res.Kept++
	}
	for _, p := range decision.Disable {
		res.Changes = append(res.Changes, fmt.Sprintf("DISABLE permission %s (id=%d, kept)", p.Code, p.ID))
		res.Disabled++
		if !dryRun {
			if _, err = tx.ExecContext(ctx, `UPDATE sys_permissions SET status='OFF', updated_at=NOW() WHERE id=$1`, p.ID); err != nil {
				return nil, fmt.Errorf("disable permission %s: %w", p.Code, err)
			}
		}
	}

	sort.Strings(res.Changes)
	return res, nil
}

// disableDecision 是步骤 4 的判定结果：应置 OFF 的权限，以及因超出 align 处置范围而被保护的权限。
type disableDecision struct {
	Disable []permissionRow
	Protect []permissionRow
}

// decideDisable 判定库中哪些权限应置 OFF（纯函数，不触碰数据库，便于单元测试）。
//
// align 的处置范围限于"纳入范围（Resolve 命中权威表）路由"的派生码，故规则为：
//   - `sys:` 保留前缀的权限不处置；
//   - 已在期望集中的码不处置（步骤 3 已保留其 id/状态并按需同步关联）；
//   - 当前非 `ON` 的码不处置；
//   - 未绑定任何纳入范围路由的码不处置，计入 Protect。涵盖两类：
//     a) 只绑定豁免路由（独立认证/会话、特殊传输、业务域，如 modeldev 查询接口）的码；
//     b) 不绑定任何路由、由 CLI 等按码直接检查的业务能力（如 `modeldev:manage_release_binding`）。
//     误停用会直接破坏功能；漏停用只是留下一个未被任何范围路由使用的码（不授予访问），风险更低。
//   - 其余（绑定纳入范围路由且码不在期望集）置 OFF（保 id）。
func decideDisable(perms []permissionRow, links map[uint64]map[uint64]bool,
	inScopeAPIIDs map[uint64]bool, expected map[string][]uint64) disableDecision {
	var d disableDecision
	for _, p := range perms {
		if strings.HasPrefix(p.Code, constants.SystemPermissionCodePrefix) {
			continue
		}
		if _, ok := expected[p.Code]; ok {
			continue
		}
		if p.Status != "ON" {
			continue
		}
		if boundToInScopeRoute(links[p.ID], inScopeAPIIDs) {
			d.Disable = append(d.Disable, p)
			continue
		}
		d.Protect = append(d.Protect, p)
	}
	return d
}

// boundToInScopeRoute 报告权限绑定的接口中是否有任一条属纳入范围路由。
func boundToInScopeRoute(bound, inScopeAPIIDs map[uint64]bool) bool {
	for apiID := range bound {
		if inScopeAPIIDs[apiID] {
			return true
		}
	}
	return false
}

type apiRow struct {
	id     uint64
	method string
	path   string
}

func loadActiveApis(ctx context.Context, tx *sql.Tx) ([]apiRow, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(method,''), COALESCE(path,'') FROM sys_apis
		 WHERE deleted_at IS NULL AND status='ON' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apiRow
	for rows.Next() {
		var a apiRow
		if err = rows.Scan(&a.id, &a.method, &a.path); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func loadPermissions(ctx context.Context, tx *sql.Tx) ([]permissionRow, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(code,''), COALESCE(status,'ON') FROM sys_permissions
		 WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []permissionRow
	for rows.Next() {
		var p permissionRow
		if err = rows.Scan(&p.ID, &p.Code, &p.Status); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// loadPermissionApis 返回 permission_id -> api_id 集合。
func loadPermissionApis(ctx context.Context, tx *sql.Tx) (map[uint64]map[uint64]bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT permission_id, api_id FROM sys_permission_apis WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]map[uint64]bool{}
	for rows.Next() {
		var pid, aid uint64
		if err = rows.Scan(&pid, &aid); err != nil {
			return nil, err
		}
		if out[pid] == nil {
			out[pid] = map[uint64]bool{}
		}
		out[pid][aid] = true
	}
	return out, rows.Err()
}

func insertPermission(ctx context.Context, tx *sql.Tx, code string) (uint64, error) {
	name := stringcase.ToPascalCase(strings.ReplaceAll(code, ":", "_"))
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sys_permissions(name, code, status, created_at) VALUES($1,$2,'ON',NOW())
		 ON CONFLICT (code) DO NOTHING`, name, code); err != nil {
		return 0, fmt.Errorf("insert permission %s: %w", code, err)
	}
	var id uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM sys_permissions WHERE code=$1 AND deleted_at IS NULL`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("select permission id for %s: %w", code, err)
	}
	return id, nil
}

func insertLink(ctx context.Context, tx *sql.Tx, permissionID, apiID uint64) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sys_permission_apis(permission_id, api_id, created_at) VALUES($1,$2,NOW())
		 ON CONFLICT (permission_id, api_id) DO NOTHING`, permissionID, apiID); err != nil {
		return fmt.Errorf("link permission %d api %d: %w", permissionID, apiID, err)
	}
	return nil
}

func deleteLink(ctx context.Context, tx *sql.Tx, permissionID, apiID uint64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sys_permission_apis WHERE permission_id=$1 AND api_id=$2`, permissionID, apiID); err != nil {
		return fmt.Errorf("unlink permission %d api %d: %w", permissionID, apiID, err)
	}
	return nil
}
