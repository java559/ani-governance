-- Explicit catalog preparation AFTER admin sync-apis --dry-run and sync-apis.
-- Role grants, INSTANCE plan module and GPU quota belong to the selected lab tenant.
BEGIN;
DO $$
DECLARE r record; aid bigint; pid bigint; route_count bigint;
BEGIN
 FOR r IN SELECT * FROM (VALUES
  ('POST','/api/v1/instances','instance:create'),
  ('GET','/api/v1/instances','instance:list'),
  ('GET','/api/v1/instances/{instance_id}','instance:read'),
  ('GET','/api/v1/instances/{instance_id}/configuration','instance:read'),
  ('GET','/api/v1/instances/mountable-pvcs','instance:create'),
  ('GET','/api/v1/instances/{instance_id}/runtime','instance:read'),
  ('GET','/api/v1/instances/{instance_id}/events','instance:read'),
  ('GET','/api/v1/instances/{instance_id}/logs','instance:read'),
  ('POST','/api/v1/instances/{instance_id}/metrics/query','instance:read'),
  ('GET','/api/v1/instances/{instance_id}/metrics/latest','instance:read'),
  ('GET','/api/v1/admin/scheduler-config','instance:scheduler:read'),
  ('PUT','/api/v1/admin/scheduler-config','instance:scheduler:update'),
  ('GET','/api/v1/admin/instance-scheduling-settings','instance:scheduling:read'),
  ('PUT','/api/v1/admin/instance-scheduling-settings','instance:scheduling:defaults:update'),
  ('POST','/api/v1/instances/{data.instance_id}:start','instance:start'),
  ('POST','/api/v1/instances/{data.instance_id}:stop','instance:stop'),
  ('DELETE','/api/v1/instances/{data.instance_id}','instance:delete'),
  ('GET','/api/v1/instance-operations/{operation_id}','instance:operation:read'),
  ('GET','/api/v1/instance-presets','instance:preset:read'),
  ('POST','/api/v1/instances/{data.instance_id}/exec','instance:exec')
 ) AS routes(method,path,code) LOOP
  SELECT count(*),min(id) INTO route_count,aid FROM sys_apis
   WHERE path=r.path AND method=r.method AND module='InstanceService'
     AND business_module='INSTANCE' AND scope='ADMIN' AND status='ON' AND deleted_at IS NULL;
  IF route_count<>1 THEN RAISE EXCEPTION 'One registered active Instance route is required for % %',r.method,r.path; END IF;
  INSERT INTO sys_permissions(name,code,status,created_at) VALUES(r.code,r.code,'ON',now()) ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.code AND deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL AND NOT (r.path='/api/v1/admin/instance-scheduling-settings' AND r.method='PUT' AND permission_id IN (SELECT id FROM sys_permissions WHERE code='instance:scheduling:allowlists:update' AND deleted_at IS NULL))) THEN
   RAISE EXCEPTION 'Instance route has a foreign permission; review the existing grant';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now()) ON CONFLICT(permission_id,api_id) DO NOTHING;
 END LOOP;
 INSERT INTO sys_permissions(name,code,status,created_at)
 VALUES('instance:scheduling:allowlists:update','instance:scheduling:allowlists:update','ON',now()) ON CONFLICT(code) DO NOTHING;
 SELECT id INTO STRICT pid FROM sys_permissions WHERE code='instance:scheduling:allowlists:update' AND deleted_at IS NULL;
 SELECT id INTO STRICT aid FROM sys_apis WHERE path='/api/v1/admin/instance-scheduling-settings' AND method='PUT' AND module='InstanceService' AND deleted_at IS NULL;
 INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now()) ON CONFLICT(permission_id,api_id) DO NOTHING;
END $$;
COMMIT;
