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
	require.Equal(t, "object", request["type"])
	require.ElementsMatch(t, []any{"name", "kind", "implementation_id", "compute_preset_id", "dataset_version_id", "parameters", "idempotency_key"}, request["required"])
	fields := object(request["properties"])
	require.Len(t, fields, 11, "only the canonical implementation and compute intent fields are public")
	for _, forbidden := range []string{"data", "tenant_id", "actor", "namespace", "command", "snapshot", "gpu", "preset_id", "image_version_id", "general_parameters", "source_execution_id", "finetuning"} {
		require.NotContains(t, fields, forbidden)
	}
	for _, name := range []string{"name", "kind", "implementation_id", "compute_preset_id", "dataset_version_id", "idempotency_key", "base_model_version_id", "target_model_id", "new_model_name"} {
		require.Equal(t, "string", object(fields[name])["type"], name)
	}
	timeout := object(fields["timeout_seconds"])
	require.Equal(t, "integer", timeout["type"])
	require.Equal(t, "uint64", timeout["format"])
	parameters := object(fields["parameters"])
	require.NotEqual(t, true, parameters["nullable"])
	require.Equal(t, "#/components/schemas/ExecutionParameters", parameters["$ref"])
	parameterSchema := object(schemas["ExecutionParameters"])
	require.Equal(t, "object", parameterSchema["type"])
	require.NotEqual(t, true, parameterSchema["nullable"])
	parameterFields := object(parameterSchema["properties"])
	require.Len(t, parameterFields, 11)
	for _, name := range []string{"learning_rate", "lora_dropout"} {
		require.Equal(t, "string", object(parameterFields[name])["type"], name)
	}
	for _, name := range []string{"epochs", "batch_size", "max_steps", "per_device_train_batch_size", "gradient_accumulation_steps", "cutoff_len", "lora_rank", "lora_alpha"} {
		field := object(parameterFields[name])
		require.Equal(t, "integer", field["type"], name)
		require.Equal(t, "int64", field["format"], name)
	}
	seed := object(parameterFields["seed"])
	require.Equal(t, "integer", seed["type"])
	require.Equal(t, "uint32", seed["format"])
	reply := object(object(responses["202"])["content"])
	require.Equal(t, "#/components/schemas/CreateExecutionResponse", object(object(reply["application/json"])["schema"])["$ref"])
	response := object(schemas["CreateExecutionResponse"])
	require.Equal(t, "object", response["type"])
	responseFields := object(response["properties"])
	require.Len(t, responseFields, 7)
	require.NotContains(t, responseFields, "resolved_release_id", "reserved release identity is not part of durable acceptance")
	for _, name := range []string{"operation_id", "execution_id", "compute_state", "delivery_state", "close_state", "resource_state"} {
		require.Equal(t, "string", object(responseFields[name])["type"])
	}
	require.NotContains(t, responseFields, "resolved_release_id")
	require.Equal(t, "boolean", object(responseFields["replayed"])["type"])
	require.Contains(t, schemas, "ModelOperation")
	require.Contains(t, schemas, "ModelRegistrationView")
}
