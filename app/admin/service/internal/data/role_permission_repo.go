package data

import (
	"context"
	"time"

	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	"go-wind-admin/pkg/localdeps/go-utils/copierutil"
	"go-wind-admin/pkg/localdeps/go-utils/mapper"
	"go-wind-admin/pkg/localdeps/go-utils/timeutil"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"

	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"
)

type RolePermissionRepo struct {
	log       *bLogger.Helper
	entClient *entCrud.EntClient[*ent.Client]

	mapper          *mapper.CopierMapper[permissionV1.RolePermission, ent.RolePermission]
	statusConverter *mapper.EnumTypeConverter[permissionV1.RolePermission_Status, rolepermission.Status]
	effectConverter *mapper.EnumTypeConverter[permissionV1.RolePermission_EffectiveStatus, rolepermission.Effect]
}

func NewRolePermissionRepo(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *RolePermissionRepo {
	repo := &RolePermissionRepo{
		log:       ctx.NewLoggerHelper("role-permission/repo/admin-service"),
		entClient: entClient,
		mapper:    mapper.NewCopierMapper[permissionV1.RolePermission, ent.RolePermission](),
		statusConverter: mapper.NewEnumTypeConverter[permissionV1.RolePermission_Status, rolepermission.Status](
			permissionV1.RolePermission_Status_name,
			permissionV1.RolePermission_Status_value,
		),
		effectConverter: mapper.NewEnumTypeConverter[permissionV1.RolePermission_EffectiveStatus, rolepermission.Effect](
			permissionV1.RolePermission_EffectiveStatus_name,
			permissionV1.RolePermission_EffectiveStatus_value,
		),
	}

	repo.init()

	return repo
}

func (r *RolePermissionRepo) init() {
	r.mapper.AppendConverters(copierutil.NewTimeStringConverterPair())
	r.mapper.AppendConverters(copierutil.NewTimeTimestamppbConverterPair())

	r.mapper.AppendConverters(r.statusConverter.NewConverterPair())
	r.mapper.AppendConverters(r.effectConverter.NewConverterPair())
}

// CleanPermissions 清理角色的所有权限
func (r *RolePermissionRepo) CleanPermissions(
	ctx context.Context,
	tx *ent.Tx,
	roleID uint32,
) error {
	if _, err := tx.RolePermission.Delete().
		Where(
			rolepermission.RoleIDEQ(roleID),
		).
		Exec(ctx); err != nil {
		r.log.Errorf(context.Background(), "delete old role [%d] permissions failed: %s", roleID, err.Error())
		return permissionV1.ErrorInternalServerError("delete old role permissions failed")
	}
	return nil
}

func (r *RolePermissionRepo) BatchCreate(ctx context.Context, tx *ent.Tx, datas []*permissionV1.RolePermission) error {
	if len(datas) == 0 {
		return nil
	}

	now := time.Now()

	builders := make([]*ent.RolePermissionCreate, 0, len(datas))
	for _, data := range datas {
		builder := tx.RolePermission.Create().
			SetNillableTenantID(data.TenantId).
			SetRoleID(data.GetRoleId()).
			SetPermissionID(data.GetPermissionId()).
			SetNillableStatus(r.statusConverter.ToEntity(data.Status)).
			SetNillableEffect(r.effectConverter.ToEntity(data.Effect)).
			SetNillablePriority(data.Priority).
			SetNillableCreatedBy(data.CreatedBy).
			SetCreatedAt(now)

		builders = append(builders, builder)
	}

	err := tx.RolePermission.CreateBulk(builders...).Exec(ctx)
	if err != nil {
		r.log.Errorf(ctx, "batch create role permissions failed: %s", err.Error())
		return permissionV1.ErrorInternalServerError("batch create role permissions failed")
	}

	return nil
}

// Upsert 创建或更新角色权限关联
func (r *RolePermissionRepo) Upsert(ctx context.Context, tx *ent.Tx, data *permissionV1.RolePermission) error {
	var operatorID *uint32
	if data.UpdatedBy != nil {
		operatorID = data.UpdatedBy
	} else {
		operatorID = data.CreatedBy
	}
	if operatorID == nil {
		r.log.Errorf(ctx, "operator ID is nil for upsert role permission")
		return permissionV1.ErrorInternalServerError("operator ID is required for upsert role permission")
	}

	var now *time.Time
	if data.UpdatedAt != nil {
		t := timeutil.TimestamppbToTime(data.UpdatedAt)
		now = t
	} else if data.CreatedAt != nil {
		t := timeutil.TimestamppbToTime(data.CreatedAt)
		now = t
	}
	if now == nil {
		now = trans.Ptr(time.Now())
	}

	builder := tx.RolePermission.Create().
		SetNillableTenantID(data.TenantId).
		SetRoleID(data.GetRoleId()).
		SetPermissionID(data.GetPermissionId()).
		SetNillableStatus(r.statusConverter.ToEntity(data.Status)).
		SetNillableEffect(r.effectConverter.ToEntity(data.Effect)).
		SetNillablePriority(data.Priority).
		SetNillableCreatedBy(operatorID).
		SetNillableCreatedAt(now).
		OnConflictColumns(
			rolepermission.FieldTenantID,
			rolepermission.FieldPermissionID,
			rolepermission.FieldRoleID,
		).
		UpdateNewValues().
		SetUpdatedBy(*operatorID).
		SetUpdatedAt(*now)

	if data.Status != nil {
		builder.SetStatus(*r.statusConverter.ToEntity(data.Status))
	}
	if data.Effect != nil {
		builder.SetEffect(*r.effectConverter.ToEntity(data.Effect))
	}
	if data.Priority != nil {
		builder.SetPriority(*data.Priority)
	}

	err := builder.Exec(ctx)
	if err != nil {
		r.log.Errorf(ctx, "create role permission failed: %s", err.Error())
		return permissionV1.ErrorInternalServerError("create role permission failed")
	}

	return nil
}

// ReplacePermissions applies the submitted set without rebuilding grants that
// remain selected. Their IDs, status, effect, priority and audit history survive
// an additive role update. RoleRepo.Update holds the role row in this transaction.
func (r *RolePermissionRepo) ReplacePermissions(ctx context.Context, tx *ent.Tx,
	tenantID, operatorID uint32,
	roleID uint32, permissionIDs []uint32,
) error {
	existing, err := tx.RolePermission.Query().Where(
		rolepermission.TenantIDEQ(tenantID), rolepermission.RoleIDEQ(roleID),
	).All(ctx)
	if err != nil {
		return permissionV1.ErrorInternalServerError("read role permissions failed")
	}
	selected := make(map[uint32]bool, len(permissionIDs))
	for _, id := range permissionIDs {
		selected[id] = true
	}
	var remove []uint32
	for _, row := range existing {
		if row.PermissionID != nil && selected[*row.PermissionID] {
			delete(selected, *row.PermissionID)
		} else {
			remove = append(remove, row.ID)
		}
	}
	if len(remove) > 0 {
		if _, err = tx.RolePermission.Delete().Where(
			rolepermission.TenantIDEQ(tenantID), rolepermission.RoleIDEQ(roleID), rolepermission.IDIn(remove...),
		).Exec(ctx); err != nil {
			return permissionV1.ErrorInternalServerError("remove role permissions failed")
		}
	}
	add := make([]uint32, 0, len(selected))
	for id := range selected {
		add = append(add, id)
	}
	return r.AssignPermissions(ctx, tx, tenantID, operatorID, roleID, add)
}

// AssignPermissions 给角色分配权限
func (r *RolePermissionRepo) AssignPermissions(ctx context.Context, tx *ent.Tx,
	tenantID, operatorID uint32,
	roleID uint32, permissionIDs []uint32,
) error {
	if len(permissionIDs) == 0 {
		return nil
	}

	now := time.Now()

	for _, permissionID := range permissionIDs {
		rp := tx.RolePermission.
			Create().
			SetTenantID(tenantID).
			SetPermissionID(permissionID).
			SetRoleID(roleID).
			SetCreatedBy(operatorID).
			SetCreatedAt(now).
			OnConflictColumns(
				rolepermission.FieldTenantID,
				rolepermission.FieldPermissionID,
				rolepermission.FieldRoleID,
			).
			UpdateNewValues().
			SetUpdatedBy(operatorID).
			SetUpdatedAt(now)

		if err := rp.Exec(ctx); err != nil {
			r.log.Errorf(ctx, "assign permission to role failed: %s", err.Error())
			return permissionV1.ErrorInternalServerError("assign permission to role failed")
		}
	}

	return nil
}

// ListPermissionIDs 列出角色的权限ID列表
func (r *RolePermissionRepo) ListPermissionIDs(ctx context.Context, roleID uint32) ([]uint32, error) {
	q := r.entClient.Client().RolePermission.Query().
		Where(
			rolepermission.RoleIDEQ(roleID),
			rolepermission.StatusEQ(rolepermission.StatusOn),
			rolepermission.EffectEQ(rolepermission.EffectAllow),
		)

	intIDs, err := q.
		Select(rolepermission.FieldPermissionID).
		Ints(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query permission ids by role id failed: %s", err.Error())
		return nil, permissionV1.ErrorInternalServerError("query permission ids by role id failed")
	}
	ids := make([]uint32, len(intIDs))
	for i, v := range intIDs {
		ids[i] = uint32(v)
	}
	return ids, nil
}

// ListPermissionIDsByRoleIDs 根据角色ID列表获取权限ID列表
func (r *RolePermissionRepo) ListPermissionIDsByRoleIDs(ctx context.Context, roleIDs []uint32) ([]uint32, error) {
	q := r.entClient.Client().RolePermission.Query().
		Where(
			rolepermission.RoleIDIn(roleIDs...),
			rolepermission.StatusEQ(rolepermission.StatusOn),
			rolepermission.EffectEQ(rolepermission.EffectAllow),
		)

	intIDs, err := q.
		Select(rolepermission.FieldPermissionID).
		Ints(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query permission ids by role ids failed: %s", err.Error())
		return nil, permissionV1.ErrorInternalServerError("query permission ids by role ids failed")
	}
	ids := make([]uint32, len(intIDs))
	for i, v := range intIDs {
		ids[i] = uint32(v)
	}
	return ids, nil
}

// RemovePermissions 移除角色的部分权限
func (r *RolePermissionRepo) RemovePermissions(ctx context.Context, tenantID, roleID uint32, permissionIDs []uint32) error {
	_, err := r.entClient.Client().RolePermission.Delete().
		Where(
			rolepermission.And(
				rolepermission.RoleIDEQ(roleID),
				rolepermission.TenantIDEQ(tenantID),
				rolepermission.PermissionIDIn(permissionIDs...),
			),
		).
		Exec(ctx)
	if err != nil {
		r.log.Errorf(ctx, "remove roles by role id failed: %s", err.Error())
		return permissionV1.ErrorInternalServerError("remove roles by role id failed")
	}
	return nil
}

func (r *RolePermissionRepo) ListPermissionsByRoleID(ctx context.Context, roleID uint32) ([]*permissionV1.RolePermission, error) {
	entities, err := r.entClient.Client().RolePermission.Query().
		Where(
			rolepermission.RoleIDEQ(roleID),
		).
		All(ctx)
	if err != nil {
		r.log.Errorf(ctx, "list role permissions by role id failed: %s", err.Error())
		return nil, permissionV1.ErrorInternalServerError("list role permissions by role id failed")
	}

	results := make([]*permissionV1.RolePermission, 0, len(entities))
	for _, entity := range entities {
		results = append(results, r.mapper.ToDTO(entity))
	}

	return results, nil
}
