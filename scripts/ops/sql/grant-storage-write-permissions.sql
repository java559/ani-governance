-- Explicit tenant IDs required; default preview. Reload Gov policies after applying.
\set ON_ERROR_STOP on
\if :{?tenant_ids}
\else
\echo 'Required: -v tenant_ids=1,2'
\quit 1
\endif
\if :{?apply}
\else
\set apply false
\endif
BEGIN;
SELECT set_config('ani.storage_write_tenants', :'tenant_ids', true);
SELECT set_config('ani.storage_write_apply', :'apply', true);
DO $$
BEGIN
  IF current_setting('ani.storage_write_tenants') !~ '^[1-9][0-9]*(,[1-9][0-9]*)*$' THEN
    RAISE EXCEPTION 'tenant_ids must contain canonical positive integers';
  END IF;
END $$;
CREATE TEMP TABLE storage_write_targets ON COMMIT DROP AS
SELECT unnest(string_to_array(current_setting('ani.storage_write_tenants'), ',')::bigint[]) tenant_id;
DO $$
DECLARE target record; grant_role record; selected_permission bigint; selected_api bigint;
BEGIN
  PERFORM pg_advisory_xact_lock(71919012);
  IF EXISTS(SELECT 1 FROM storage_write_targets t WHERE NOT EXISTS(
    SELECT 1 FROM sys_roles r JOIN sys_tenants tenant ON tenant.id=r.tenant_id
    WHERE r.tenant_id=t.tenant_id AND r.code='tenant:manager' AND r.status='ON' AND r.deleted_at IS NULL
    AND tenant.status='ON' AND tenant.deleted_at IS NULL)) THEN
    RAISE EXCEPTION 'Every selected tenant must be active with an active tenant:manager role';
  END IF;
  FOR target IN SELECT * FROM (VALUES
    ('storage:bucket:create','/api/v1/storage/buckets'),
    ('storage:object:upload','/api/v1/storage/buckets/{data.bucket_id}/objects:upload-url'),
    ('storage:object:delete','/api/v1/storage/buckets/{data.bucket_id}/objects:delete')
  ) AS t(code,path) LOOP
    SELECT p.id INTO STRICT selected_permission FROM sys_permissions p
      WHERE p.code=target.code AND p.status='ON' AND p.deleted_at IS NULL;
    SELECT a.id INTO STRICT selected_api FROM sys_apis a JOIN sys_permission_apis pa ON pa.api_id=a.id
      WHERE a.path=target.path AND a.method='POST' AND a.business_module='STORAGE' AND a.status='ON'
      AND a.deleted_at IS NULL AND pa.permission_id=selected_permission AND pa.deleted_at IS NULL;
    FOR grant_role IN SELECT r.id,r.tenant_id FROM sys_roles r WHERE r.tenant_id IN (SELECT tenant_id FROM storage_write_targets)
      AND r.code='tenant:manager' AND r.status='ON' AND r.deleted_at IS NULL LOOP
      IF EXISTS(SELECT 1 FROM sys_role_permissions rp WHERE rp.role_id=grant_role.id AND rp.permission_id=selected_permission
        AND (rp.tenant_id IS DISTINCT FROM grant_role.tenant_id OR rp.status IS DISTINCT FROM 'ON'
        OR rp.effect IS DISTINCT FROM 'ALLOW' OR rp.deleted_at IS NOT NULL)) THEN
        RAISE EXCEPTION 'Conflicting existing grant for role %, permission %',grant_role.id,target.code;
      END IF;
      IF current_setting('ani.storage_write_apply')::boolean THEN
        INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at)
        SELECT grant_role.tenant_id,grant_role.id,selected_permission,'ALLOW','ON',now()
        WHERE NOT EXISTS(SELECT 1 FROM sys_role_permissions rp WHERE rp.role_id=grant_role.id AND rp.permission_id=selected_permission);
      END IF;
      RAISE NOTICE 'Storage write grant: tenant %, role %, permission %',grant_role.tenant_id,grant_role.id,target.code;
    END LOOP;
  END LOOP;
END $$;
\if :apply
COMMIT;
\else
ROLLBACK;
\echo 'Preview only; use -v apply=true to persist reviewed grants.'
\endif
