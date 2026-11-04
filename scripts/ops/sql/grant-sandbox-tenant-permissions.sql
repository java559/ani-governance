-- Explicit data change: grant tenant sandbox permissions to tenant managers.
--
-- Symptom: /api/v1/sandbox/{id}/code-runs, /api/v1/sandbox/{id}/files and
-- /api/v1/sandbox/{id}/tokens return 403 FORBIDDEN ("unauthorized access") for
-- tenant administrators, while the permission codes, their API links and the
-- SANDBOX plan module already exist. Casbin denies because no tenant role holds
-- the grant: only one hand-made e2e tenant role was granted the newer sandbox
-- codes, and template:tenant:manager has none, so tenants inherit nothing.
--
-- Scope:
--   1. Grants all seventeen SANDBOX permission codes to the tenant-manager
--      template (template:tenant:manager, tenant_id=0) so tenants created later
--      inherit them.
--   2. Grants them to every existing active tenant:manager role, using each
--      role's own tenant_id. This repairs both tenants that hold none and
--      tenants that only hold the seven original lifecycle actions.
-- Only codes missing from a role are inserted: existing grants are never
-- updated, and a grant that exists but is inactive/denied aborts the whole
-- transaction for explicit review. It does not touch platform roles, other role
-- codes, permission/API links, plans or modules, and creates no tenant data.
-- SANDBOX must still be enabled on the tenant's plan separately.
--
-- Each code is verified against its live route before granting, so a catalog
-- mismatch fails the transaction instead of granting a dead capability.
--
-- Atomic and repeatable. Run with psql -v ON_ERROR_STOP=1 -f this file, then
-- restart (or refresh) every Governance instance so the in-memory Casbin
-- policies are rebuilt.
DO $grant$
DECLARE
  target record;
  target_permission_id bigint;
  template_role_id bigint;
  role_rec record;
BEGIN
  PERFORM pg_advisory_xact_lock(71919013);

  SELECT id INTO STRICT template_role_id FROM sys_roles
   WHERE code='template:tenant:manager' AND type='TEMPLATE'
     AND status='ON' AND deleted_at IS NULL;

  -- All sandbox codes are listed, including the seven lifecycle actions that
  -- hand-made tenant roles already hold: the template has none, so a partial
  -- list would leave tenants created later without create/list/read.
  FOR target IN SELECT * FROM (VALUES
    ('sandbox:create','POST','/api/v1/sandbox'),
    ('sandbox:list','GET','/api/v1/sandbox'),
    ('sandbox:read','GET','/api/v1/sandbox/{sandbox_id}'),
    ('sandbox:operation:read','GET','/api/v1/sandbox/operations/{operation_id}'),
    ('sandbox:pause','POST','/api/v1/sandbox/{data.sandbox_id}:pause'),
    ('sandbox:resume','POST','/api/v1/sandbox/{data.sandbox_id}:resume'),
    ('sandbox:delete','DELETE','/api/v1/sandbox/{data.sandbox_id}'),
    ('sandbox:preset:read','GET','/api/v1/compute-presets'),
    ('sandbox:code:run','POST','/api/v1/sandbox/{data.sandbox_id}/code-runs'),
    ('sandbox:code:read','GET','/api/v1/sandbox/{sandbox_id}/code-runs/{run_id}'),
    ('sandbox:files:list','GET','/api/v1/sandbox/{sandbox_id}/files'),
    ('sandbox:files:read','GET','/api/v1/sandbox/{sandbox_id}/files/content'),
    ('sandbox:files:write','POST','/api/v1/sandbox/{data.sandbox_id}/files'),
    ('sandbox:files:delete','DELETE','/api/v1/sandbox/{data.sandbox_id}/files'),
    ('sandbox:files:operation:read','GET','/api/v1/sandbox/{sandbox_id}/file-operations/{operation_id}'),
    ('sandbox:token:create','POST','/api/v1/sandbox/{data.sandbox_id}/tokens'),
    ('sandbox:token:revoke','DELETE','/api/v1/sandbox/{sandbox_id}/tokens/{token_id}')
  ) AS t(code,method,path) LOOP
    SELECT id INTO STRICT target_permission_id FROM sys_permissions
     WHERE code=target.code AND status='ON' AND deleted_at IS NULL;

    -- The permission must already be linked to the route it is being granted for.
    IF NOT EXISTS (
      SELECT 1 FROM sys_permission_apis pa
       JOIN sys_apis a ON a.id=pa.api_id
       WHERE pa.permission_id=target_permission_id AND pa.deleted_at IS NULL
         AND a.path=target.path AND a.method=target.method
         AND a.status='ON' AND a.deleted_at IS NULL
    ) THEN
      RAISE EXCEPTION '% is not linked to % %; register the API catalog first', target.code, target.method, target.path;
    END IF;

    IF EXISTS (
      SELECT 1 FROM sys_role_permissions
       WHERE tenant_id=0 AND role_id=template_role_id AND permission_id=target_permission_id
         AND (status IS DISTINCT FROM 'ON'
           OR effect IS DISTINCT FROM 'ALLOW' OR deleted_at IS NOT NULL)
    ) THEN
      RAISE EXCEPTION 'Existing template:tenant:manager grant for % is inactive or denied; review explicitly', target.code;
    END IF;
    INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at)
    SELECT 0,template_role_id,target_permission_id,'ALLOW','ON',now()
     WHERE NOT EXISTS (
       SELECT 1 FROM sys_role_permissions
        WHERE tenant_id=0 AND role_id=template_role_id AND permission_id=target_permission_id
     );

    FOR role_rec IN SELECT id, tenant_id FROM sys_roles
     WHERE code='tenant:manager' AND type='TENANT' AND status='ON' AND deleted_at IS NULL
    LOOP
      IF EXISTS (
        SELECT 1 FROM sys_role_permissions
         WHERE tenant_id=role_rec.tenant_id AND role_id=role_rec.id AND permission_id=target_permission_id
           AND (status IS DISTINCT FROM 'ON'
             OR effect IS DISTINCT FROM 'ALLOW' OR deleted_at IS NOT NULL)
      ) THEN
        RAISE EXCEPTION 'Existing tenant:manager role % (tenant %) grant for % is inactive or denied; review explicitly',
          role_rec.id, role_rec.tenant_id, target.code;
      END IF;
      INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at)
      SELECT role_rec.tenant_id,role_rec.id,target_permission_id,'ALLOW','ON',now()
       WHERE NOT EXISTS (
         SELECT 1 FROM sys_role_permissions
          WHERE tenant_id=role_rec.tenant_id AND role_id=role_rec.id AND permission_id=target_permission_id
       );
    END LOOP;  -- tenant:manager roles

    target_permission_id := NULL;
  END LOOP;  -- sandbox permission codes
END
$grant$;
