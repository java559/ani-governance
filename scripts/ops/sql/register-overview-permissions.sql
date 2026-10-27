-- 首页资源统计卡片权限登记：在 admin sync-apis 之后执行（sys_apis 需已含新路由）。
-- 覆盖：权限码 overview:resource-count:read、API 关联、角色继承授予。
-- 入口挂 INSTANCE 业务模块（现有 plan2/plan3 均含 INSTANCE、均无 DASHBOARD，
-- 不改套餐数据）；凡当前持有 instance:read 的角色同批授予，不写死角色 ID。
-- 幂等可重复执行；路由缺失、重复或已关联其他权限时 fail closed。
-- 执行：psql -v ON_ERROR_STOP=1 -d ani_governance -1 -f register-overview-permissions.sql
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
DO $$
DECLARE
  aid bigint; pid bigint; route_count bigint; instance_read_pid bigint;
BEGIN
  -- 1) 目录校验：admin sync-apis 必须已同步且仅同步一次本路由
  --    （module 来自 OpenAPI tag，business_module 来自 ServiceTagToBusinessModule）。
  SELECT count(*),min(id) INTO route_count,aid FROM sys_apis
   WHERE path='/api/v1/overview' AND method='GET'
     AND module='OverviewService' AND business_module='INSTANCE'
     AND scope='ADMIN' AND status='ON' AND deleted_at IS NULL;
  IF route_count<>1 THEN
    RAISE EXCEPTION 'overview route must be synced exactly once by admin sync-apis first';
  END IF;

  -- 2) 权限点
  INSERT INTO sys_permissions(name,code,status,created_at)
  VALUES('overview:resource-count:read','overview:resource-count:read','ON',now())
  ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions
   WHERE code='overview:resource-count:read' AND deleted_at IS NULL;

  -- 3) 权限→API 关联（fail closed：路由已关联其他权限时拒绝）
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL) THEN
    RAISE EXCEPTION 'overview route has a foreign permission; review the existing grant';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now())
  ON CONFLICT(permission_id,api_id) DO NOTHING;

  -- 4) 角色继承授予：凡当前持有 instance:read 的（租户,角色）同批授予首页统计读取，
  --    不写死角色 ID；新租户管理员按既有 instance:read 授予自动获得。
  SELECT id INTO STRICT instance_read_pid FROM sys_permissions
   WHERE code='instance:read' AND deleted_at IS NULL;
  INSERT INTO sys_role_permissions(created_at,tenant_id,status,role_id,permission_id,effect,priority)
  SELECT now(),rp.tenant_id,'ON',rp.role_id,pid,'ALLOW',0
  FROM sys_role_permissions rp
  WHERE rp.permission_id=instance_read_pid AND rp.deleted_at IS NULL
  ON CONFLICT DO NOTHING;
END $$;
COMMIT;

-- 回显落库结果
SELECT code, status FROM sys_permissions WHERE code='overview:resource-count:read';
SELECT a.path, a.method, a.module, a.business_module FROM sys_apis a
 WHERE a.path='/api/v1/overview' AND a.method='GET' AND a.deleted_at IS NULL;
SELECT rp.tenant_id, rp.role_id, p.code FROM sys_role_permissions rp
 JOIN sys_permissions p ON p.id=rp.permission_id
 WHERE p.code='overview:resource-count:read' AND rp.deleted_at IS NULL
 ORDER BY rp.tenant_id, rp.role_id;
