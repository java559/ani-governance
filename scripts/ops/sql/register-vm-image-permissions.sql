-- Explicit catalog preparation for the tenant VM image routes.
--
-- Run AFTER `admin sync-apis --dry-run` and the explicit `admin sync-apis`, which
-- is what creates the sys_apis rows. This file only registers permission codes and
-- their API links. No role or plan grants and no quota/account changes: a tenant
-- still needs an explicit grant (see grant-vm-image-tenant-permissions.sql) and the
-- VIRTUAL_MACHINE plan module.
--
-- Repeatable and atomic. Run with psql -v ON_ERROR_STOP=1 -f this file.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
LOCK TABLE sys_permissions,sys_apis,sys_permission_apis IN SHARE ROW EXCLUSIVE MODE;
DO $$
DECLARE r record; aid bigint; pid bigint;
BEGIN
 FOR r IN SELECT * FROM (VALUES
 ('GET','/api/v1/virtual-machine-images','vm:read'),
 ('GET','/api/v1/virtual-machine-images/{image_id}','vm:read'),
 ('POST','/api/v1/virtual-machine-images','vm:image:upload'),
 ('POST','/api/v1/virtual-machine-images/{image_id}/upload-sessions','vm:image:upload'),
 ('PATCH','/api/v1/virtual-machine-images/{image_id}','vm:image:update'),
 ('DELETE','/api/v1/virtual-machine-images/{image_id}','vm:image:delete')
 ) AS routes(method,path,code) LOOP
  -- The route must already exist with the image service identity. A missing row
  -- means sync-apis ran against an older binary, not that the code may be skipped.
  SELECT id INTO STRICT aid FROM sys_apis WHERE path=r.path AND method=r.method
   AND module='VMImageService' AND business_module='VIRTUAL_MACHINE' AND scope='ADMIN'
   AND status='ON' AND deleted_at IS NULL;
  INSERT INTO sys_permissions(name,code,status,created_at) VALUES(r.code,r.code,'ON',now()) ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.code AND deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL) THEN
   RAISE EXCEPTION 'VM image route % % has a foreign permission; review existing grants', r.method, r.path;
  END IF;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id=pid AND deleted_at IS NOT NULL) THEN
   RAISE EXCEPTION 'VM image route permission was deleted; explicit recovery required';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now()) ON CONFLICT(permission_id,api_id) DO NOTHING;
 END LOOP;
END $$;
COMMIT;

-- Identifiers created by this catalog, for exact reversal. Capture them with
--   psql -X -A -t -v ON_ERROR_STOP=1 -f this-file.sql > register-output.txt
-- or run them again to list the retained ids.
SELECT json_build_object(
  'apis', (SELECT json_agg(json_build_object('id',id,'method',method,'path',path) ORDER BY id)
             FROM sys_apis WHERE path LIKE '/api/v1/virtual-machine-images%' AND deleted_at IS NULL),
  'permissions', (SELECT json_agg(json_build_object('id',id,'code',code) ORDER BY id)
             FROM sys_permissions WHERE code IN ('vm:image:upload','vm:image:update','vm:image:delete') AND deleted_at IS NULL),
  'permission_apis', (SELECT json_agg(pa.id ORDER BY pa.id)
             FROM sys_permission_apis pa
             JOIN sys_permissions p ON p.id=pa.permission_id
             JOIN sys_apis a ON a.id=pa.api_id
             WHERE p.code IN ('vm:read','vm:image:upload','vm:image:update','vm:image:delete')
               AND a.path LIKE '/api/v1/virtual-machine-images%' AND pa.deleted_at IS NULL)
);
