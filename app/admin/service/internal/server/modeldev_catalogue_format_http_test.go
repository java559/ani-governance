package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The input-version route allows state and format only. CSV and JSON are the
// two formats the upstream catalogue publishes, so the selector must hand them
// through unchanged and reject everything else instead of forwarding it.
func TestModelDevInputVersionFormatSelection(t *testing.T) {
	allowed := map[string]bool{"state": true, "format": true}
	for _, test := range []struct {
		name     string
		query    string
		format   string
		rejected bool
	}{
		{name: "csv", query: "?format=CSV", format: "CSV"},
		{name: "json", query: "?format=JSON", format: "JSON"},
		{name: "csv with state", query: "?state=READY&format=CSV", format: "CSV"},
		{name: "no format", query: "?state=READY"},
		{name: "lowercase", query: "?format=csv", rejected: true},
		{name: "unsupported", query: "?format=PARQUET", rejected: true},
		{name: "empty", query: "?format=", rejected: true},
		{name: "repeated", query: "?format=CSV&format=JSON", rejected: true},
		{name: "kind not declared here", query: "?format=CSV&kind=FINETUNING", rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/modeldev/input-versions"+test.query, nil)
			selector, err := modelDevCatalogueSelectors(request, allowed)
			if test.rejected {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if test.format == "" {
				require.Nil(t, selector.format)
				return
			}
			require.NotNil(t, selector.format)
			require.Equal(t, test.format, *selector.format)
		})
	}
}

// The implementations route declares kind only; adding format to the catalogue
// must not widen that route.
func TestModelDevImplementationsRouteRejectsFormat(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/modeldev/implementations?format=CSV", nil)
	_, err := modelDevCatalogueSelectors(request, map[string]bool{"kind": true})
	require.Error(t, err)
}
