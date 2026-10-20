package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestInstanceHTTPConfigurationAcceptedWithoutValueMutation(t *testing.T) {
	body := `{"data":{"name":"workspace","workspace_mount_path":"/work","volume_mounts":[{"pvc_name":"data","mount_path":"/datasets","read_only":true}],"labels":{"business.example.com/team":"research"},"annotations":{"business.example.com/note":"ready"},"env":[{"name":"FIRST","value":"  literal $(NEXT)  "},{"name":"NEXT","value":"second"}]}}`
	r := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	message := new(view.CreateInstanceRequest)
	require.NoError(t, decodeInstanceHTTP(r, message))
	encoded, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
	require.NoError(t, err)
	require.JSONEq(t, body, string(encoded))
}

func TestInstanceHTTPConfigurationRejectsNullAndInternalFields(t *testing.T) {
	for _, field := range []string{"workspace_mount_path", "volume_mounts", "network", "labels", "annotations", "env"} {
		t.Run(field, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(`{"data":{"`+field+`":null}}`))
			r.Header.Set("Content-Type", "application/json")
			require.Error(t, decodeInstanceHTTP(r, new(view.CreateInstanceRequest)))
		})
	}
	for _, body := range []string{
		`{"data":{"workspace_mount_path":""}}`,
		`{"data":{"network":{}}}`,
		`{"data":{"volume_mounts":[{"pvc_name":"data","mount_path":"/data","read_only":null}]}}`,
		`{"data":{"env":[{"name":"TOKEN","value":null}]}}`,
		`{"data":{"instance_contract_version":1}}`,
		`{"data":{"volume_mounts":[{"pvc_name":"data","pvc_uid":"forged","mount_path":"/data"}]}}`,
	} {
		r := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		require.Error(t, decodeInstanceHTTP(r, new(view.CreateInstanceRequest)), body)
	}
}

func TestInstanceHTTPConfigurationAcceptsExplicitEmptyCollections(t *testing.T) {
	for _, extra := range []string{`"env":[]`, `"labels":{}`, `"annotations":{}`, `"volume_mounts":[]`} {
		request := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(`{"data":{`+extra+`}}`))
		request.Header.Set("Content-Type", "application/json")
		require.NoError(t, decodeInstanceHTTP(request, new(view.CreateInstanceRequest)))
	}
	request := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(`{"data":{}}`))
	request.Header.Set("Content-Type", "application/json")
	require.NoError(t, decodeInstanceHTTP(request, new(view.CreateInstanceRequest)))
}

func TestInstanceHTTPConfigurationErrorCodeAndReason(t *testing.T) {
	for _, test := range []struct {
		name, upstream, reason string
		code                   int32
	}{
		{name: "PVC catalog bound", upstream: "INSTANCE_PVC_CATALOG_LIMIT", reason: "INSTANCE_PVC_CATALOG_LIMIT", code: 422},
		{name: "log connection limit", upstream: "INSTANCE_LOG_CONNECTION_LIMIT", reason: "INSTANCE_LOG_CONNECTION_LIMIT", code: 429},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual := kratoserrors.FromError(instanceHTTPError(status.Error(codes.ResourceExhausted, test.upstream)))
			require.Equal(t, test.code, actual.Code)
			require.Equal(t, test.reason, actual.Reason)
		})
	}
}
