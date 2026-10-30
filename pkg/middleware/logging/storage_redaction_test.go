package logging

import (
	"github.com/stretchr/testify/require"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	"net/http"
	"testing"
)

func TestStorageWriteAuditDoesNotRetainKeys(t *testing.T) {
	for _, operation := range []string{admin.OperationStorageServiceGetUploadURL, admin.OperationStorageServiceDeleteObject, admin.OperationStorageServiceCreateBucket} {
		t.Run(operation, func(t *testing.T) {
			env := newAuditServer(t)
			env.fire(http.MethodPost, "/case/5?prefix=private-key", map[string]string{"X-Test-Operation": operation, "Content-Type": "application/json", "Referer": "https://example.test/?private=key"}, `{"data":{"key":"private-key"}}`, "127.0.0.1:1234")
			require.Len(t, env.capture.api, 1)
			record := env.capture.api[0]
			require.Equal(t, "[redacted]", record.GetRequestBody())
			require.Empty(t, record.GetReferer())
			require.Equal(t, "/case/5", record.GetRequestUri())
			require.Empty(t, record.GetResponse())
		})
	}
}
