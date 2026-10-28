-- 平台组件日志流权限登记：在 admin sync-apis 之后执行（sys_apis 需已含新路由）。
-- 覆盖：权限码 component:log:read、API 关联、platform:admin 角色授予。
--
-- 与组件状态接口同域命名但**独立**：component:log:read 不替代 component:status:read，
-- 持有其中之一不能读另一条。本接口是平台级高权限只读入口（可看日志正文），
-- 只授予平台管理员角色（sys_roles.code='platform:admin'，tenant_id=0），
-- 不授予任何租户角色与 API Key，不开套餐模块；租户闸门对其 fail closed。
--
-- 幂等可重复执行；路由缺失、重复或已关联其他权限时 fail closed。
-- 执行：kubectl exec -i postgresql-0 -- psql -U postgres -v ON_ERROR_STOP=1 \
--       -d ani_governance -f - < register-platform-component-log-permissions.sql
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
DO $$
DECLARE
  aid bigint; pid bigint; route_count bigint; platform_role_id bigint;
BEGIN
  -- 1) 目录校验：admin sync-apis 必须已同步且仅同步一次本路由
  --    （module 来自 OpenAPI tag，business_module 来自 ServiceTagToBusinessModule）。
  SELECT count(*),min(id) INTO route_count,aid FROM sys_apis
   WHERE path='/api/v1/platform/components/{component}/logs/stream' AND method='GET'
     AND module='PlatformComponentService' AND business_module='SYSTEM'
     AND scope='ADMIN' AND status='ON' AND deleted_at IS NULL;
  IF route_count<>1 THEN
    RAISE EXCEPTION 'platform component log stream route must be synced exactly once by admin sync-apis first';
  END IF;

  -- 2) 权限点
  INSERT INTO sys_permissions(name,code,status,created_at)
  VALUES('component:log:read','component:log:read','ON',now())
  ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions
   WHERE code='component:log:read' AND deleted_at IS NULL;

  -- 3) 权限→API 关联（fail closed：路由已关联其他权限时拒绝）
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL) THEN
    RAISE EXCEPTION 'platform component log stream route has a foreign permission; review the existing grant';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now())
  ON CONFLICT(permission_id,api_id) DO NOTHING;

  -- 4) 角色授予：仅平台管理员角色（缺角色即报错，绝不静默 no-op）。
  SELECT id INTO STRICT platform_role_id FROM sys_roles
   WHERE code='platform:admin' AND tenant_id=0 AND type='SYSTEM'
     AND status='ON' AND deleted_at IS NULL;
  INSERT INTO sys_role_permissions(created_at,tenant_id,status,role_id,permission_id,effect,priority)
  VALUES(now(),0,'ON',platform_role_id,pid,'ALLOW',0)
  ON CONFLICT DO NOTHING;
END $$;
COMMIT;

-- 回显落库结果
SELECT code, status FROM sys_permissions WHERE code='component:log:read';
SELECT a.path, a.method, a.module, a.business_module FROM sys_apis a
 WHERE a.path='/api/v1/platform/components/{component}/logs/stream' AND a.method='GET' AND a.deleted_at IS NULL;
SELECT rp.tenant_id, rp.role_id, p.code FROM sys_role_permissions rp
 JOIN sys_permissions p ON p.id=rp.permission_id
 WHERE p.code='component:log:read' AND rp.deleted_at IS NULL
 ORDER BY rp.tenant_id, rp.role_id;
-- 交叉核对：两条能力互不替代，不得出现同一权限码挂两条路由
SELECT p.code, count(*) FROM sys_permission_apis pa
 JOIN sys_permissions p ON p.id=pa.permission_id
 WHERE p.code IN ('component:status:read','component:log:read') AND pa.deleted_at IS NULL
 GROUP BY p.code;
