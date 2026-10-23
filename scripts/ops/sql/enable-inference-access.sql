-- Explicit selected tenant-manager role and selected subscription plan ONLY.
-- Example: psql "$DSN" -v ON_ERROR_STOP=1 -v tenant_id=... -v role_id=...
--   -v plan_id=... -f scripts/ops/sql/enable-inference-access.sql
-- A plan entitlement applies to every tenant using that selected plan; choose
-- a dedicated plan when that wider entitlement is not intended. This does not
-- edit FREE, role templates, other roles, API keys, quota limits or identities.
BEGIN;
SELECT set_config('ani.inference_tenant', :'tenant_id', true);
SELECT set_config('ani.inference_role', :'role_id', true);
SELECT set_config('ani.inference_plan', :'plan_id', true);
DO $$
DECLARE tid bigint:=current_setting('ani.inference_tenant')::bigint;
 rid bigint:=current_setting('ani.inference_role')::bigint;
 chosen_plan bigint:=current_setting('ani.inference_plan')::bigint;
 r record; pid bigint;
BEGIN
 IF tid<=0 OR rid<=0 OR chosen_plan<=0 THEN RAISE EXCEPTION 'Explicit tenant, role and plan IDs are required'; END IF;
 IF NOT EXISTS(SELECT 1 FROM sys_tenants WHERE id=tid AND plan_id=chosen_plan AND status='ON' AND deleted_at IS NULL AND (expired_at IS NULL OR expired_at>now())) THEN
  RAISE EXCEPTION 'Selected plan must belong to the active selected tenant';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM sys_roles WHERE id=rid AND tenant_id=tid AND code='tenant:manager' AND type='TENANT' AND status='ON' AND data_scope='ALL' AND deleted_at IS NULL) THEN
  RAISE EXCEPTION 'Selected current tenant:manager role with ALL scope is required';
 END IF;
 FOR r IN SELECT * FROM (VALUES
  ('inference:service:read','GET','/api/v1/inference/services'),
  ('inference:service:create','POST','/api/v1/inference/services'),
  ('inference:service:delete','POST','/api/v1/inference/services/{resource_id}:delete'),
  ('inference:service:update','POST','/api/v1/inference/services/{resource_id}:update'),
  ('inference:service:start','POST','/api/v1/inference/services/{resource_id}:start'),
  ('inference:service:stop','POST','/api/v1/inference/services/{resource_id}:stop'),
  ('inference:service:restart','POST','/api/v1/inference/services/{resource_id}:restart')
 ) AS permissions(code,method,path) LOOP
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.code AND status='ON' AND deleted_at IS NULL;
  IF NOT EXISTS(SELECT 1 FROM sys_permission_apis pa JOIN sys_apis a ON a.id=pa.api_id
    WHERE pa.permission_id=pid AND pa.deleted_at IS NULL AND a.module='InferenceService' AND a.business_module='INFERENCE'
      AND a.path=r.path AND a.method=r.method AND a.scope='ADMIN' AND a.status='ON' AND a.deleted_at IS NULL) THEN
   RAISE EXCEPTION 'Register the exact Inference permission/API relationship first';
  END IF;
  IF EXISTS(SELECT 1 FROM sys_role_permissions WHERE tenant_id=tid AND role_id=rid AND permission_id=pid
    AND (status IS DISTINCT FROM 'ON' OR effect IS DISTINCT FROM 'ALLOW' OR deleted_at IS NOT NULL)) THEN
   RAISE EXCEPTION 'Existing selected role grant is inactive or denied; explicit review is required';
  END IF;
  INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at)
   SELECT tid,rid,pid,'ALLOW','ON',now() WHERE NOT EXISTS(SELECT 1 FROM sys_role_permissions WHERE tenant_id=tid AND role_id=rid AND permission_id=pid);
 END LOOP;
 INSERT INTO sys_plan_modules(plan_id,module,created_at)
  SELECT chosen_plan,'INFERENCE',now() WHERE NOT EXISTS(SELECT 1 FROM sys_plan_modules WHERE plan_id=chosen_plan AND module='INFERENCE' AND deleted_at IS NULL);
END $$;
COMMIT;
