package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"go-wind-admin/pkg/localdeps/go-crud/entgo/mixin"
)

// QuotaOperation holds the schema definition for the QuotaOperation entity.
// 持久化转发操作：同时就是转发意图记录，不另建 dispatch/outbox 表。
// 请求、owner、租户、actor、配额向量一旦提交不可改写。
type QuotaOperation struct {
	ent.Schema
}

func (QuotaOperation) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table:     "sys_quota_operations",
			Charset:   "utf8mb4",
			Collation: "utf8mb4_bin",
			Checks: map[string]string{
				"sys_quota_operations_tenant_positive_ck":           `tenant_id > 0`,
				"sys_quota_operations_instance_contract_version_ck": `instance_contract_version IN (1, 2)`,
			},
		},
		entsql.WithComments(true),
		schema.Comment("配额操作与持久化转发记录表"),
	}
}

// Fields of the QuotaOperation.
func (QuotaOperation) Fields() []ent.Field {
	return []ent.Field{
		// 稳定操作 ID（UUID），重试保持不变。
		field.String("operation_id").
			Comment("操作ID（UUID）").
			NotEmpty().
			Immutable().
			Unique(),

		// 下游资源租户 UUID（由 Governance 从可信 Principal 解析并持久化）。
		field.String("resource_tenant_id").
			Comment("下游资源租户UUID").
			NotEmpty().
			Immutable(),

		// 资源 ID：创建时由 Governance 生成并随原操作持久化，重试读取原值。
		field.String("resource_id").
			Comment("资源ID（UUID）").
			NotEmpty().
			Immutable(),

		// 删除操作的来源创建操作；CREATE 为空。复合 FK 由 schema 导出器补充。
		field.String("create_operation_id").
			Comment("原创建操作ID（DELETE 必填）").
			NotEmpty().
			Immutable().
			Optional().
			Nillable(),

		field.String("actor_type").
			Comment("操作主体类型（user/access_key）").
			NotEmpty().
			Immutable(),

		field.String("actor_id").
			Comment("操作主体ID").
			NotEmpty().
			Immutable(),

		field.String("owner_service").
			Comment("owner 服务标识（如 ani-gpu-simulator）").
			NotEmpty().
			Immutable(),

		// action 使用字符串 + adapter 注册表（LAB_GPU_CREATE/LAB_GPU_DELETE），
		// 不写入通用账本枚举。
		field.String("action").
			Comment("业务动作（adapter 注册表键）").
			NotEmpty().
			Immutable(),

		field.String("idempotency_key").
			Comment("幂等键（UUID）").
			NotEmpty().
			Immutable(),

		// 幂等哈希：可信 tenant/actor/action/owner + 校验后业务参数；
		// 不含 request-id、时间戳或新生成 UUID。
		field.String("request_hash").
			Comment("规范请求哈希").
			NotEmpty().
			Immutable(),

		// Server-selected immutable instance admission contract; old rows remain v1.
		field.Uint32("instance_contract_version").Default(1).Min(1).Max(2).Immutable(),

		// 经校验的业务参数（schema_version=1，固定字段顺序）。
		// 不得保存 Bearer、密码、Cookie、签名头或私钥。
		field.Text("canonical_request").
			Comment("规范请求（schema_version=1）").
			NotEmpty().
			Immutable(),

		field.Enum("dispatch_state").
			Comment("投递状态").
			NamedValues(
				"Queued", "QUEUED",
				"Dispatching", "DISPATCHING",
				"Unknown", "UNKNOWN",
				"Acked", "ACKED",
				"CanceledUnsent", "CANCELED_UNSENT",
				"Rejected", "REJECTED",
			).
			Default("QUEUED"),

		field.Int("attempt_count").
			Comment("已尝试发送次数").
			Default(0),

		field.Int64("lease_generation").
			Comment("领取代次").
			Default(0),

		field.Bool("retry_blocked").
			Comment("永久合同错误暂停自动重试").
			Default(false),

		field.String("last_error_code").
			Comment("最近错误码").
			NotEmpty().
			Optional().
			Nillable(),

		field.Time("next_attempt_at").
			Comment("下次尝试时间（退避）").
			Optional().
			Nillable(),

		field.String("lease_owner").
			Comment("当前租约持有者").
			NotEmpty().
			Optional().
			Nillable(),

		field.Time("lease_until").
			Comment("租约到期时间").
			Optional().
			Nillable(),

		// 下游 ACK 响应原文（审计用）。
		field.Text("ack_json").
			Comment("ACK 响应（JSON）").
			Optional().
			Nillable(),
	}
}

// Mixin of the QuotaOperation.
func (QuotaOperation) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.AutoIncrementId{},
		mixin.TimeAt{},
		mixin.TenantID[uint32]{},
	}
}

// Indexes of the QuotaOperation.
func (QuotaOperation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "operation_id", "resource_tenant_id", "owner_service", "resource_id").Unique().StorageKey("uix_sys_quota_operations_usage_ref"),
		index.Fields("tenant_id", "operation_id").Unique().
			StorageKey("uix_sys_quota_operations_tenant_id_operation_id"),
		// operation 幂等唯一键：同租户同主体同动作同幂等键。
		index.Fields("tenant_id", "actor_type", "actor_id", "action", "idempotency_key").Unique().
			StorageKey("uix_sys_quota_operations_idempotency"),
		// worker 领取扫描。
		index.Fields("dispatch_state", "next_attempt_at").StorageKey("idx_sys_quota_operations_dispatch"),
		// 原始创建记录的条件唯一：同租户同 owner 同资源只允许一个创建操作。
		// 不把 LAB_GPU_CREATE 写入索引条件（按 action 无关的资源身份约束）。
		index.Fields("tenant_id", "owner_service", "resource_id").Unique().
			StorageKey("uix_sys_quota_operations_create_resource").
			Annotations(entsql.IndexWhere("create_operation_id IS NULL")),
	}
}
