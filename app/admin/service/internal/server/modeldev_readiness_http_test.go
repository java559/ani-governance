package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
)

const modelDevReadinessBody = `{"base_model_version_id":"11111111-2222-4333-8444-555555555555","compute_preset_id":"22222222-3333-4444-8555-666666666666"}`

// This verifies the actual route and entry into authentication. The denying
// middleware never substitutes an authenticated tenant or successful business.
func TestModelDevReadinessRouteEntersAuthentication(t *testing.T) {
	server := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
		}
	}))
	registerModelDevHTTP(server, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/modeldev/finetuning/readiness/check", strings.NewReader(modelDevReadinessBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code, "MODELDEV_READINESS_ROUTE: actual public source-only route must enter authentication")
}

func TestModelDevReadinessRejectsUntrustedBodyFields(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{}`, modelDevReadinessBody + `{}`,
		`{"base_model_version_id":null,"compute_preset_id":"id"}`,
		`{"base_model_version_id":"id","base_model_version_id":"again","compute_preset_id":"id"}`,
		`{"base_model_version_id":"id","compute_preset_id":"id","finetuning_template":null}`,
		`{"base_model_version_id":"id","compute_preset_id":"id","finetuning_template":"\ud800"}`,
		strings.Repeat(" ", 16385),
	} {
		t.Run(raw[:min(len(raw), 80)], func(t *testing.T) {
			_, err := decodeModelDevReadiness(strings.NewReader(raw))
			require.Error(t, err)
		})
	}
	for _, name := range []string{"tenant_id", "actor", "compute_origin", "dataset_version_id", "target_model_id", "new_model_name", "family", "image_version_id"} {
		raw := strings.TrimSuffix(modelDevReadinessBody, "}") + `,"` + name + `":"forged"}`
		_, err := decodeModelDevReadiness(strings.NewReader(raw))
		require.Error(t, err, name)
	}
	valid := strings.TrimSuffix(modelDevReadinessBody, "}") + `,"finetuning_template":"native-choice"}`
	out, err := decodeModelDevReadiness(strings.NewReader(valid))
	require.NoError(t, err)
	require.Equal(t, "native-choice", *out.FinetuningTemplate)
}
