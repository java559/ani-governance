package data

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/predicate"
	"go-wind-admin/app/admin/service/internal/data/ent/sysconfig"
)

const InstanceSchedulingSettingsPath = "/api/v1/admin/instance-scheduling-settings"
const instanceSchedulingKeyPrefix = "instance.scheduling."

func isInstanceSchedulingKey(key string) bool {
	return strings.HasPrefix(key, instanceSchedulingKeyPrefix)
}
func genericConfigPredicate() predicate.SysConfig {
	return sysconfig.Or(sysconfig.KeyIsNil(), sysconfig.Not(sysconfig.KeyHasPrefix(instanceSchedulingKeyPrefix)))
}
func instanceSchedulingKey(cluster string) (string, error) {
	if id, err := uuid.Parse(cluster); err != nil || id == uuid.Nil || id.String() != cluster {
		return "", fmt.Errorf("instance cluster is not configured")
	}
	return instanceSchedulingKeyPrefix + cluster, nil
}

// ReadInstanceSchedulingSettings bypasses the optional generic cache so a
// storage error is distinguishable from an unconfigured record.
func (r *ConfigRepo) ReadInstanceSchedulingSettings(ctx context.Context, cluster string) ([]byte, error) {
	key, err := instanceSchedulingKey(cluster)
	if err != nil {
		return nil, err
	}
	row, err := r.entClient.Client().SysConfig.Query().Where(sysconfig.KeyEQ(key)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read instance scheduling settings: %w", err)
	}
	if row.Value == nil || row.ValueType == nil || *row.ValueType != sysconfig.ValueTypeString {
		return nil, fmt.Errorf("invalid managed instance scheduling record")
	}
	return []byte(*row.Value), nil
}

// One existing global config row is replaced atomically under PostgreSQL's
// unique key/upsert semantics. No tenant-default layer or version table exists.
func (r *ConfigRepo) SaveInstanceSchedulingSettings(ctx context.Context, cluster string, actor uint32, value []byte) error {
	key, err := instanceSchedulingKey(cluster)
	if err != nil {
		return err
	}
	if actor == 0 || len(value) == 0 || len(value) > 65536 {
		return fmt.Errorf("invalid instance scheduling record")
	}
	now := time.Now().UTC()
	err = r.entClient.Client().SysConfig.Create().SetKey(key).SetName("Container scheduling settings").SetValue(string(value)).SetValueType(sysconfig.ValueTypeString).SetIsBuiltIn(true).SetCreatedAt(now).SetCreatedBy(actor).SetUpdatedAt(now).SetUpdatedBy(actor).
		OnConflictColumns(sysconfig.FieldKey).Update(func(u *ent.SysConfigUpsert) {
		u.SetValue(string(value)).SetValueType(sysconfig.ValueTypeString).SetIsBuiltIn(true).SetUpdatedAt(now).SetUpdatedBy(actor)
	}).Exec(ctx)
	if err != nil {
		return fmt.Errorf("save instance scheduling settings: %w", err)
	}
	r.invalidateCacheKey(ctx, key)
	return nil
}
