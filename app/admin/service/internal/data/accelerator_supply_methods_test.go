package data

import (
	"strings"
	"testing"
)

func TestAcceleratorSupplyMethodsRequireExactRegistration(t *testing.T) {
	for _, method := range []string{
		"/accelerator.v1.AcceleratorAdminService/ListSupplyCandidates",
		"/accelerator.v1.AcceleratorAdminService/RegisterSupplyConfiguration",
		"/accelerator.v1.AcceleratorAdminService/GetSupplyRegistration",
	} {
		if got := acceleratorMethodKind(method); got != "admin" {
			t.Fatalf("registered method %s has delegation kind %q", method, got)
		}
		for _, unknown := range []string{method + "Other", method + "/", "/other.v1.AcceleratorAdminService/" + method[strings.LastIndexByte(method, '/')+1:]} {
			if got := acceleratorMethodKind(unknown); got != "" {
				t.Fatalf("unregistered method %s accepted with kind %q", unknown, got)
			}
		}
	}
}
