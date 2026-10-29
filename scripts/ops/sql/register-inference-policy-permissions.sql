-- GOV-ACC-INF-V12-WIRING-01. Run explicitly AFTER admin sync-apis --dry-run
-- and admin sync-apis against the approved schema. Catalog registration only:
-- no role grant, subscription module, tenant quota or listener is enabled.
BEGIN;
DO $$
DECLARE r record; aid bigint; pid bigint; route_count bigint;
BEGIN
 FOR r IN SELECT * FROM (VALUES
  ('POST','/api/v1/inference-policies','inference:policy:create'),
  ('GET','/api/v1/inference-policies','inference:policy:read'),
  ('GET','/api/v1/inference-policies/{policy_id}','inference:policy:read'),
  ('PUT','/api/v1/inference-policies/{policy_id}','inference:policy:update'),
  ('DELETE','/api/v1/inference-policies/{policy_id}','inference:policy:delete')
 ) AS routes(method,path,code) LOOP
  SELECT count(*),min(id) INTO route_count,aid FROM sys_apis
   WHERE path=r.path AND method=r.method AND module='InferenceKeyService'
     AND business_module='INFERENCE' AND scope='ADMIN' AND status='ON' AND deleted_at IS NULL;
  IF route_count<>1 THEN RAISE EXCEPTION 'One registered active Inference route is required for % %',r.method,r.path; END IF;
  INSERT INTO sys_permissions(name,code,status,created_at) VALUES(r.code,r.code,'ON',now()) ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.code AND deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM sys_permission_apis WHERE api_id=aid AND permission_id<>pid AND deleted_at IS NULL) THEN
   RAISE EXCEPTION 'Inference route has a foreign permission; review the existing grant';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now()) ON CONFLICT(permission_id,api_id) DO NOTHING;
 END LOOP;
END $$;
COMMIT;
