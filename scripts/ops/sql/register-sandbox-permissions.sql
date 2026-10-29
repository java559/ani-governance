-- Run only AFTER admin sync-apis --dry-run and explicit sync-apis.
-- New permissions are OFF. Preserve existing status, role grants and plan modules.
-- The compute directory intentionally has separate grants for each usage.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
LOCK TABLE sys_permissions, sys_apis, sys_permission_apis IN SHARE ROW EXCLUSIVE MODE;
DO $$
DECLARE r record; aid bigint; pid bigint;
BEGIN
 FOR r IN SELECT * FROM (VALUES
  ('POST','/api/v1/sandbox','sandbox:create','SandboxService','INSTANCE','CreateSandbox'),
  ('GET','/api/v1/sandbox','sandbox:list','SandboxService','INSTANCE','ListSandboxes'),
  ('GET','/api/v1/sandbox/{sandbox_id}','sandbox:read','SandboxService','INSTANCE','GetSandbox'),
  ('GET','/api/v1/sandbox/operations/{operation_id}','sandbox:operation:read','SandboxService','INSTANCE','GetSandboxOperation'),
  ('POST','/api/v1/sandbox/{data.sandbox_id}:pause','sandbox:pause','SandboxService','INSTANCE','PauseSandbox'),
  ('POST','/api/v1/sandbox/{data.sandbox_id}:resume','sandbox:resume','SandboxService','INSTANCE','ResumeSandbox'),
  ('DELETE','/api/v1/sandbox/{data.sandbox_id}','sandbox:delete','SandboxService','INSTANCE','DeleteSandbox'),
  ('GET','/api/v1/compute-presets','sandbox:preset:read','ComputePresetService','SYSTEM','ListPresets')
 ) AS routes(method,path,code,module,business_module,rpc) LOOP
  IF (SELECT count(*) FROM sys_apis WHERE path=r.path AND method=r.method) <> 1 THEN
   RAISE EXCEPTION 'Sandbox route missing or ambiguous: % %',r.method,r.path;
  END IF;
  SELECT id INTO STRICT aid FROM sys_apis WHERE path=r.path AND method=r.method
   AND deleted_at IS NULL AND scope='ADMIN' AND status='ON'
   AND module=r.module AND business_module=r.business_module
   AND operation=r.module||'_'||r.rpc;
  INSERT INTO sys_permissions(name,code,status,created_at)
   VALUES(r.code,r.code,'OFF',now()) ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.code AND deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM sys_permission_apis pa JOIN sys_permissions p ON p.id=pa.permission_id
   WHERE pa.api_id=aid AND pa.permission_id<>pid AND pa.deleted_at IS NULL
    AND NOT (r.code='sandbox:preset:read' AND p.deleted_at IS NULL AND p.code IN (
     'instance:preset:read','modeldev:compute-preset:read','inference:compute-preset:read','vm:compute-preset:read'))) THEN
   RAISE EXCEPTION 'Sandbox route has an unexpected permission: % %',r.method,r.path;
  END IF;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id=pid AND deleted_at IS NOT NULL) THEN
   RAISE EXCEPTION 'Sandbox route grant is deleted: % %',r.method,r.path;
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now())
   ON CONFLICT(permission_id,api_id) DO NOTHING;
 END LOOP;
END $$;
COMMIT;
