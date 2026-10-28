package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go-wind-admin/pkg/middleware/auth"
)

// Exercise the actual NewRestServer registration order and real services.
// Denied identities must reach the quota guard before any persistence access;
// a shadowing /tenants/{id} route instead fails binding with CODEC/400.
func TestQuotaSummaryStaticRouteReachesQuotaGuard(t *testing.T) {
	server := newRestServerWithSwagger(t, false)
	for _, tc := range []struct {
		name      string
		principal *auth.Principal
		status    int
	}{
		{"missing-identity", nil, http.StatusUnauthorized},
		{"tenant-identity", &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 2}, http.StatusNotFound},
		{"api-key", &auth.Principal{Type: auth.SubjectAPIKey, ID: 7, TenantID: 2}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/tenants/quota-account-summaries?tenantId=2", nil)
			if tc.principal != nil {
				req = req.WithContext(auth.NewPrincipalContext(req.Context(), tc.principal))
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, req)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			var body struct {
				Reason string `json:"reason"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			require.NotEqual(t, "CODEC", body.Reason, "static summaries cannot bind as numeric tenant IDs")
			if tc.principal != nil {
				require.Equal(t, "QUOTA_NOT_FOUND", body.Reason)
			}
		})
	}
}
