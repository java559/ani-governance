-- Run explicitly after admin sync-apis --dry-run and approved sync-apis.
-- Registers capabilities only; does not grant roles or enable tenant plans.
BEGIN;
DO $$
DECLARE r record; aid bigint; pid bigint; route_count bigint;
BEGIN
 FOR r IN SELECT * FROM (VALUES
  ('POST','/api/v1/inference-keys','inference:key:create'),
  ('GET','/api/v1/inference-keys','inference:key:read'),
  ('GET','/api/v1/inference-keys/{key_id}','inference:key:read'),
  ('DELETE','/api/v1/inference-keys/{key_id}','inference:key:revoke'),
  ('POST','/api/v1/inference-keys/{key_id}:rotate','inference:key:rotate'),
  ('POST','/api/v1/inference-keys/{key_id}:bind-policy','inference:key:bind-policy')
 ) AS routes(method,path,code) LOOP
  SELECT count(*),min(id) INTO route_count,aid FROM sys_apis
   WHERE path=r.path AND method=r.method AND module='InferenceKeyService'
     AND business_module='INFERENCE' AND scope='ADMIN' AND status='ON' AND deleted_at IS NULL;
  IF route_count<>1 THEN RAISE EXCEPTION 'One registered active inference key route is required for % %',r.method,r.path; END IF;
  INSERT INTO sys_permissions(name,code,status,created_at) VALUES(r.code,r.code,'ON',now()) ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.code AND deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL) THEN
   RAISE EXCEPTION 'Inference key route has a foreign permission; review the existing grant';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now()) ON CONFLICT(permission_id,api_id) DO NOTHING;
 END LOOP;
END $$;
COMMIT;
