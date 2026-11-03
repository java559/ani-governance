package permcode

import "testing"

func TestModelDevEnvironmentRoutesUseIndependentFrozenPermissions(t *testing.T) {
	for _, tc := range []struct{ method, path, code string }{{"POST", "/api/v1/modeldev/environment:ensure", "modeldev:manage_environment"}, {"GET", "/api/v1/modeldev/environment", "modeldev:view_environment"}} {
		got, err := Resolve(tc.method, tc.path)
		if err != nil || got.Code != tc.code || got.BusinessModule != "MODEL" || got.ScopeKind != "tenant" {
			t.Fatalf("%s %s: %+v %v", tc.method, tc.path, got, err)
		}
	}
	if _, err := Resolve("POST", "/api/v1/modeldev/environment"); err == nil {
		t.Fatal("unregistered method received an environment permission")
	}
}
