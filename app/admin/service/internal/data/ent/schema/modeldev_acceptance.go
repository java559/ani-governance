package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"go-wind-admin/pkg/localdeps/go-crud/entgo/mixin"
)

// ModelDevAcceptance is the immutable CPU admission and its pending dispatch.
// It is deliberately independent of quota operations, accounts and charges.
type ModelDevAcceptance struct{ ent.Schema }

func (ModelDevAcceptance) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{
		Table: "sys_modeldev_acceptances",
		Checks: map[string]string{
			"sys_modeldev_acceptances_tenant_positive_ck":   "tenant_id > 0",
			"sys_modeldev_acceptances_action_ck":            "action = 'modeldev.execution.create'",
			"sys_modeldev_acceptances_dispatch_state_ck":    "dispatch_state IN ('QUEUED','DISPATCHING','UNKNOWN','ACKED')",
			"sys_modeldev_acceptances_delivery_counters_ck": "attempt_count >= 0 AND lease_generation >= 0",
			"sys_modeldev_acceptances_delivery_lease_ck":    "((dispatch_state = 'DISPATCHING' AND lease_owner IS NOT NULL AND length(lease_owner) BETWEEN 1 AND 128 AND lease_until IS NOT NULL AND lease_generation > 0) OR (dispatch_state <> 'DISPATCHING' AND lease_owner IS NULL AND lease_until IS NULL))",
			"sys_modeldev_acceptances_owner_receipt_ck":     "((dispatch_state = 'ACKED' AND owner_receipt_canonical IS NOT NULL AND octet_length(owner_receipt_canonical) BETWEEN 1 AND 4096) OR (dispatch_state <> 'ACKED' AND owner_receipt_canonical IS NULL))",
			"sys_modeldev_acceptances_delivery_error_ck":    "last_error_code IS NULL OR last_error_code IN ('INVALID_COMMAND','COMMAND_CONFLICT','OWNER_UNAVAILABLE','INVALID_ACK','LEASE_EXHAUSTED','ATTEMPTS_EXHAUSTED')",
			"sys_modeldev_acceptances_delivery_due_ck":      "dispatch_state = 'UNKNOWN' OR (next_attempt_at IS NULL AND retry_blocked = false)",
			"sys_modeldev_acceptances_stop_intent_ck":       "((stop_intent_generation = 0 AND stop_requested_at IS NULL AND stop_requested_actor IS NULL AND close_dispatch_state = 'IDLE') OR (stop_intent_generation > 0 AND stop_requested_at IS NOT NULL AND stop_requested_actor IS NOT NULL AND length(stop_requested_actor) BETWEEN 1 AND 128 AND close_dispatch_state IN ('QUEUED','DISPATCHING','UNKNOWN','ACKED')))",
			"sys_modeldev_acceptances_close_counters_ck":    "close_attempt_count >= 0 AND close_lease_generation >= 0",
			"sys_modeldev_acceptances_close_lease_ck":       "((close_dispatch_state = 'DISPATCHING' AND close_lease_owner IS NOT NULL AND length(close_lease_owner) BETWEEN 1 AND 128 AND close_lease_until IS NOT NULL AND close_lease_generation > 0) OR (close_dispatch_state <> 'DISPATCHING' AND close_lease_owner IS NULL AND close_lease_until IS NULL))",
			"sys_modeldev_acceptances_close_receipt_ck":     "((close_dispatch_state = 'ACKED' AND close_receipt_canonical IS NOT NULL AND octet_length(close_receipt_canonical) BETWEEN 1 AND 4096) OR (close_dispatch_state <> 'ACKED' AND close_receipt_canonical IS NULL))",
			"sys_modeldev_acceptances_close_error_ck":       "close_last_error_code IS NULL OR close_last_error_code IN ('INVALID_COMMAND','COMMAND_CONFLICT','OWNER_UNAVAILABLE','INVALID_ACK','LEASE_EXHAUSTED','ATTEMPTS_EXHAUSTED')",
			"sys_modeldev_acceptances_close_due_ck":         "close_dispatch_state = 'UNKNOWN' OR (close_next_attempt_at IS NULL AND close_retry_blocked = false)",
		},
	}}
}

func (ModelDevAcceptance) Mixin() []ent.Mixin {
	return []ent.Mixin{mixin.AutoIncrementId{}, ModelDevAdmissionTenantID{}}
}

