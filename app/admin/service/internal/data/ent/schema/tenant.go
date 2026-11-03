package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"go-wind-admin/pkg/localdeps/go-crud/entgo/mixin"
)

// Tenant holds the schema definition for the Tenant entity.
type Tenant struct {
	ent.Schema
}

func (Tenant) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table:     "sys_tenants",
			Charset:   "utf8mb4",
			Collation: "utf8mb4_bin",
		},
		entsql.WithComments(true),
		schema.Comment("租户表"),
	}
}

// Fields of the Tenant.
func (Tenant) Fields() []ent.Field {
	return []ent.Field{
		// 由治理持久生成，普通更新不提供 setter。 / Persistent immutable resource identity.
		field.String("resource_tenant_id").NotEmpty().Unique().Immutable().DefaultFunc(uuid.NewString),
		// The tenant row is also the durable tenant-space provisioning intent.
		// Existing rows are LEGACY; the tenant creation repository explicitly
		// records a new QUEUED intent and operation ID in the same transaction.
		field.Enum("tenant_space_state").Values("LEGACY", "QUEUED", "DISPATCHING", "UNKNOWN", "READY", "CLOSING", "CLOSED").Default("LEGACY"),
		field.String("tenant_space_operation_id").Optional().Nillable().NotEmpty().Unique().Immutable(),
		field.Int64("tenant_space_lease_generation").NonNegative().Default(0),
		field.String("tenant_space_lease_owner").Optional().Nillable().MaxLen(128),
		field.Time("tenant_space_lease_until").Optional().Nillable(),
		field.Time("tenant_space_next_attempt_at").Optional().Nillable(),
		field.String("name").
			Comment("租户名称").
			//Unique().
			NotEmpty().
			Optional().
			Nillable(),

		field.String("code").
			Comment("租户编号").
			// 唯一约束：code 是对外租户标识（登录接口的 tenant_name 取值来源），
			// 登录时按 code 解析租户，若允许重复则同一编号会命中多行、归属不确定。
			// name 保持自由文本不加约束，仅作展示。
			Unique().
			NotEmpty().
			Optional().
			Nillable(),

		field.String("logo_url").
			Comment("租户logo地址").
			Optional().
			Nillable(),

		field.String("domain").
			Comment("租户专属域名").
			Optional().
			Nillable(),

		field.String("industry").
			Comment("所属行业").
			Optional().
			Nillable(),

		field.Uint32("admin_user_id").
			Comment("管理员用户ID").
			Optional().
			Nillable(),

		field.Enum("status").
			Comment("租户状态").
			NamedValues(
				"On", "ON",
				"Off", "OFF",
				"Expired", "EXPIRED",
				"Freeze", "FREEZE",
			).
			Default("ON").
			Optional().
			Nillable(),

		field.Enum("type").
			Comment("租户类型").
			NamedValues(
				"Trial", "TRIAL",
				"Paid", "PAID",
				"Internal", "INTERNAL",
				"Partner", "PARTNER",
				"Custom", "CUSTOM",
			).
			Default("PAID").
			Optional().
			Nillable(),

		field.Enum("audit_status").
			Comment("审核状态").
			NamedValues(
				"Pending", "PENDING",
				"Approved", "APPROVED",
				"Rejected", "REJECTED",
			).
			Optional().
			Nillable(),

		field.Time("subscription_at").
			Comment("订阅时间").
			Optional().
			Nillable(),

		field.Time("unsubscribe_at").
			Comment("取消订阅时间").
			Optional().
			Nillable(),

		// Expose the existing nullable FK so tenant DTOs retain their subscribed plan.
		field.Uint32("plan_id").
			Optional().
			Nillable(),

		field.String("subscription_plan").
			Comment("订阅套餐").
			Optional().
			Nillable(),

		field.Time("expired_at").
			Comment("租户有效期").
			Optional().
			Nillable(),
	}
}

// Edges of the Tenant.
func (Tenant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("plan", Plan.Type).
			Ref("tenants").
			Field("plan_id").
			Unique(),
	}
}

// Mixin of the Tenant.
func (Tenant) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.AutoIncrementId{},
		mixin.TimeAt{},
		mixin.OperatorID{},
		mixin.Remark{},
	}
}

// Indexes of the Tenant.
func (Tenant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("id", "resource_tenant_id").Unique().StorageKey("uix_sys_tenants_id_resource_tenant"),
		// 保持 name 唯一
		index.Fields("name").
			Unique().
			StorageKey("idx_sys_tenant_name"),

		// 保持 code 唯一
		index.Fields("code").
			Unique().
			StorageKey("idx_sys_tenant_code"),

		// 按域名快速定位租户
		index.Fields("domain").StorageKey("idx_sys_tenant_domain"),

		// 按管理员查询
		index.Fields("admin_user_id").StorageKey("idx_sys_tenant_admin_user_id"),

		// 状态 + 审核状态联合过滤
		index.Fields("status", "audit_status").StorageKey("idx_sys_tenant_status_audit_status"),

		// 按类型 + 到期时间，用于分类型的到期筛选
		index.Fields("type", "expired_at").StorageKey("idx_sys_tenant_type_expired_at"),

		// 订阅时间（范围查询）
		index.Fields("subscription_at").StorageKey("idx_sys_tenant_subscription_at"),

		// 单列到期时间索引（方便单列范围查询）
		index.Fields("expired_at").StorageKey("idx_sys_tenant_expired_at"),

		// 操作者 + 创建时间，用于审计回溯（时间列放末尾以利于范围扫描）
		index.Fields("created_by", "created_at").StorageKey("idx_sys_tenant_created_by_created_at"),

		// 创建时间索引，用于租户列表的时间区间查询与分页
		index.Fields("created_at").StorageKey("idx_sys_tenant_created_at"),
	}
}
