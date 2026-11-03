package server

import (
	view "go-wind-admin/api/gen/go/instance/service/v1"
	vmview "go-wind-admin/api/gen/go/vm/service/v1"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityGroupSelectionRequiresExplicitArray(t *testing.T) {
	for _, test := range []struct {
		body  string
		valid bool
	}{
		{`{"data":{"security_group_ids":[]}}`, true},
		{`{"data":{"security_group_ids":["sg_0123456789abcdef0123456789abcdef"]}}`, true},
		{`{"data":{}}`, false}, {`{"data":{"security_group_ids":null}}`, false},
		{`{"data":{"security_group_ids":"sg_0123456789abcdef0123456789abcdef"}}`, false},
		{`{"data":{"security_group_ids":[],"security_group_ids":["foreign"]}}`, false},
	} {
		err := validateSecurityGroupSelectionJSON([]byte(test.body))
		if (err == nil) != test.valid {
			t.Fatalf("body %s validity: %v", test.body, err)
		}
		request := httptest.NewRequest("PUT", "/api/v1/instances/id/security-groups", strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		err = decodeInstanceHTTP(request, new(view.SetInstanceSecurityGroupsRequest))
		if (err == nil) != test.valid {
			t.Fatalf("instance decoder body %s validity: %v", test.body, err)
		}
	}
}
func TestInstanceAndVMCreateAcceptSecurityGroupSelection(t *testing.T) {
	body := `{"data":{"name":"workload","security_group_ids":["sg_0123456789abcdef0123456789abcdef"]}}`
	instance := new(view.CreateInstanceRequest)
	request := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if err := decodeInstanceHTTP(request, instance); err != nil || len(instance.GetData().GetSecurityGroupIds()) != 1 {
		t.Fatal("instance selection lost", err)
	}
	vm := new(vmview.CreateVMRequest)
	request = httptest.NewRequest("POST", "/api/v1/virtual-machines", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if err := decodeStrictJSONHTTP(request, vm); err != nil || len(vm.GetData().GetSecurityGroupIds()) != 1 {
		t.Fatal("VM selection lost", err)
	}
}
