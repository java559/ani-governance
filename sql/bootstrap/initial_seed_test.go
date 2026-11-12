package bootstrap

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go-wind-admin/pkg/middleware/auth"
)

func releaseSeed(t *testing.T) (*initialSeed, []API) {
	t.Helper()
	doc, err := os.ReadFile("../../app/admin/service/cmd/server/assets/openapi.yaml")
	require.NoError(t, err)
	catalog, err := Catalog(doc)
	require.NoError(t, err)
	seed, err := loadInitialSeed(catalog)
	require.NoError(t, err)
	return seed, catalog
}

func TestInitialSeedReleaseCoverage(t *testing.T) {
	seed, catalog := releaseSeed(t)
	require.Len(t, seed.Routes, 509)
	require.Len(t, seed.ExcludedRoutes, 6)
	selected := seed.catalogForInit(catalog)
	require.Len(t, selected, 509)
	for _, api := range selected {
		require.NotEqual(t, "VMImageService", api.Module, api.Path)
	}
	require.Len(t, seed.Permissions, 380)
	require.Len(t, seed.Menus, 51) // 21 Console pages, 28 BOSS pages and two containers.
	require.Len(t, seed.FullPlanModules, 19)
	routes := map[string]initialRoute{}
	independent := 0
	for _, r := range seed.Routes {
		routes[r.Method+" "+r.Path] = r
		if r.Authentication == "independent" {
			independent++
		}
	}
	require.Equal(t, 10, independent)
	// These source policies are newer than the generic permcode table. The seed
	// must preserve the exact capability consumed by the real platform handler.
	for _, op := range auth.PlatformNetworkOperations {
		r, ok := routes[op.Method+" "+op.Path]
		if ok {
			require.Contains(t, r.Permissions, op.Permission, r.Path)
		}
	}
	for _, op := range auth.ClusterOpsOperations {
		r, ok := routes[op.Method+" "+op.Path]
		require.True(t, ok, op.Path)
		require.Equal(t, "SYSTEM", r.Module)
		require.Equal(t, []string{op.Permission}, r.Permissions, r.Path)
	}
	require.ElementsMatch(t, []string{"instance:preset:read", "modeldev:compute-preset:read", "vm:compute-preset:read", "inference:compute-preset:read", "sandbox:preset:read"}, routes["GET /api/v1/compute-presets"].Permissions)
	require.Equal(t, []string{"modeldev:dataset:read"}, routes["GET /api/v1/modeldev/input-versions"].Permissions, "dataset migration supersedes the legacy input grant")
	require.Equal(t, "MODELDEV", routes["GET /api/v1/modeldev/input-versions"].Module)
	require.Equal(t, "SANDBOX", routes["GET /api/v1/sandbox"].Module)
	require.Equal(t, []string{"network:vpc:get"}, routes["GET /api/v1/networks/vpcs/{vpc_id}/associated-instances"].Permissions)
	require.Equal(t, []string{"network:subnet:get"}, routes["GET /api/v1/networks/subnets/{subnet_id}/associated-instances"].Permissions)
	require.Equal(t, []string{"modeldev:manage_execution"}, routes["POST /api/v1/modeldev/executions/{execution_id}:resume-model-registration"].Permissions)
	clusterCodes := map[string]bool{}
	for _, p := range seed.Permissions {
		if p.Group == "boss.cluster-ops" {
			clusterCodes[p.Code] = true
			require.Equal(t, []string{"platform"}, p.Roles, p.Code)
		}
	}
	require.Len(t, clusterCodes, 7)
	for _, action := range []string{"read", "create", "update", "delete", "scale", "restart", "logs"} {
		require.True(t, clusterCodes["cluster:ops:"+action], action)
	}
	for _, template := range seed.RoleTemplates {
		for _, code := range template.Permissions {
			require.False(t, clusterCodes[code], template.Code+": "+code)
		}
	}
	for _, p := range seed.Permissions {
		require.Regexp(t, `[一-龥]`, p.Name, p.Code)
	}
}

