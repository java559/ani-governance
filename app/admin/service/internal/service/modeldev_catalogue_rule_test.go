package service

import (
	"testing"

	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
)

// The inclusive flags decide whether the returned minimum/maximum is itself
// acceptable. Dropping them makes a client accept lora_dropout=1 and
// learning_rate=0, which the owner rejects. Absent flags must stay absent
// instead of defaulting to closed.
func TestModelDevPublicParameterRuleKeepsIntervalOpenness(t *testing.T) {
	closed, open := true, false
	for _, test := range []struct {
		name         string
		rule         *modeldevcontractv1.ParameterRule
		wantMin      *bool
		wantMax      *bool
		wantEditable bool
		wantReason   string
	}{
		{
			name: "half-open upper bound keeps maximum exclusive",
			rule: &modeldevcontractv1.ParameterRule{
				Name: "lora_dropout", Type: modeldevcontractv1.ParameterType_PARAMETER_TYPE_DECIMAL,
				MinimumInclusive: &closed, MaximumInclusive: &open,
				Maximum:  &trainingcontractv1.Parameter{Name: "lora_dropout", Value: &trainingcontractv1.Parameter_DecimalValue{DecimalValue: "1"}},
				Editable: true,
			},
			wantMin: &closed, wantMax: &open, wantEditable: true,
		},
		{
			name: "half-open lower bound keeps minimum exclusive",
			rule: &modeldevcontractv1.ParameterRule{
				Name: "learning_rate", Type: modeldevcontractv1.ParameterType_PARAMETER_TYPE_DECIMAL,
				MinimumInclusive: &open,
				Minimum:          &trainingcontractv1.Parameter{Name: "learning_rate", Value: &trainingcontractv1.Parameter_DecimalValue{DecimalValue: "0"}},
				Editable:         true,
			},
			wantMin: &open, wantMax: nil, wantEditable: true,
		},
		{
			name: "absent flags stay absent so a client cannot assume a bound",
			rule: &modeldevcontractv1.ParameterRule{
				Name: "cutoff_len", Type: modeldevcontractv1.ParameterType_PARAMETER_TYPE_INTEGER, Editable: true,
			},
			wantMin: nil, wantMax: nil, wantEditable: true,
		},
		{
			name: "read-only rule carries its reason",
			rule: &modeldevcontractv1.ParameterRule{
				Name: "step_timeout_seconds", Type: modeldevcontractv1.ParameterType_PARAMETER_TYPE_INTEGER,
				Editable: false, NonEditableReason: "managed by the platform",
			},
			wantMin: nil, wantMax: nil, wantEditable: false, wantReason: "managed by the platform",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := modelDevPublicParameterRule(test.rule)
			if out.Name != test.rule.Name || out.Type == "" {
				t.Fatalf("name=%q type=%q", out.Name, out.Type)
			}
			assertOptionalBool(t, "minimum_inclusive", out.MinimumInclusive, test.wantMin)
			assertOptionalBool(t, "maximum_inclusive", out.MaximumInclusive, test.wantMax)
			if out.Editable != test.wantEditable {
				t.Fatalf("editable=%v", out.Editable)
			}
			if out.NonEditableReason != test.wantReason {
				t.Fatalf("non_editable_reason=%q", out.NonEditableReason)
			}
			if out.ValidationDescription != test.rule.ValidationDescription {
				t.Fatalf("validation_description=%q", out.ValidationDescription)
			}
			if out.NumericRepresentation != test.rule.NumericRepresentation {
				t.Fatalf("numeric_representation=%q", out.NumericRepresentation)
			}
		})
	}
}

func assertOptionalBool(t *testing.T, field string, got, want *bool) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Fatalf("%s=%v, want absent", field, *got)
	case want != nil && got == nil:
		t.Fatalf("%s absent, want %v", field, *want)
	case want != nil && *got != *want:
		t.Fatalf("%s=%v, want %v", field, *got, *want)
	}
}
