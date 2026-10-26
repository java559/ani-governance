package server

import (
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVMHTTPErrorPreservesVMContractReasons(t *testing.T) {
	withReason, err := status.New(codes.FailedPrecondition, "private native detail").WithDetails(&errdetails.ErrorInfo{Reason: "VM_DEPENDENCY_CHANGED"})
	require.NoError(t, err)
	for _, test := range []struct {
		err    error
		code   int
		reason string
	}{
		{status.Error(codes.Aborted, "VM_VERSION_CONFLICT"), 409, "VM_VERSION_CONFLICT"},
		{status.Error(codes.NotFound, "VM_NOT_FOUND"), 404, "VM_NOT_FOUND"},
		{withReason.Err(), 409, "VM_DEPENDENCY_CHANGED"},
		{kratoserrors.Conflict("IDEMPOTENCY_CONFLICT", "private ledger details"), 409, "VM_IDEMPOTENCY_CONFLICT"},
		{status.Error(codes.Unavailable, "private address"), 503, "VM_DEPENDENCY_NOT_READY"},
	} {
		result := vmHTTPError(test.err)
		require.Equal(t, test.code, kratoserrors.Code(result))
		require.Equal(t, test.reason, kratoserrors.Reason(result))
		require.NotContains(t, result.Error(), "private")
	}
}

func TestVMHTTPDNSSelectionAndTrustedFields(t *testing.T) {
	for _, selection := range []string{
		``,
		`,"dns":{"mode":"PLATFORM_DEFAULT"}`,
		`,"dns":{"mode":"CUSTOM","nameservers":["223.5.5.5","2001:4860:4860::8888"]}`,
	} {
		request := httptest.NewRequest("POST", "/api/v1/virtual-machines", strings.NewReader(`{"data":{"name":"vm-test"`+selection+`}}`))
		request.Header.Set("Content-Type", "application/json")
		require.NoError(t, decodeStrictJSONHTTP(request, new(view.CreateVMRequest)))
	}
	for _, fields := range []string{
		`"dns":{"mode":"CUSTOM","nameservers":["223.5.5.5"],"dns_policy":"None"}`,
		`"dns":{"mode":"CUSTOM","nameservers":["223.5.5.5"],"tenant_id":"foreign"}`,
		`"dns":{"mode":"CUSTOM","nameservers":"223.5.5.5"}`,
		`"frozen_dns":{"mode":"CUSTOM","nameservers":["223.5.5.5"]}`,
		`"dns_defaults":{"nameservers":["223.5.5.5"]}`,
		`"namespace":"foreign"`,
	} {
		request := httptest.NewRequest("POST", "/api/v1/virtual-machines", strings.NewReader(`{"data":{"name":"vm-test",`+fields+`}}`))
		request.Header.Set("Content-Type", "application/json")
		require.Error(t, decodeStrictJSONHTTP(request, new(view.CreateVMRequest)), fields)
	}
	for _, query := range []string{"tenant_id=foreign", "mode=CUSTOM", "nameservers=223.5.5.5", "page=1"} {
		request := httptest.NewRequest("GET", "/api/v1/virtual-machine-dns-defaults?"+query, nil)
		require.Error(t, decodeInstanceQuery(request, new(view.GetDNSDefaultsRequest)), query)
	}
	request := httptest.NewRequest("GET", "/api/v1/virtual-machine-dns-defaults", nil)
	require.NoError(t, decodeInstanceQuery(request, new(view.GetDNSDefaultsRequest)))
}
