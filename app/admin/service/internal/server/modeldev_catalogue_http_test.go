package server

import "testing"

// Catalogue selection uses the same authenticated BFF boundary as execution.
// The denying middleware is an authentication-boundary check, not LIVE proof.
func TestModelDevCatalogueRoutesEnterAuthentication(t *testing.T) {
	testModelDevAuthenticatedRoutes(t, []string{
		"/api/v1/modeldev/implementations",
		"/api/v1/modeldev/input-versions",
		"/api/v1/modeldev/input-versions/11111111-2222-4333-8444-555555555555",
	})
}
