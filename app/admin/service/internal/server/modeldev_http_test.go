package server

import (
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/service"
)

const modelDevCreateIntentJSON = `"name":"train","kind":"GENERAL_TRAINING","implementation_id":"builtin.mlp","compute_preset_id":"11111111-2222-4333-8444-555555555555","parameters":{},"dataset_version_id":"22222222-3333-4444-8555-666666666666"`

func TestDecodeModelDevCreatePreservesParameterPresence(t *testing.T) {
	for _, test := range []struct {
		name    string
		extra   string
		present bool
	}{
		{name: "omitted learning rate"},
		{name: "explicit default", extra: ``, present: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := `{` + modelDevCreateIntentJSON + test.extra + `,"idempotency_key":"Case-\uD83D\uDE80"}`
			if test.present {
				raw = strings.Replace(raw, `"parameters":{}`, `"parameters":{"learning_rate":"0.01"}`, 1)
			}
			in, err := decodeModelDevCreate(strings.NewReader(raw))
			require.NoError(t, err)
			require.Equal(t, "Case-🚀", in.IdempotencyKey)
			require.Equal(t, "train", in.Intent.Name)
			if test.present {
				require.NotNil(t, in.Intent.MLP.LearningRate)
				require.Equal(t, "0.01", *in.Intent.MLP.LearningRate)
			} else {
				require.Nil(t, in.Intent.MLP.LearningRate)
			}
		})
	}
	// An escaped backslash is ordinary key content, not a surrogate escape.
	in, err := decodeModelDevCreate(strings.NewReader(`{` + modelDevCreateIntentJSON + `,"idempotency_key":"literal-\\uD800"}`))
	require.NoError(t, err)
	require.Equal(t, `literal-\uD800`, in.IdempotencyKey)
}

func TestDecodeModelDevCreateRejectsAmbiguousOrInjectedJSON(t *testing.T) {
	valid := `{` + modelDevCreateIntentJSON + `,"idempotency_key":"original"}`
	for _, test := range []struct{ name, raw string }{
		{"missing key", `{` + modelDevCreateIntentJSON + `}`},
		{"null key", `{` + modelDevCreateIntentJSON + `,"idempotency_key":null}`},
		{"empty key", `{` + modelDevCreateIntentJSON + `,"idempotency_key":""}`},
		{"control key", `{` + modelDevCreateIntentJSON + `,"idempotency_key":"secret\u0000value"}`},
		{"lone surrogate key", `{` + modelDevCreateIntentJSON + `,"idempotency_key":"secret\uD800"}`},
		{"duplicate key", strings.TrimSuffix(valid, "}") + `,"idempotency_key":"other"}`},
		{"escaped duplicate intent field", strings.TrimSuffix(valid, "}") + `,"na\u006de":"other"}`},
		{"nested duplicate", strings.Replace(valid, `"parameters":{}`, `"parameters":{"epochs":3,"epochs":3}`, 1)},
		{"null parameters", strings.Replace(valid, `"parameters":{}`, `"parameters":null`, 1)},
		{"missing parameters", strings.Replace(valid, `,"parameters":{}`, ``, 1)},
		{"array parameters", strings.Replace(valid, `"parameters":{}`, `"parameters":[]`, 1)},
		{"legacy preset", strings.TrimSuffix(valid, "}") + `,"preset_id":"11111111-2222-4333-8444-555555555555"}`},
		{"zero timeout", strings.TrimSuffix(valid, "}") + `,"timeout_seconds":0}`},
		{"injected tenant", strings.TrimSuffix(valid, "}") + `,"tenant_id":"secret-tenant"}`},
		{"injected actor", strings.TrimSuffix(valid, "}") + `,"actor":"secret-actor"}`},
		{"trailing object", valid + `{}`},
		{"invalid utf8", strings.Replace(valid, "original", string([]byte{0xff}), 1)},
		{"oversize", valid + strings.Repeat(" ", 16385)},
	} {
		t.Run(test.name, func(t *testing.T) {
			in, err := decodeModelDevCreate(strings.NewReader(test.raw))
			require.Equal(t, service.ModelDevCreateInput{}, in)
			require.Error(t, err)
			failure := errors.FromError(err)
			require.Equal(t, int32(400), failure.Code)
			require.Equal(t, "INVALID_MODELDEV_CREATE", failure.Reason)
			require.Equal(t, "invalid modeldev create request", failure.Message)
		})
	}
}

// The catalogue selection is forwarded unchanged to the shared strict parser;
// a partial pair is rejected before any persistence.
func TestDecodeModelDevCreateForwardsTrainingImageSelection(t *testing.T) {
	id := "img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	raw := `{` + modelDevCreateIntentJSON + `,"training_image_id":"` + id + `","training_image_scope":"platform","idempotency_key":"selection-1"}`
	in, err := decodeModelDevCreate(strings.NewReader(raw))
	require.NoError(t, err)
	require.NotNil(t, in.Intent.TrainingImageID)
	require.Equal(t, id, *in.Intent.TrainingImageID)
	require.NotNil(t, in.Intent.TrainingImageScope)
	require.Equal(t, "platform", *in.Intent.TrainingImageScope)

	for _, partial := range []string{
		`"training_image_id":"` + id + `",`,
		`"training_image_scope":"platform",`,
		`"training_image_id":"registry.example.test/cpu/mlp:latest","training_image_scope":"platform",`,
		`"training_image_id":"` + id + `","training_image_scope":"global",`,
	} {
		body := `{` + modelDevCreateIntentJSON + `,` + partial + `"idempotency_key":"selection-2"}`
		if _, err := decodeModelDevCreate(strings.NewReader(body)); err == nil {
			t.Fatalf("partial or non-catalogue selection accepted: %s", partial)
		}
	}
}
