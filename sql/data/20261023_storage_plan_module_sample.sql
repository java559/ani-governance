-- 套餐模块白名单示例：仅为指定套餐显式开启 STORAGE。
-- 该脚本不进入默认迁移，也不自动为任何存量套餐添加 STORAGE；由运营按租户确认后执行。
-- 用法：psql -v ON_ERROR_STOP=1 -v plan_id=<套餐 ID> -1 -f 20261023_storage_plan_module_sample.sql
INSERT INTO sys_plan_modules (plan_id, module, created_at)
SELECT p.id, 'STORAGE', NOW()
FROM sys_plans p
WHERE p.id = :'plan_id'
  AND NOT EXISTS (
    SELECT 1 FROM sys_plan_modules pm
    WHERE pm.plan_id = p.id AND pm.module = 'STORAGE' AND pm.deleted_at IS NULL
  );
-- 执行后必须重启所有 Gov 实例（或在套餐管理页面刷新当前实例），让 Casbin 策略重新加载。
-- 回退：DELETE FROM sys_plan_modules WHERE plan_id = :'plan_id' AND module = 'STORAGE';
