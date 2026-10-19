-- Grant the Model BFF to one explicitly selected tenant role and
-- plan. Example:
--   psql "$DSN" -v ON_ERROR_STOP=1 \
--     -v tenant_id=6 -v role_id=8 -v plan_id=1 \
--     -f scripts/ops/sql/enable-model-access.sql
--
-- Run register-model-permissions.sql first. This file does not
-- modify other tenants, role templates, quotas, identities or API keys.
BEGIN;
SELECT set_config('ani.model_tenant', :'tenant_id', true);
SELECT set_config('ani.model_role', :'role_id', true);
SELECT set_config('ani.model_plan', :'plan_id', true);

DO $$
DECLARE
  tenant_id_value bigint := current_setting('ani.model_tenant')::bigint;
  role_id_value bigint := current_setting('ani.model_role')::bigint;
  plan_id_value bigint := current_setting('ani.model_plan')::bigint;
  permission_code text;
  v_permission_id bigint;
BEGIN
  IF tenant_id_value <= 0 OR role_id_value <= 0 OR plan_id_value <= 0 THEN
    RAISE EXCEPTION 'Explicit tenant_id, role_id and plan_id are required';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM sys_tenants
     WHERE id = tenant_id_value AND plan_id = plan_id_value
       AND status = 'ON' AND deleted_at IS NULL
       AND (expired_at IS NULL OR expired_at > now())
  ) THEN
    RAISE EXCEPTION 'Selected plan must belong to the active selected tenant';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM sys_roles
     WHERE id = role_id_value AND tenant_id = tenant_id_value
       AND code = 'tenant:manager' AND type = 'TENANT'
       AND status = 'ON' AND data_scope = 'ALL' AND deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'Selected current tenant:manager role with ALL scope is required';
  END IF;

  FOREACH permission_code IN ARRAY ARRAY[
    'model:service:read', 'model:service:manage'
  ] LOOP
    SELECT id INTO STRICT v_permission_id
      FROM sys_permissions
     WHERE code = permission_code AND status = 'ON' AND deleted_at IS NULL;
    IF EXISTS (
      SELECT 1 FROM sys_role_permissions
       WHERE tenant_id = tenant_id_value AND role_id = role_id_value
         AND permission_id = v_permission_id
         AND (status IS DISTINCT FROM 'ON'
           OR effect IS DISTINCT FROM 'ALLOW' OR deleted_at IS NOT NULL)
    ) THEN
      RAISE EXCEPTION 'Selected role has an inactive or denied grant for %; review explicitly', permission_code;
    END IF;
    INSERT INTO sys_role_permissions(tenant_id, role_id, permission_id, effect, status, created_at)
    SELECT tenant_id_value, role_id_value, v_permission_id, 'ALLOW', 'ON', now()
     WHERE NOT EXISTS (
       SELECT 1 FROM sys_role_permissions
        WHERE tenant_id = tenant_id_value AND role_id = role_id_value
          AND permission_id = v_permission_id
     );
  END LOOP;

  INSERT INTO sys_plan_modules(plan_id, module, created_at)
  SELECT plan_id_value, module_name, now()
    FROM unnest(ARRAY['MODEL'::text]) AS modules(module_name)
   WHERE NOT EXISTS (
     SELECT 1 FROM sys_plan_modules
      WHERE plan_id = plan_id_value AND module = module_name AND deleted_at IS NULL
   );
END $$;
COMMIT;
