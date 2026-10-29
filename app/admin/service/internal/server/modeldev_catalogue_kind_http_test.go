package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
)

// The kind filter must be rejected for any value outside the published
// vocabulary, and it must not become accepted on routes that never declared it.
func TestModelDevCatalogueRejectsUnsupportedKind(t *testing.T) {
	server := khttp.NewServer()
	registerModelDevHTTP(server, nil)
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	for _, path := range []string{
		"/api/v1/modeldev/implementations?device_mode=cpu&kind=finetuning",
		"/api/v1/modeldev/implementations?device_mode=cpu&kind=",
		"/api/v1/modeldev/implementations?device_mode=cpu&kind=UNSPECIFIED",
		"/api/v1/modeldev/implementations?device_mode=cpu&kind=EXECUTION_KIND_FINETUNING",
		"/api/v1/modeldev/implementations?device_mode=cpu&kind=FINETUNING&kind=GENERAL_TRAINING",
		"/api/v1/modeldev/implementations?device_mode=cpu&kind=FINETUNING&state=READY",
		"/api/v1/modeldev/implementations?device_mode=cpu&implementation_id=builtin.mlp",
		"/api/v1/modeldev/input-versions?kind=FINETUNING",
		"/api/v1/modeldev/input-versions?state=ready",
		"/api/v1/modeldev/input-versions?state=READY&kind=FINETUNING",
	} {
		t.Run(path, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, web.URL+path, nil)
			require.NoError(t, err)
			response, err := web.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusBadRequest, response.StatusCode)
		})
	}
}