func TestInitialSeedRejectsIncompleteOrExpandedRelease(t *testing.T) {
	_, catalog := releaseSeed(t)
	_, err := loadInitialSeed(catalog[:len(catalog)-1])
	require.ErrorContains(t, err, "API catalog is incomplete")
	expanded := append(append([]API{}, catalog...), API{Method: "GET", Path: "/api/v1/extra", Operation: "Extra_Get", BusinessModule: "SYSTEM"})
	_, err = loadInitialSeed(expanded)
	require.ErrorContains(t, err, "expected exactly 515")
	changed := append([]API{}, catalog...)
	changed[0].BusinessModule = "SYSTEM"
	if changed[0].BusinessModule == catalog[0].BusinessModule {
		changed[0].Operation = "Other_Get"
	}
	_, err = loadInitialSeed(changed)
	require.ErrorContains(t, err, "differs from release")
}

func TestInitialSeedRejectsAuthorizationGaps(t *testing.T) {
	_, catalog := releaseSeed(t)
	for _, scenario := range []struct {
		name   string
		mutate func(*initialSeed)
	}{
		{"template cannot cross scope", func(s *initialSeed) {
			s.RoleTemplates[1].Permissions = append(s.RoleTemplates[1].Permissions, "network:platform:public-pool:create")
		}},
		{"template cannot use unknown permission", func(s *initialSeed) {
			s.RoleTemplates[0].Permissions = append(s.RoleTemplates[0].Permissions, "unknown:grant")
		}},
		{"template needs valid metadata", func(s *initialSeed) { s.RoleTemplates[2].TemplateFor = "platform:admin" }},
		{"excluded route cannot grant", func(s *initialSeed) { s.ExcludedRoutes[0].Permissions = []string{"vm:read"} }},
		{"business route cannot be excluded", func(s *initialSeed) { s.ExcludedRoutes[0] = s.Routes[0] }},
		{"business exempt is not public", func(s *initialSeed) {
			for i := range s.Routes {
				r := &s.Routes[i]
				if r.Path == "/api/v1/modeldev/executions" && r.Method == "POST" {
					r.Permissions = nil
					r.Authentication = "independent"
				}
			}
		}},
		{"unknown grant", func(s *initialSeed) { s.Routes[0].Permissions = []string{"test:missing"} }},
		{"platform grant on tenant template", func(s *initialSeed) {
			for i := range s.Permissions {
				if s.Permissions[i].Code == "network:platform:public-pool:create" {
					s.Permissions[i].Roles = []string{"tenant"}
				}
			}
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var seed initialSeed
			require.NoError(t, json.Unmarshal(initialSeedJSON, &seed))
			scenario.mutate(&seed)
			require.Error(t, seed.validate(catalog))
		})
	}
}

func TestInitialSeedAdministratorAPICoverage(t *testing.T) {
	seed, _ := releaseSeed(t)
	roles := map[string]map[string]bool{"platform": {}, "tenant": {}}
	for _, permission := range seed.Permissions {
		for _, role := range permission.Roles {
			roles[role][permission.Code] = true
		}
	}
	coverage := map[string]int{}
	shared, independent := 0, 0
	for _, route := range seed.Routes {
		if route.Authentication == "independent" {
			independent++
			continue
		}
		matched := map[string]bool{}
		for role, permissions := range roles {
			for _, permission := range route.Permissions {
				if permissions[permission] {
					matched[role] = true
				}
			}
			if matched[role] {
				coverage[role]++
			}
		}
		require.NotEmpty(t, matched, route.Method+" "+route.Path)
		if len(matched) == 2 {
			shared++
		}
	}
	require.Equal(t, map[string]int{"platform": 273, "tenant": 269}, coverage)
	require.Equal(t, 43, shared)
	require.Equal(t, 10, independent)
	require.Equal(t, 509, coverage["platform"]+coverage["tenant"]-shared+independent)
}

