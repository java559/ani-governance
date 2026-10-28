package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The name filter is a substring of the admitted name, which is 1-80 printable
// runes. Anything outside that shape can never match a stored row, so the
// selector rejects it instead of returning an empty page that looks like an
// empty tenant.
func TestModelDevExecutionNameSelector(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    string
		rejected bool
	}{
		{name: "simple", value: "lora-sft-01"},
		{name: "with space", value: "cpu smoke"},
		{name: "chinese", value: "训练任务"},
		{name: "80 runes is the admitted maximum", value: strings.Repeat("x", 80)},
		{name: "empty", value: "", rejected: true},
		{name: "blank", value: "   ", rejected: true},
		{name: "81 runes", value: strings.Repeat("x", 81), rejected: true},
		{name: "control character", value: "bad\x01name", rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, err := modelDevNamePointer(test.value)
			if test.rejected {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, test.value, *selected)
		})
	}
}
