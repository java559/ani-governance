package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestInstanceNewCommandsStrictPublicJSON(t *testing.T) {
	const key = `"idempotencyKey":"10000000-0000-4000-8000-000000000001"`
	for _, action := range []string{"restart", "attach-pvc", "detach-pvc"} {
		factory := func() proto.Message {
			switch action {
			case "attach-pvc":
				return new(view.AttachInstancePVCRequest)
			case "detach-pvc":
				return new(view.DetachInstancePVCRequest)
			default:
				return new(view.ChangeInstanceRequest)
			}
		}
		suffix := ""
		if action != "restart" {
			suffix = `,"mount_path":"/datasets"`
		}
		if action == "attach-pvc" {
			suffix += `,"pvc_name":"research-data"`
		}
		for _, version := range []string{"7", "9007199254740993", "9223372036854775807"} {
			body := `{"data":{` + key + `,"expectedVersion":"` + version + `"` + suffix + `}}`
			r := httptest.NewRequest("POST", "/api/v1/instances/30000000-0000-4000-8000-000000000001:"+action, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			message := factory()
			require.NoError(t, decodeInstanceHTTP(r, message), body)
			raw, err := protojson.Marshal(message)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"`+version+`"`)
		}
		for _, fields := range []string{
			`"expectedVersion":7`, `"expectedVersion":null`, `"expectedVersion":"1e2"`, `"expectedVersion":"07"`, `"expectedVersion":"9223372036854775808"`,
			`"expectedVersion":"7","expectedVersion":"8"`, `"expected_version":"7"`, `"expectedVersion":"7","tenant_id":"forged"`,
			`"expectedVersion":"7","restartIfRunning":true`, `"expectedVersion":"7","restart_if_running":null`,
		} {
			body := `{"data":{` + key + `,` + fields + suffix + `}}`
			r := httptest.NewRequest("POST", "/api/v1/instances/30000000-0000-4000-8000-000000000001:"+action, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			require.Error(t, decodeInstanceHTTP(r, factory()), body)
		}
		if action != "restart" {
			for _, value := range []string{`null`, `"false"`, `0`} {
				body := `{"data":{` + key + `,"expectedVersion":"7"` + suffix + `,"restart_if_running":` + value + `}}`
				r := httptest.NewRequest("POST", "/api/v1/instances/x:"+action, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				require.Error(t, decodeInstanceHTTP(r, factory()), body)
			}
		}
	}
	// Existing routes keep their established field aliases and numeric version.
	r := httptest.NewRequest("POST", "/api/v1/instances/x:start", strings.NewReader(`{"data":{`+key+`,"expected_version":7}}`))
	r.Header.Set("Content-Type", "application/json")
	require.NoError(t, decodeInstanceHTTP(r, new(view.ChangeInstanceRequest)))
}

// Exercise the registered router and path binder as well as the raw decoder.
// Valid commands reach the real service's authentication check; malformed
// commands must be rejected before any owner or ledger is needed.
func TestInstanceNewCommandsHTTPRejectsUntrustedJSON(t *testing.T) {
	server := khttp.NewServer()
	RegisterInstanceHTTPServer(server, service.NewInstanceService(nil, nil, nil, nil, nil))
	const id = "30000000-0000-4000-8000-000000000001"
	const key = "10000000-0000-4000-8000-000000000001"
	for _, action := range []string{"restart", "attach-pvc", "detach-pvc"} {
		t.Run(action, func(t *testing.T) {
			fields := `"idempotencyKey":"` + key + `","expectedVersion":"9007199254740993"`
			if action != "restart" {
				fields += `,"mount_path":"/datasets","restart_if_running":false`
			}
			if action == "attach-pvc" {
				fields += `,"pvc_name":"research-data","read_only":false`
			}
			valid := `{"data":{` + fields + `}}`
			path := "/api/v1/instances/" + id + ":" + action
			fire := func(body, query, contentType, header string, want int) {
				t.Helper()
				r := httptest.NewRequest(http.MethodPost, path+query, strings.NewReader(body))
				r.Header.Set("Content-Type", contentType)
				if header != "" {
					r.Header.Set(header, "forged")
				}
				out := httptest.NewRecorder()
				server.ServeHTTP(out, r)
				require.Equal(t, want, out.Code, out.Body.String())
				var failure struct{ Reason string }
				require.NoError(t, json.Unmarshal(out.Body.Bytes(), &failure))
				if want == http.StatusBadRequest {
					require.Equal(t, "INVALID_INSTANCE_REQUEST", failure.Reason)
				}
			}
			fire(valid, "", "application/json", "", http.StatusUnauthorized)
			for _, invalid := range []string{
				`{}`, `{"data":null}`, `{"data":[]}`,
				`{"data":{` + fields + `},"data":{` + fields + `}}`,
				`{"data":{` + fields + `},"tenant_id":"forged"}`,
				`{"data":{` + fields + `,"instanceId":"30000000-0000-4000-8000-000000000002"}}`,
				`{"data":{` + fields + `,"actor":"forged"}}`,
				`{"data":{` + fields + `,"namespace":"forged"}}`,
				strings.Replace(valid, `"9007199254740993"`, `9007199254740993`, 1),
				strings.Replace(valid, `"expectedVersion"`, `"expected_version"`, 1),
				strings.Replace(valid, `"idempotencyKey"`, `"idempotency_key"`, 1),
				valid + `{}`,
			} {
				fire(invalid, "", "application/json", "", http.StatusBadRequest)
			}
			for _, header := range []string{"X-Ani-Tenant-Id", "X-Tenant-Id", "X-Ani-Actor", "X-Ani-Operator", "X-Resource-Tenant-Id"} {
				fire(valid, "", "application/json", header, http.StatusBadRequest)
			}
			fire(valid, "?unexpected=value", "application/json", "", http.StatusBadRequest)
			fire(valid, "", "text/plain", "", http.StatusBadRequest)
			fire(valid+strings.Repeat(" ", 65536), "", "application/json", "", http.StatusBadRequest)
			if action != "restart" {
				fire(strings.Replace(valid, `"restart_if_running":false`, `"restart_if_running":null`, 1), "", "application/json", "", http.StatusBadRequest)
				fire(strings.Replace(valid, `"mount_path"`, `"mountPath"`, 1), "", "application/json", "", http.StatusBadRequest)
			}
			if action == "attach-pvc" {
				fire(strings.Replace(valid, `"read_only":false`, `"read_only":null`, 1), "", "application/json", "", http.StatusBadRequest)
				fire(strings.Replace(valid, `"pvc_name"`, `"pvcName"`, 1), "", "application/json", "", http.StatusBadRequest)
			} else {
				fire(`{"data":{`+fields+`,"pvc_name":"research-data"}}`, "", "application/json", "", http.StatusBadRequest)
			}
		})
	}
}

func TestInstanceMountConfigurationPresenceAndVersionJSON(t *testing.T) {
	encode := protojson.MarshalOptions{EmitUnpopulated: true}
	raw, err := encode.Marshal(&view.Operation{})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"target_configuration":null`)
	raw, err = encode.Marshal(&view.Operation{TargetConfiguration: &view.MountConfiguration{}, AppliedMountVersion: 9007199254740993})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"volume_mounts":[]`)
	require.Contains(t, string(raw), `"applied_mount_version":"9007199254740993"`)
	raw, err = encode.Marshal(&view.InstanceConfiguration{PendingChange: &view.PendingMountChange{OperationId: "operation", IntentVersion: 9007199254740993, TargetConfiguration: &view.MountConfiguration{}}})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"pending_change":{`)
	require.Contains(t, string(raw), `"intent_version":"9007199254740993"`)
	require.Contains(t, string(raw), `"operation_id":"operation"`)
}