func (ModelDevAcceptance) Policy() ent.Policy {
	return &TenantMutationGuardPolicy{}
}

func (ModelDevAcceptance) Fields() []ent.Field {
	return []ent.Field{
		field.String("resource_tenant_id").NotEmpty().Immutable(),
		field.String("actor").NotEmpty().Immutable(),
		field.String("action").NotEmpty().Immutable(),
		field.String("idempotency_key").NotEmpty().Immutable(),
		field.String("operation_id").NotEmpty().Immutable().Unique(),
		field.String("execution_id").NotEmpty().Immutable().Unique(),
		field.String("intent_hash").NotEmpty().Immutable(),
		field.Bytes("intent_canonical").NotEmpty().Immutable(),
		field.String("execution_spec_hash").NotEmpty().Immutable(),
		field.Bytes("snapshot_canonical").NotEmpty().Immutable(),
		field.Time("accepted_at").Immutable(),
		field.Enum("dispatch_state").Values("QUEUED", "DISPATCHING", "UNKNOWN", "ACKED").Default("QUEUED"),
		field.Int64("attempt_count").NonNegative().Default(0),
		field.Int64("lease_generation").NonNegative().Default(0),
		field.String("lease_owner").Optional().Nillable().MaxLen(128),
		field.Time("lease_until").Optional().Nillable(),
		field.Time("next_attempt_at").Optional().Nillable(),
		field.Bool("retry_blocked").Default(false),
		field.String("last_error_code").Optional().Nillable(),
		field.Bytes("owner_receipt_canonical").Optional(),
		field.Int64("stop_intent_generation").NonNegative().Default(0),
		field.Time("stop_requested_at").Optional().Nillable(),
		field.String("stop_requested_actor").Optional().Nillable().MaxLen(128),
		field.Enum("close_dispatch_state").Values("IDLE", "QUEUED", "DISPATCHING", "UNKNOWN", "ACKED").Default("IDLE"),
		field.Int64("close_attempt_count").NonNegative().Default(0),
		field.Int64("close_lease_generation").NonNegative().Default(0),
		field.String("close_lease_owner").Optional().Nillable().MaxLen(128),
		field.Time("close_lease_until").Optional().Nillable(),
		field.Time("close_next_attempt_at").Optional().Nillable(),
		field.Bool("close_retry_blocked").Default(false),
		field.String("close_last_error_code").Optional().Nillable(),
		field.Bytes("close_receipt_canonical").Optional(),

		// telemetry_context / close_telemetry_context 只承载日志关联的
		// TelemetryContext。独立 jsonb 列，不进 canonical 摘要、幂等键或冻结规格。
		// 创建与关闭分开保存，用户新动作不覆盖创建来源。
		field.String("telemetry_context").
			Comment("创建受理日志关联上下文（JSONB）").
			SchemaType(map[string]string{
				dialect.MySQL:    "json",
				dialect.Postgres: "jsonb",
			}).
			Optional().
			Nillable(),
		field.String("close_telemetry_context").
			Comment("关闭受理日志关联上下文（JSONB）").
			SchemaType(map[string]string{
				dialect.MySQL:    "json",
				dialect.Postgres: "jsonb",
			}).
			Optional().
			Nillable(),
	}
}

func (ModelDevAcceptance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("resource_tenant_id", "actor", "action", "idempotency_key").Unique().StorageKey("uix_sys_modeldev_acceptances_idempotency"),
		index.Fields("tenant_id", "operation_id").Unique().StorageKey("uix_sys_modeldev_acceptances_tenant_operation"),
		index.Fields("tenant_id", "execution_id").Unique().StorageKey("uix_sys_modeldev_acceptances_tenant_execution"),
		index.Fields("retry_blocked", "dispatch_state", "next_attempt_at", "lease_until", "id").StorageKey("idx_sys_modeldev_acceptances_delivery_due"),
		index.Fields("close_retry_blocked", "close_dispatch_state", "close_next_attempt_at", "close_lease_until", "id").StorageKey("idx_sys_modeldev_acceptances_close_due"),
	}
}

// Keep the repository's TenantPrivacy policy while requiring a real tenant.
type ModelDevAdmissionTenantID struct{ mixin.TenantID[uint32] }

func (ModelDevAdmissionTenantID) Fields() []ent.Field {
	return []ent.Field{field.Uint32("tenant_id").Positive().Nillable().Immutable()}
}
