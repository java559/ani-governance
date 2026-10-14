package assets

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The public document must describe the handwritten route's flat JSON and
// durable 202 response. Generated default handlers are not registered for it.
func TestModelDevCreateOpenAPIContract(t *testing.T) {
	var document map[string]any
	require.NoError(t, yaml.Unmarshal(OpenApiData, &document))
	object := func(value any) map[string]any {
		t.Helper()
		got, ok := value.(map[string]any)
		require.True(t, ok, "expected an OpenAPI object")
		return got
	}
	path := object(object(document["paths"])["/api/v1/modeldev/executions"])
	operation := object(path["post"])
	responses := object(operation["responses"])
	require.Contains(t, responses, "202")
	require.NotContains(t, responses, "200", "synthetic 200 contradicts the real durable-acceptance status")
	body := object(object(object(operation["requestBody"])["content"])["application/json"])
	require.Equal(t, "#/components/schemas/CreateExecutionRequest", object(body["schema"])["$ref"])
	schemas := object(object(document["components"])["schemas"])
	request := object(schemas["CreateExecutionRequest"])
	require.ElementsMatch(t, []any{"name", "kind", "preset_id", "dataset_version_id", "idempotency_key"}, request["required"])
	fields := object(request["properties"])
	require.Len(t, fields, 8, "only CPU-P01 intent and idempotency fields are public")
	for _, forbidden := range []string{"data", "tenant_id", "actor", "namespace", "command", "snapshot", "gpu"} {
		require.NotContains(t, fields, forbidden)
	}
	parameters := object(fields["general_parameters"])
	require.Equal(t, "array", parameters["type"])
	require.NotEqual(t, true, parameters["nullable"])
	require.Equal(t, "#/components/schemas/GeneralParameter", object(parameters["items"])["$ref"])
	for _, name := range []string{"name", "type", "value"} {
		require.Equal(t, "string", object(object(object(schemas["GeneralParameter"])["properties"])[name])["type"])
	}
	reply := object(object(responses["202"])["content"])
	require.Equal(t, "#/components/schemas/CreateExecutionResponse", object(object(reply["application/json"])["schema"])["$ref"])
	responseFields := object(object(schemas["CreateExecutionResponse"])["properties"])
	for _, name := range []string{"operation_id", "execution_id", "resolved_release_id", "compute_state", "delivery_state", "close_state", "resource_state"} {
		require.Equal(t, "string", object(responseFields[name])["type"])
	}
	require.Equal(t, "boolean", object(responseFields["replayed"])["type"])
}
