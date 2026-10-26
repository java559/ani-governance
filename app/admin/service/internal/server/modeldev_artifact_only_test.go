package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

func TestDecodeModelDevFinetuningWithoutRegistrationTarget(t *testing.T) {
	const body = `{"name":"fine-artifacts","kind":"FINETUNING","implementation_id":"builtin.sft_lora","compute_preset_id":"11111111-2222-4333-8444-555555555555","dataset_version_id":"22222222-3333-4444-8555-666666666666","base_model_version_id":"33333333-4444-4555-8666-777777777777","parameters":{},"idempotency_key":"fine-artifacts"}`
	in, err := decodeModelDevCreate(strings.NewReader(body))
	require.NoError(t, err)
	require.NotNil(t, in.Intent.BaseModelVersionID)
	require.Nil(t, in.Intent.NewModelName)
	require.Nil(t, in.Intent.TargetModelID)
	canonical, _, err := cpup01.CanonicalIntent(in.Intent)
	require.NoError(t, err)
	frozen, err := cpup01.DecodeCanonicalIntent(canonical)
	require.NoError(t, err)
	require.Equal(t, in.Intent, frozen)

	for _, extra := range []string{
		`,"new_model_name":"published","target_model_id":"44444444-5555-4666-8777-888888888888"`,
		`,"skip_model_registration":true`,
		`,"target_model_id":null`,
	} {
		_, err := decodeModelDevCreate(strings.NewReader(strings.TrimSuffix(body, "}") + extra + "}"))
		require.Error(t, err, "only the existing omitted target fields express artifact-only output")
	}
}
