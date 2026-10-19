-- Register the Model BFF permissions after the API catalog has
-- been reviewed and synchronised with `admin sync-apis`.
--
-- This file deliberately does not grant a role, enable a plan, create a
-- tenant, or change quotas. Use enable-model-access.sql for one
-- explicitly selected tenant role and plan.
BEGIN;

DO $$
DECLARE
  route record;
  v_api_id bigint;
  v_permission_id bigint;
BEGIN
  FOR route IN
    SELECT * FROM (VALUES
      ('GET',  '/api/v1/models',                                      'model:service:read',   'MODEL'),
      ('GET',  '/api/v1/models/{model_id}',                           'model:service:read',   'MODEL'),
      ('GET',  '/api/v1/models:search',                               'model:service:read',   'MODEL'),
      ('GET',  '/api/v1/models/{model_id}/versions',                  'model:service:read',   'MODEL'),
      ('GET',  '/api/v1/model-versions/{model_version_id}',           'model:service:read',   'MODEL'),
      ('GET',  '/api/v1/model-versions/{model_version_id}/download-url','model:service:read',   'MODEL'),
      ('GET',  '/api/v1/models/import-tasks/{task_id}',               'model:service:read',   'MODEL'),
      ('POST', '/api/v1/models',                                      'model:service:manage', 'MODEL'),
      ('POST', '/api/v1/model-versions',                              'model:service:manage', 'MODEL'),
      ('POST', '/api/v1/models/{model_id}/upload-url',                'model:service:manage', 'MODEL'),
      ('POST', '/api/v1/models:import',                               'model:service:manage', 'MODEL'),
      ('POST', '/api/v1/models/import-tasks/{task_id}:retry',         'model:service:manage', 'MODEL'),
      ('DELETE','/api/v1/models/{model_id}',                          'model:service:manage', 'MODEL'),
      ('DELETE','/api/v1/model-versions/{model_version_id}',           'model:service:manage', 'MODEL')
    ) AS routes(method, path, permission_code, business_module)
  LOOP
    SELECT a.id INTO STRICT v_api_id
      FROM sys_apis a
     WHERE a.method = route.method
       AND a.path = route.path
       AND a.module = 'ModelService'
       AND a.business_module = route.business_module
       AND a.scope = 'ADMIN'
       AND a.status = 'ON'
       AND a.deleted_at IS NULL;

    INSERT INTO sys_permissions(name, code, status, created_at)
    VALUES (route.permission_code, route.permission_code, 'ON', now())
    ON CONFLICT (code) DO NOTHING;

    SELECT p.id INTO STRICT v_permission_id
      FROM sys_permissions p
     WHERE p.code = route.permission_code
       AND p.status = 'ON'
       AND p.deleted_at IS NULL;

    INSERT INTO sys_permission_apis(permission_id, api_id, created_at)
    VALUES (v_permission_id, v_api_id, now())
    ON CONFLICT (permission_id, api_id) DO NOTHING;
  END LOOP;
END $$;

COMMIT;
