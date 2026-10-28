-- Bind the ModelDev resume-model-registration route to its management permission
-- AFTER `admin sync-apis --dry-run` and `admin sync-apis` have registered the
-- exact route in sys_apis.
--
-- The route /api/v1/modeldev/executions/{execution_id}:resume-model-registration
-- is authorized by ModelDevResumeRegistrationAction, which requires the separate
-- modeldev:manage_execution permission AND a sys_permission_apis link from that
-- permission to this exact (method, path) row. `admin permissions-align` cannot
-- perform this binding: the route is outside the permcode authority tables and
-- would be reported as a missing entry, which rejects the whole batch.
--
-- This file deliberately does not grant a role, enable a plan, create a tenant,
-- or change quotas. Use the existing tenant role management entry to grant
-- modeldev:manage_execution to the selected role.
BEGIN;

DO $$
DECLARE
  r record;
  aid bigint;
  pid bigint;
  route_count bigint;
BEGIN
  FOR r IN SELECT * FROM (VALUES
    ('POST', '/api/v1/modeldev/executions/{execution_id}:resume-model-registration', 'modeldev:manage_execution')
  ) AS routes(method, path, code)
  LOOP
    SELECT count(*), min(id) INTO route_count, aid
      FROM sys_apis
     WHERE path = r.path
       AND method = r.method
       AND module = 'ModelDevService'
       AND business_module = 'MODEL'
       AND scope = 'ADMIN'
       AND status = 'ON'
       AND deleted_at IS NULL;

    IF route_count <> 1 THEN
      RAISE EXCEPTION 'One registered active ModelDev route is required for % % (found %)', r.method, r.path, route_count;
    END IF;

    INSERT INTO sys_permissions(name, code, status, created_at)
    VALUES (r.code, r.code, 'ON', now())
    ON CONFLICT (code) DO NOTHING;

    SELECT id INTO STRICT pid
      FROM sys_permissions
     WHERE code = r.code
       AND status = 'ON'
       AND deleted_at IS NULL;

    -- Never steal a route that a different active permission already owns.
    IF EXISTS (
      SELECT 1 FROM sys_permission_apis
       WHERE api_id = aid
         AND permission_id <> pid
         AND deleted_at IS NULL
    ) THEN
      RAISE EXCEPTION 'ModelDev route % % already belongs to another permission; review the existing grant', r.method, r.path;
    END IF;

    INSERT INTO sys_permission_apis(permission_id, api_id, created_at)
    VALUES (pid, aid, now())
    ON CONFLICT (permission_id, api_id) DO NOTHING;
  END LOOP;
END $$;

COMMIT;
