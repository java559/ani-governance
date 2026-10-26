package logging

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
)

func TestInstanceConfigurationAuditRedactsEnvironmentValues(t *testing.T) {
	env := newAuditServer(t)
	env.fire(http.MethodPost, "/case/5?value=private-env", map[string]string{
		"X-Test-Operation": admin.OperationInstanceServiceCreateInstance,
		"Content-Type":     "application/json",
		"Referer":          "https://example.test/?value=private-env",
	}, `{"data":{"env":[{"name":"TOKEN","value":"private-env"}]}}`, "127.0.0.1:1234")
	require.Len(t, env.capture.api, 1)
	record := env.capture.api[0]
	require.Equal(t, "[redacted]", record.GetRequestBody())
	require.Empty(t, record.GetReferer())
	require.Equal(t, "/case/5", record.GetRequestUri())
	require.Empty(t, record.GetResponse())
}

func TestComputePresetAuditKeepsQueryAndBodyRedacted(t *testing.T) {
	env := newAuditServer(t)
	env.fire(http.MethodPost, "/case/5?name=private-preset", map[string]string{
		"X-Test-Operation": admin.OperationComputePresetServiceAdminCreatePreset,
		"Content-Type":     "application/json",
		"Referer":          "https://example.test/?name=private-preset",
	}, `{"data":{"name":"private-preset"}}`, "127.0.0.1:1234")
	require.Len(t, env.capture.api, 1)
	record := env.capture.api[0]
	require.Equal(t, "[redacted]", record.GetRequestBody())
	require.Empty(t, record.GetReferer())
	require.Equal(t, "/case/5", record.GetRequestUri())
}

func TestVMConsoleAuditDoesNotRetainTicketOrBody(t *testing.T) {
	env := newAuditServer(t)
	env.fire(http.MethodPost, "/case/5?ticket=private-vnc-ticket", map[string]string{"X-Test-Operation": "/admin.service.v1.VMService/CreateVMConsoleSession", "Content-Type": "application/json", "Referer": "https://example.test/?ticket=private-vnc-ticket"}, `{"data":{"idempotencyKey":"private-session-key"}}`, "127.0.0.1:1234")
	require.Len(t, env.capture.api, 1)
	record := env.capture.api[0]
	require.Equal(t, "[redacted]", record.GetRequestBody())
	require.Empty(t, record.GetReferer())
	require.Equal(t, "/case/5", record.GetRequestUri())
	require.Empty(t, record.GetResponse())
}
