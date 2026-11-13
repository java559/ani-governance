-- Explicit catalog preparation for the platform image management routes.
--
-- Run AFTER `admin sync-apis --dry-run` and the explicit `admin sync-apis`, which
-- is what creates the sys_apis rows. This file only registers the 22 permission
-- codes and their API links. No role grants: the platform:admin grant lives in
-- grant-platform-image-permissions.sql.
--
-- Repeatable and atomic. Run with psql -v ON_ERROR_STOP=1 -f this file.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
LOCK TABLE sys_permissions,sys_apis,sys_permission_apis IN SHARE ROW EXCLUSIVE MODE;

-- 只记录本次真正插入的行：用 INSERT ... RETURNING 捕获，不按路径/码前缀反查。
-- 反查会在重复执行时把上次已存在的行也算进来，回退时误删历史授权。
-- 两张表必须活过 COMMIT（下面的清单查询在事务外执行），因此不用 ON COMMIT DROP；
-- 它们只在本次 psql 会话内可见，会话结束即消失。
DROP TABLE IF EXISTS _new_image_permissions;
DROP TABLE IF EXISTS _new_image_permission_apis;
CREATE TEMP TABLE _new_image_permissions (id bigint PRIMARY KEY);
CREATE TEMP TABLE _new_image_permission_apis (id bigint PRIMARY KEY);

DO $$
DECLARE r record; aid bigint; pid bigint; created_pid bigint; created_link bigint;
BEGIN
 FOR r IN SELECT * FROM (VALUES
  ('GET','/api/v1/admin/images/space','image:platform:space:get','PlatformImageService'),
  ('POST','/api/v1/admin/images/space:enable','image:platform:space:enable','PlatformImageService'),
  ('GET','/api/v1/admin/images/publisher-credential','image:platform:credential:get','PlatformImageService'),
  ('POST','/api/v1/admin/images/publisher-credential:issue','image:platform:credential:issue','PlatformImageService'),
  ('POST','/api/v1/admin/images/publisher-credential:reset','image:platform:credential:reset','PlatformImageService'),
  ('POST','/api/v1/admin/images/publisher-credential:disable','image:platform:credential:disable','PlatformImageService'),
  ('GET','/api/v1/admin/images/registrations','image:platform:registration:list','PlatformImageService'),
  ('GET','/api/v1/admin/images/registrations/{image_id}','image:platform:registration:get','PlatformImageService'),
  ('POST','/api/v1/admin/images/registrations','image:platform:registration:create','PlatformImageService'),
  ('PATCH','/api/v1/admin/images/registrations/{image_id}','image:platform:registration:update','PlatformImageService'),
  ('POST','/api/v1/admin/images/registrations/{image_id}:unregister','image:platform:registration:unregister','PlatformImageService'),
  ('GET','/api/v1/admin/tenants/{target_tenant_id}/images/space','image:platform:tenant-space:get','AdminTenantImageService'),
  ('POST','/api/v1/admin/tenants/{target_tenant_id}/images/space:enable','image:platform:tenant-space:enable','AdminTenantImageService'),
  ('GET','/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential','image:platform:tenant-credential:get','AdminTenantImageService'),
  ('POST','/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential:issue','image:platform:tenant-credential:issue','AdminTenantImageService'),
  ('POST','/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential:reset','image:platform:tenant-credential:reset','AdminTenantImageService'),
  ('POST','/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential:disable','image:platform:tenant-credential:disable','AdminTenantImageService'),
  ('GET','/api/v1/admin/tenants/{target_tenant_id}/images/registrations','image:platform:tenant-registration:list','AdminTenantImageService'),
  ('GET','/api/v1/admin/tenants/{target_tenant_id}/images/registrations/{image_id}','image:platform:tenant-registration:get','AdminTenantImageService'),
  ('POST','/api/v1/admin/tenants/{target_tenant_id}/images/registrations','image:platform:tenant-registration:create','AdminTenantImageService'),
  ('PATCH','/api/v1/admin/tenants/{target_tenant_id}/images/registrations/{image_id}','image:platform:tenant-registration:update','AdminTenantImageService'),
  ('POST','/api/v1/admin/tenants/{target_tenant_id}/images/registrations/{image_id}:unregister','image:platform:tenant-registration:unregister','AdminTenantImageService')
 ) AS routes(method,path,code,service) LOOP
  -- The route must already exist with the image service identity. A missing row
  -- means sync-apis ran against an older binary, not that the code may be skipped.
  SELECT id INTO STRICT aid FROM sys_apis WHERE path=r.path AND method=r.method
   AND module=r.service AND business_module='IMAGE' AND scope='ADMIN'
   AND status='ON' AND deleted_at IS NULL;
  INSERT INTO sys_permissions(name,code,status,created_at) VALUES(r.code,r.code,'ON',now())
   ON CONFLICT(code) DO NOTHING RETURNING id INTO created_pid;
  IF created_pid IS NOT NULL THEN
   INSERT INTO _new_image_permissions(id) VALUES(created_pid) ON CONFLICT DO NOTHING;
  END IF;
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.code AND deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL) THEN
   RAISE EXCEPTION 'platform image route % % has a foreign permission; review existing grants', r.method, r.path;
  END IF;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id=pid AND deleted_at IS NOT NULL) THEN
   RAISE EXCEPTION 'platform image route permission was deleted; explicit recovery required';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now())
   ON CONFLICT(permission_id,api_id) DO NOTHING RETURNING id INTO created_link;
  IF created_link IS NOT NULL THEN
   INSERT INTO _new_image_permission_apis(id) VALUES(created_link) ON CONFLICT DO NOTHING;
  END IF;
  created_pid := NULL;
  created_link := NULL;
 END LOOP;
END $$;
COMMIT;

-- Identifiers created by this run, for exact reversal. Capture them with
--   psql -X -A -t -v ON_ERROR_STOP=1 -f this-file.sql > register-output.txt
-- Re-running when nothing is left to insert returns empty arrays, so the rollback
-- of a repeat run cannot revoke rows that existed before it.
SELECT json_build_object(
  'permissions', (SELECT json_agg(json_build_object('id',p.id,'code',p.code) ORDER BY p.id)
             FROM _new_image_permissions n JOIN sys_permissions p ON p.id=n.id),
  'permission_apis', (SELECT json_agg(pa.id ORDER BY pa.id)
             FROM _new_image_permission_apis n JOIN sys_permission_apis pa ON pa.id=n.id)
);