func TestInitialSeedAdditionalRoleTemplateBoundaries(t *testing.T) {
	seed, _ := releaseSeed(t)
	templates := map[string]map[string]bool{}
	for _, template := range seed.RoleTemplates {
		templates[template.Code] = map[string]bool{}
		for _, code := range template.Permissions {
			templates[template.Code][code] = true
		}
	}
	ops, developer, viewer := templates["template:platform:operator"], templates["template:tenant:developer"], templates["template:tenant:viewer"]
	require.Len(t, ops, 106)
	require.Len(t, developer, 179)
	require.Len(t, viewer, 93)
	for _, code := range []string{"role:create", "permission:edit", "sys:platform_admin", "tenant:create", "plan:edit", "sys:reset_others_credential"} {
		require.False(t, ops[code], code)
	}
	for _, code := range []string{"sys:tenant_manager", "user:create", "role:edit", "network:platform:public-pool:create"} {
		require.False(t, developer[code], code)
	}
	for _, code := range []string{"vm:create", "instance:create", "storage:volume:create", "instance:pvc:attach", "inference:key:read", "image:credential:get", "instance:exec", "vm:console"} {
		require.False(t, viewer[code], code)
	}
	for _, grants := range []map[string]bool{ops, developer, viewer} {
		require.True(t, grants["auth:logout"], "logout must not require administrator/API-key management permission")
	}
	// POST metric queries and object metadata/download queries are read operations.
	// The other exceptions are current-user account/security and inbox operations.
	safePost := map[string]bool{
		"POST /api/v1/gpu/resources/{resourceKind}/{resourceId}/metrics/query": true,
		"POST /api/v1/gpu/metrics/query":                                       true,
		"POST /api/v1/instances/{instance_id}/metrics/query":                   true,
		"POST /api/v1/storage/buckets/{data.bucket_id}/objects:head":           true,
		"POST /api/v1/storage/buckets/{data.bucket_id}/objects:download-url":   true,
	}
	self := map[string]bool{}
	for _, permission := range seed.Permissions {
		if permission.Group == "common.identity" || permission.Group == "common.message" || permission.Code == "auth:logout" {
			self[permission.Code] = true
		}
	}
	for _, route := range seed.Routes {
		for _, code := range route.Permissions {
			if viewer[code] && route.Method != "GET" && !self[code] {
				require.True(t, safePost[route.Method+" "+route.Path], code+": "+route.Method+" "+route.Path)
			}
		}
	}
}

// Fresh installs must use the same retired-route boundary as running v1.1 APIs.
func TestInitialSeedAcceleratorV11Capabilities(t *testing.T) {
	seed, _ := releaseSeed(t)
	routes := map[string]initialRoute{}
	permissions := map[string]initialPermission{}
	for _, r := range seed.Routes {
		routes[r.Method+" "+r.Path] = r
	}
	for _, p := range seed.Permissions {
		permissions[p.Code] = p
	}
	for _, path := range []string{"GET /api/v1/accelerator/capacity", "GET /api/v1/admin/accelerator/capacity", "POST /api/v1/accelerator/admission-preview", "GET /api/v1/admin/accelerator/bindings", "GET /api/v1/accelerator/usages", "GET /api/v1/accelerator/usages/{owner_service}/{resource_id}", "GET /api/v1/accelerator/usages/{owner_service}/{resource_id}/bindings"} {
		require.NotContains(t, routes, path)
	}
	for _, code := range []string{"accelerator:admission:preview", "accelerator:binding:admin-list", "accelerator:binding:list", "accelerator:capacity:admin-get", "accelerator:capacity:get"} {
		require.NotContains(t, permissions, code)
	}
	for path, code := range map[string]string{"GET /api/v1/admin/accelerator/profiles/{profile_id}/capacity": "accelerator:profile:capacity-read", "GET /api/v1/admin/accelerator/supply-groups/{group_id}/capacity": "accelerator:supply-group:capacity-read"} {
		require.Equal(t, []string{code}, routes[path].Permissions)
		require.Equal(t, []string{"platform"}, permissions[code].Roles)
		for _, role := range seed.RoleTemplates {
			if role.Scope == "TENANT" {
				require.NotContains(t, role.Permissions, code)
			}
		}
	}
	require.Contains(t, permissions, "accelerator:usage:get")
	for path, code := range map[string]string{
		"GET /api/v1/admin/accelerator/supply-configurations":                  "accelerator:discovery:list",
		"POST /api/v1/admin/accelerator/supply-groups:preview-policy":          "accelerator:discovery:list",
		"GET /api/v1/admin/accelerator/supply-candidates":                      "accelerator:discovery:list",
		"POST /api/v1/admin/accelerator/supply-registrations":                  "accelerator:supply:register",
		"GET /api/v1/admin/accelerator/supply-registrations/{registration_id}": "accelerator:discovery:list",
	} {
		require.Equal(t, []string{code}, routes[path].Permissions)
		require.Equal(t, []string{"platform"}, permissions[code].Roles)
		for _, role := range seed.RoleTemplates {
			if role.Scope == "TENANT" {
				require.NotContains(t, role.Permissions, code)
			}
		}
	}
	require.Contains(t, permissions, "accelerator:usage:list")
	require.Equal(t, []string{"accelerator:usage:list"}, routes["GET /api/v1/gpu/metrics/latest"].Permissions)
}
