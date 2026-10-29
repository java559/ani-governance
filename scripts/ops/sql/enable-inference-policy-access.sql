-- Explicit selected tenant-manager role ONLY; requires the existing plan's
-- INFERENCE entitlement and never enables a shared plan or another role.
-- psql "$DSN" -v ON_ERROR_STOP=1 -v tenant_id=... -v role_id=...
--   -v plan_id=... -f scripts/ops/sql/enable-inference-policy-access.sql
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
  ('inference:policy:create','POST','/api/v1/inference-policies'),
  ('inference:policy:read','GET','/api/v1/inference-policies'),
  ('inference:policy:read','GET','/api/v1/inference-policies/{policy_id}'),
  ('inference:policy:update','PUT','/api/v1/inference-policies/{policy_id}'),
  ('inference:policy:delete','DELETE','/api/v1/inference-policies/{policy_id}'),
  ('inference:key:bind-policy','POST','/api/v1/inference-keys/{key_id}:bind-policy')
 ) AS permissions(code,method,path) LOOP
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.code AND status='ON' AND deleted_at IS NULL;
  IF NOT EXISTS(SELECT 1 FROM sys_permission_apis pa JOIN sys_apis a ON a.id=pa.api_id
    WHERE pa.permission_id=pid AND pa.deleted_at IS NULL AND a.module='InferenceKeyService' AND a.business_module='INFERENCE'
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
 IF NOT EXISTS(SELECT 1 FROM sys_plan_modules WHERE plan_id=chosen_plan AND module='INFERENCE' AND deleted_at IS NULL) THEN
  RAISE EXCEPTION 'Selected plan must already enable INFERENCE';
 END IF;
END $$;
COMMIT;
