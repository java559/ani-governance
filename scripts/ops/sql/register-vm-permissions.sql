-- Explicit catalog preparation AFTER admin sync-apis --dry-run and sync-apis.
-- No role or plan grants and no CPU/memory/disk quota/account changes.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
LOCK TABLE sys_permissions,sys_apis,sys_permission_apis IN SHARE ROW EXCLUSIVE MODE;
DO $$
DECLARE r record; aid bigint; pid bigint;
BEGIN
 FOR r IN SELECT * FROM (VALUES
 ('GET','/api/v1/virtual-machines','vm:list'),
 ('POST','/api/v1/virtual-machines','vm:create'),
 ('GET','/api/v1/virtual-machines/{vm_id}','vm:read'),
 ('DELETE','/api/v1/virtual-machines/{data.vm_id}','vm:delete'),
 ('GET','/api/v1/virtual-machines/{vm_id}/operations/{operation_id}','vm:read'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/start','vm:power'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/stop','vm:power'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/restart','vm:power'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/force-stop','vm:force-stop'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/console-sessions','vm:console'),
 ('GET','/api/v1/virtual-machines/{vm_id}/disks','vm:read'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/disks','vm:disk:write'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/disks/{data.disk_id}/attach','vm:disk:write'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/disks/{data.disk_id}/detach','vm:disk:write'),
 ('DELETE','/api/v1/virtual-machines/{data.vm_id}/disks/{data.disk_id}','vm:disk:write'),
 ('GET','/api/v1/virtual-machine-media','vm:read'),
 ('GET','/api/v1/virtual-machine-dns-defaults','vm:create'),
 ('PUT','/api/v1/virtual-machines/{data.vm_id}/media/{data.slot}','vm:media:write'),
 ('DELETE','/api/v1/virtual-machines/{data.vm_id}/media/{data.slot}','vm:media:write'),
 ('PUT','/api/v1/virtual-machines/{data.vm_id}/boot','vm:media:write'),
 ('GET','/api/v1/virtual-machines/{vm_id}/snapshots','vm:snapshot:read'),
 ('GET','/api/v1/virtual-machines/{vm_id}/snapshots/{snapshot_id}','vm:snapshot:read'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/snapshots','vm:snapshot:create'),
 ('DELETE','/api/v1/virtual-machines/{data.vm_id}/snapshots/{data.snapshot_id}','vm:snapshot:delete'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/snapshots/{data.snapshot_id}/restore-preview','vm:snapshot:restore'),
 ('POST','/api/v1/virtual-machines/{data.vm_id}/snapshots/{data.snapshot_id}/restore','vm:snapshot:restore')
 ) AS routes(method,path,code) LOOP
  SELECT id INTO STRICT aid FROM sys_apis WHERE path=r.path AND method=r.method
   AND module='VMService' AND business_module='VIRTUAL_MACHINE' AND scope='ADMIN'
   AND status='ON' AND deleted_at IS NULL;
  INSERT INTO sys_permissions(name,code,status,created_at) VALUES(r.code,r.code,'ON',now()) ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.code AND deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL) THEN
   RAISE EXCEPTION 'VM route has a foreign permission; review existing grants';
  END IF;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id=pid AND deleted_at IS NOT NULL) THEN
   RAISE EXCEPTION 'VM route permission was deleted; explicit recovery required';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now()) ON CONFLICT(permission_id,api_id) DO NOTHING;
 END LOOP;
 SELECT id INTO STRICT aid FROM sys_apis WHERE path='/api/v1/compute-presets' AND method='GET'
  AND module='ComputePresetService' AND business_module='SYSTEM' AND deleted_at IS NULL;
 INSERT INTO sys_permissions(name,code,status,created_at) VALUES('vm:compute-preset:read','vm:compute-preset:read','ON',now()) ON CONFLICT(code) DO NOTHING;
 SELECT id INTO STRICT pid FROM sys_permissions WHERE code='vm:compute-preset:read' AND deleted_at IS NULL;
 INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now()) ON CONFLICT(permission_id,api_id) DO NOTHING;
END $$;
COMMIT;
