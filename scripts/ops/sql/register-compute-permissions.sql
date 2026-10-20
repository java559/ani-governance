-- Fresh catalog preparation AFTER admin sync-apis --dry-run and sync-apis.
-- Existing instance:preset:admin permissions require the explicit migration.
-- No role grants, plan changes, or updates to existing status/restrictions.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
LOCK TABLE sys_permissions, sys_apis, sys_permission_apis IN SHARE ROW EXCLUSIVE MODE;
DO $$
DECLARE r record; aid bigint; pid bigint;
BEGIN
 IF EXISTS(SELECT 1 FROM sys_permissions WHERE code IN (
  'instance:preset:admin:list','instance:preset:admin:read','instance:preset:admin:create',
  'instance:preset:admin:update','instance:preset:admin:delete')) THEN
  RAISE EXCEPTION 'Existing preset permissions require migrate-compute-preset-permissions.sql';
 END IF;
 FOR r IN SELECT * FROM (VALUES
  ('GET','/api/v1/admin/compute-presets','list','AdminListPresets'),
  ('GET','/api/v1/admin/compute-presets/{id}','read','AdminGetPreset'),
  ('POST','/api/v1/admin/compute-presets','create','AdminCreatePreset'),
  ('PATCH','/api/v1/admin/compute-presets/{id}','update','AdminUpdatePreset'),
  ('POST','/api/v1/admin/compute-presets/{id}:set-enabled','update','AdminSetPresetEnabled'),
  ('DELETE','/api/v1/admin/compute-presets/{id}','delete','AdminDeletePreset')
 ) AS routes(method,path,action,rpc) LOOP
  IF (SELECT count(*) FROM sys_apis WHERE path=r.path AND method=r.method) <> 1 THEN
   RAISE EXCEPTION 'Compute route missing or ambiguous: % %', r.method,r.path;
  END IF;
  SELECT id INTO STRICT aid FROM sys_apis WHERE path=r.path AND method=r.method
   AND deleted_at IS NULL AND scope='ADMIN' AND module='ComputePresetService'
   AND business_module='SYSTEM' AND operation='ComputePresetService_'||r.rpc;
  INSERT INTO sys_permissions(name,code,status,created_at)
   VALUES('compute:preset:admin:'||r.action,'compute:preset:admin:'||r.action,'OFF',now())
   ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions
   WHERE code='compute:preset:admin:'||r.action AND deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL) THEN
   RAISE EXCEPTION 'Compute route has a foreign permission: % %',r.method,r.path;
  END IF;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id=pid AND deleted_at IS NOT NULL) THEN
   RAISE EXCEPTION 'Compute route grant is deleted: % %',r.method,r.path;
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now())
   ON CONFLICT(permission_id,api_id) DO NOTHING;
 END LOOP;
END $$;
COMMIT;
