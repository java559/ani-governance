package bootstrap

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"

	"go-wind-admin/pkg/permcode"
)

// This is a fresh-install manifest. It is never replayed by sync-apis or by
// Initialize on an installation that already has MarkerKey.
//
//go:embed console_boss_initial.json
var initialSeedJSON []byte

const ConsoleBossMarkerKey = "deployment.bootstrap.console-boss.v1"

type initialGroup struct{ Key, Name, Module, Parent string }
type initialPermission struct {
	Code, Name, Group string
	Roles             []string
}
type initialRoute struct {
	Method, Path, Operation, Module, Authentication string
	Permissions                                     []string
}
type initialMenu struct {
	Key, Name, Path, Module, Client, Parent string
	Permissions                             []string
}
type initialRoleTemplate struct {
	Code, Name, Scope, Description string
	TemplateFor                    string `json:"template_for"`
	Permissions                    []string
}
type initialSeed struct {
	Version         string
	Source          map[string]string
	Groups          []initialGroup
	Permissions     []initialPermission
	Routes          []initialRoute
	ExcludedRoutes  []initialRoute `json:"excluded_routes"`
	Menus           []initialMenu
	FullPlanModules []string              `json:"full_plan_modules"`
	RoleTemplates   []initialRoleTemplate `json:"role_templates"`
}

var permissionCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(:[a-z][a-z0-9_-]*)+$`)

func loadInitialSeed(catalog []API) (*initialSeed, error) {
	var seed initialSeed
	if err := json.Unmarshal(initialSeedJSON, &seed); err != nil {
		return nil, fmt.Errorf("load initial seed: %w", err)
	}
	if err := seed.validate(catalog); err != nil {
		return nil, err
	}
	return &seed, nil
}

func (s *initialSeed) validate(catalog []API) error {
	fail := func(format string, args ...any) error { return fmt.Errorf("initial seed: "+format, args...) }
	if s.Version != "console-boss-v1" || len(s.Routes) != 531 {
		return fail("invalid release manifest")
	}
	groups := map[string]bool{}
	for _, g := range s.Groups {
		if g.Key == "" || g.Name == "" || groups[g.Key] || (g.Parent != "" && !groups[g.Parent]) {
			return fail("invalid or unordered group %q", g.Key)
		}
		groups[g.Key] = true
	}
	permissions := map[string]bool{}
	tenantCodes := map[string]bool{}
	platformCodes := map[string]bool{}
	for _, p := range s.Permissions {
		if !permissionCodePattern.MatchString(p.Code) || permissions[p.Code] || !groups[p.Group] || p.Name == "" || len(p.Roles) == 0 {
			return fail("invalid permission %q", p.Code)
		}
		roles := map[string]bool{}
		for _, role := range p.Roles {
			if (role != "platform" && role != "tenant") || roles[role] {
				return fail("invalid role on %s", p.Code)
			}
			roles[role] = true
		}
		permissions[p.Code] = true
		tenantCodes[p.Code] = roles["tenant"]
		platformCodes[p.Code] = roles["platform"]
	}
	if len(s.RoleTemplates) != 3 {
		return fail("expected three additional role templates")
	}
	templateScopes := map[string]string{
		"template:platform:operator": "PLATFORM",
		"template:tenant:developer":  "TENANT",
		"template:tenant:viewer":     "TENANT",
	}
	templates := map[string]bool{}
	for _, template := range s.RoleTemplates {
		expectedScope, known := templateScopes[template.Code]
		if !known || template.Name == "" || template.Description == "" || expectedScope != template.Scope || templates[template.Code] || "template:"+template.TemplateFor != template.Code || len(template.Permissions) == 0 {
			return fail("invalid role template %q", template.Code)
		}
		seen := map[string]bool{}
		for _, code := range template.Permissions {
			if !permissions[code] || seen[code] || (template.Scope == "TENANT" && !tenantCodes[code]) || (template.Scope == "PLATFORM" && !platformCodes[code]) {
				return fail("invalid permission %s on role template %s", code, template.Code)
			}
			seen[code] = true
		}
		templates[template.Code] = true
	}
	actual := map[string]API{}
	for _, a := range catalog {
		key := a.Method + " " + a.Path
		if _, exists := actual[key]; exists {
			return fail("duplicate API %s", key)
		}
		actual[key] = a
	}
	expected := map[string]bool{}
	for _, r := range s.Routes {
		key := r.Method + " " + r.Path
		a, exists := actual[key]
		if !exists || a.Operation != r.Operation || a.BusinessModule != r.Module {
			return fail("API catalog is incomplete or differs from release: %s", key)
		}
		if expected[key] {
			return fail("duplicate route %s", key)
		}
		expected[key] = true
		if len(r.Permissions) == 0 {
			// These ten fixed routes implement their own authentication/session flow.
			// IsExempt alone is insufficient: it also lists business and stream APIs.
			allowed := map[string]bool{
				"GET /api/v1/auth/captcha": true, "POST /api/v1/auth/captcha/verify": true,
				"POST /api/v1/auth/csrf": true, "POST /api/v1/auth/forgot-password": true,
				"POST /api/v1/auth/invitations/accept": true, "POST /api/v1/auth/mfa/verify": true,
				"POST /api/v1/auth/password/login": true, "POST /api/v1/auth/platform/password/login": true,
				"POST /api/v1/auth/refresh": true, "POST /api/v1/auth/reset-password-by-code": true,
			}
			if r.Authentication != "independent" || !allowed[key] {
				return fail("ungranted route %s", key)
			}
		} else if r.Authentication != "" {
			return fail("ambiguous authentication for %s", key)
		}
		routeCodes := map[string]bool{}
		for _, c := range r.Permissions {
			if !permissions[c] || routeCodes[c] {
				return fail("invalid permission %s on %s", c, key)
			}
			routeCodes[c] = true
		}
		if e, err := permcode.Resolve(r.Method, r.Path); err == nil {
			if !routeCodes[e.Code] {
				return fail("authoritative permission %s missing on %s", e.Code, key)
			}
			if e.ScopeKind == "platform" {
				for c := range routeCodes {
					if tenantCodes[c] {
						return fail("platform route granted to tenant: %s", key)
					}
				}
			}
		} else if _, conflict := err.(*permcode.ConflictError); conflict {
			return fail("%v", err)
		}
	}
	// Unintegrated VM image endpoints remain in the binary, but must not be
	// initialized or granted until their frontend flow is ready.
	for _, r := range s.ExcludedRoutes {
		key := r.Method + " " + r.Path
		a, exists := actual[key]
		if !exists || expected[key] || a.Operation != r.Operation || a.BusinessModule != r.Module || a.Module != "VMImageService" || len(r.Permissions) != 0 || r.Authentication != "" {
			return fail("invalid excluded route %s", key)
		}
		expected[key] = true
	}
	if len(actual) != len(expected) {
		return fail("expected exactly %d catalog APIs, got %d", len(expected), len(actual))
	}
	menus := map[string]bool{}
	modules := map[string]bool{}
	for _, m := range s.FullPlanModules {
		if m == "" || modules[m] {
			return fail("invalid plan module %q", m)
		}
		modules[m] = true
	}
	for _, m := range s.Menus {
		if m.Key == "" || menus[m.Key] || m.Name == "" || m.Path == "" || (m.Client != "console" && m.Client != "boss") || (m.Parent != "" && !menus[m.Parent]) || (m.Module != "" && !modules[m.Module]) || len(m.Permissions) == 0 {
			return fail("invalid menu %q", m.Key)
		}
		for _, c := range m.Permissions {
			if !permissions[c] {
				return fail("menu %s has unknown permission %s", m.Key, c)
			}
		}
		menus[m.Key] = true
	}
	return nil
}

// catalogForInit is called only after validation. Excluded endpoints are not
// written to a fresh database, while explicit sync-apis still sees the binary's
// complete catalog and never grants permissions automatically.
func (s *initialSeed) catalogForInit(catalog []API) []API {
	allowed := make(map[string]bool, len(s.Routes))
	for _, r := range s.Routes {
		allowed[r.Method+" "+r.Path] = true
	}
	result := make([]API, 0, len(s.Routes))
	for _, a := range catalog {
		if allowed[a.Method+" "+a.Path] {
			result = append(result, a)
		}
	}
	return result
}

// apply runs only after the frozen base seed has proved the database was empty,
// under Initialize's advisory lock and transaction. Replacing base menu/grant
// scaffolding here never constitutes an upgrade of an existing installation.
func (s *initialSeed) apply(ctx context.Context, tx *sql.Tx) error {
	var apiCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sys_apis`).Scan(&apiCount); err != nil {
		return err
	}
	if apiCount != len(s.Routes) {
		return fmt.Errorf("fresh installation must contain exactly %d APIs; found %d (review extra APIs explicitly)", len(s.Routes), apiCount)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sys_role_permissions; DELETE FROM sys_permission_apis; DELETE FROM sys_permission_menus; DELETE FROM sys_menus`); err != nil {
		return err
	}
	groups := map[string]int64{}
	paths := map[string]string{}
	for i, g := range s.Groups {
		var id int64
		var parent any
		if g.Parent != "" {
			parent = groups[g.Parent]
		}
		if g.Key == "sys" {
			if err := tx.QueryRowContext(ctx, `SELECT id FROM sys_permission_groups WHERE module='sys'`).Scan(&id); err != nil {
				return err
			}
		} else if err := tx.QueryRowContext(ctx, `INSERT INTO sys_permission_groups(name,module,parent_id,path,status,sort_order,created_at) VALUES($1,$2,$3,'/','ON',$4,NOW()) RETURNING id`, g.Name, g.Module, parent, i+1).Scan(&id); err != nil {
			return err
		}
		path := paths[g.Parent] + fmt.Sprintf("%d/", id)
		if g.Parent == "" {
			path = "/" + path
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sys_permission_groups SET path=$1 WHERE id=$2`, path, id); err != nil {
			return err
		}
		groups[g.Key] = id
		paths[g.Key] = path
	}
	roles := map[string]int64{}
	for alias, code := range map[string]string{"platform": "platform:admin", "tenant": "template:tenant:manager"} {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM sys_roles WHERE code=$1 AND tenant_id=0`, code).Scan(&id); err != nil {
			return err
		}
		roles[alias] = id
	}
	permissions := map[string]int64{}
	for _, p := range s.Permissions {
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO sys_permissions(name,code,group_id,status,created_at) VALUES($1,$2,$3,'ON',NOW()) ON CONFLICT(code) DO UPDATE SET name=EXCLUDED.name,group_id=EXCLUDED.group_id RETURNING id`, p.Name, p.Code, groups[p.Group]).Scan(&id)
		if err != nil {
			return err
		}
		permissions[p.Code] = id
		for _, role := range p.Roles {
			if _, err = tx.ExecContext(ctx, `INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at) VALUES(0,$1,$2,'ALLOW','ON',NOW())`, roles[role], id); err != nil {
				return err
			}
		}
	}
	for _, template := range s.RoleTemplates {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO sys_roles(tenant_id,name,code,status,type,is_protected,data_scope,description,created_at) VALUES(0,$1,$2,'ON','TEMPLATE',true,'ALL',$3,NOW()) RETURNING id`, template.Name, template.Code, template.Description).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sys_role_metadata(tenant_id,role_id,is_template,template_for,template_version,scope,sync_policy,custom_overrides,created_at) VALUES(0,$1,true,$2,1,$3,'MANUAL','{}',NOW())`, id, template.TemplateFor, template.Scope); err != nil {
			return err
		}
		for _, code := range template.Permissions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at) VALUES(0,$1,$2,'ALLOW','ON',NOW())`, id, permissions[code]); err != nil {
				return err
			}
		}
	}
	for _, r := range s.Routes {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM sys_apis WHERE method=$1 AND path=$2 AND status='ON' AND deleted_at IS NULL`, r.Method, r.Path).Scan(&id); err != nil {
			return err
		}
		for _, c := range r.Permissions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES($1,$2,NOW())`, permissions[c], id); err != nil {
				return err
			}
		}
	}
	menus := map[string]int64{}
	for i, m := range s.Menus {
		var id int64
		var parent, module any
		typ := "CATALOG"
		if m.Parent != "" {
			parent = menus[m.Parent]
			typ = "MENU"
		}
		if m.Module != "" {
			module = m.Module
		}
		meta, err := json.Marshal(map[string]any{"title": m.Name, "authority": m.Permissions, "order": i + 1})
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO sys_menus(name,parent_id,path,component,type,status,module,meta,remark,created_at) VALUES($1,$2,$3,'',$4,'ON',$5,$6::jsonb,$7,NOW()) RETURNING id`, m.Key, parent, m.Path, typ, module, string(meta), m.Client+": "+m.Name).Scan(&id)
		if err != nil {
			return err
		}
		menus[m.Key] = id
		for _, c := range m.Permissions {
			if _, err = tx.ExecContext(ctx, `INSERT INTO sys_permission_menus(permission_id,menu_id,created_at) VALUES($1,$2,NOW())`, permissions[c], id); err != nil {
				return err
			}
		}
	}
	var planID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO sys_plans(name,version,expiry_policy,description,created_at) VALUES('完整功能模板','ENTERPRISE','READONLY','全部已接入业务模块模板；不自动分配租户，资源配额由交付时另行配置',NOW()) RETURNING id`).Scan(&planID); err != nil {
		return err
	}
	for _, module := range s.FullPlanModules {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sys_plan_modules(plan_id,module,created_at) VALUES($1,$2,NOW())`, planID, module); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sys_role_metadata SET template_version=2 WHERE role_id=$1 AND tenant_id=0`, roles["tenant"]); err != nil {
		return err
	}
	source, err := json.Marshal(s.Source)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sys_configs(key,name,value,value_type,is_built_in,created_at) VALUES($1,'Console/BOSS initial seed',$2,'JSON',true,NOW())`, ConsoleBossMarkerKey, string(source))
	return err
}
