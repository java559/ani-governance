-- Attach the execution-detail read routes to the permissions that already
-- govern the same plane. `admin sync-apis` only writes the API catalog; it
-- never links a permission, so a new route stays unauthorized until this runs.
--
-- No new permission code is introduced: pods and events are fields of the
-- execution detail (modeldev:execution:get) and the stream is the live form of
-- the training log (modeldev:execution:logs). Roles and plans are not touched.
--
-- The artifact byte relay is the same grant as the URL exchange: whoever may
-- read an artifact's download URL may also stream its bytes, so the relay
-- reuses modeldev:artifact:download instead of widening access.
BEGIN;

DO $$
DECLARE
  route record;
  v_api_id bigint;
  v_permission_id bigint;
BEGIN
  FOR route IN
    SELECT * FROM (VALUES
      ('GET', '/api/v1/modeldev/executions/{execution_id}/pods',        'modeldev:execution:get'),
      ('GET', '/api/v1/modeldev/executions/{execution_id}/events',      'modeldev:execution:get'),
      ('GET', '/api/v1/modeldev/executions/{execution_id}/resource-metrics', 'modeldev:execution:get'),
      ('GET', '/api/v1/modeldev/executions/{execution_id}/logs/stream', 'modeldev:execution:logs'),
      ('GET', '/api/v1/modeldev/artifacts/{artifact_id}/download',      'modeldev:artifact:download')
    ) AS routes(method, path, permission_code)
  LOOP
    SELECT a.id INTO STRICT v_api_id
      FROM sys_apis a
     WHERE a.method = route.method
       AND a.path = route.path
       AND a.status = 'ON'
       AND a.deleted_at IS NULL;

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
