package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewRestServerRegistersModelImportLogs(t *testing.T) {
	srv := newRestServerWithSwagger(t, false)
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/models/import-tasks/task-1/logs", nil))
	require.Equal(t, http.StatusUnauthorized, response.Code, "import logs must reach the authenticated Model boundary")
}
