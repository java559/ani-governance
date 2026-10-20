-- Explicit upgrade with matching Gov/Res binaries stopped. Review/backup first.
-- Rename in place: preserve permission/API IDs, all grants, status and audit data.
-- This transaction is independent of the Resource database migration.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
LOCK TABLE sys_permissions, sys_apis, sys_permission_apis, sys_role_permissions IN SHARE ROW EXCLUSIVE MODE;
DO $$
DECLARE r record; pid bigint; aid bigint; old_id bigint; new_id bigint;
BEGIN
 FOR r IN SELECT * FROM (VALUES ('list'),('read'),('create'),('update'),('delete')) AS actions(action) LOOP
  SELECT id INTO old_id FROM sys_permissions WHERE code='instance:preset:admin:'||r.action;
  SELECT id INTO new_id FROM sys_permissions WHERE code='compute:preset:admin:'||r.action;
  IF old_id IS NOT NULL AND new_id IS NOT NULL THEN
   RAISE EXCEPTION 'Preset permission target conflict: %',r.action;
  END IF;
  pid := coalesce(old_id,new_id);
  IF pid IS NULL OR NOT EXISTS(SELECT 1 FROM sys_permissions WHERE id=pid AND deleted_at IS NULL) THEN
   RAISE EXCEPTION 'Preset permission missing or deleted: %',r.action;
  END IF;
  IF old_id IS NOT NULL THEN
   UPDATE sys_permissions SET code='compute:preset:admin:'||r.action WHERE id=old_id;
  END IF;
 END LOOP;
 FOR r IN SELECT * FROM (VALUES
  ('GET','/api/v1/admin/compute-presets','list','AdminListPresets'),
  ('GET','/api/v1/admin/compute-presets/{id}','read','AdminGetPreset'),
  ('POST','/api/v1/admin/compute-presets','create','AdminCreatePreset'),
  ('PATCH','/api/v1/admin/compute-presets/{id}','update','AdminUpdatePreset'),
  ('POST','/api/v1/admin/compute-presets/{id}:set-enabled','update','AdminSetPresetEnabled'),
  ('DELETE','/api/v1/admin/compute-presets/{id}','delete','AdminDeletePreset')
 ) AS routes(method,path,action,rpc) LOOP
  IF (SELECT count(*) FROM sys_apis WHERE path=r.path AND method=r.method) <> 1 THEN
   RAISE EXCEPTION 'Preset route missing or ambiguous: % %',r.method,r.path;
  END IF;
  SELECT id INTO STRICT aid FROM sys_apis WHERE path=r.path AND method=r.method
   AND deleted_at IS NULL AND scope='ADMIN'
   AND ((module='InstanceService' AND business_module='INSTANCE' AND operation='InstanceService_'||r.rpc)
     OR (module='ComputePresetService' AND business_module='SYSTEM' AND operation='ComputePresetService_'||r.rpc));
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code='compute:preset:admin:'||r.action AND deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL) THEN
   RAISE EXCEPTION 'Preset route has a foreign permission: % %',r.method,r.path;
  END IF;
  UPDATE sys_apis SET module='ComputePresetService',business_module='SYSTEM',operation='ComputePresetService_'||r.rpc
   WHERE id=aid AND (module, business_module, operation) IS DISTINCT FROM ('ComputePresetService','SYSTEM','ComputePresetService_'||r.rpc);
 END LOOP;
END $$;
COMMIT;
