-- Explicit data change for installations where the platform administrator
-- cannot read the quota catalog or tenant quota accounts.
--
-- Missing role grant, not a missing route: the routes
--   GET /api/v1/admin/quota-definitions          -> quota-definition:view
--   GET /api/v1/admin/tenants/{id}/quota-accounts -> tenant:quota-read
-- are registered and their permission/API links exist, but neither permission
-- was ever granted to a role, and sys:platform_admin does not cover these two
-- routes. Without this grant casbin denies the platform administrator (403).
--
-- Scope: grants exactly these two permissions to the protected platform
-- administrator role (code platform:admin, tenant_id=0). It does not create or
-- widen any other role, does not change permission/API links, does not enable a
-- plan or module, and does not touch tenant data.
--
-- Atomic and repeatable. Run with psql -v ON_ERROR_STOP=1 -f this file, then
-- restart (or refresh) every Governance instance so the in-memory Casbin
-- policies are rebuilt; a CLI/SQL change does not update other replicas.
DO $grant$
DECLARE
  platform_role_id bigint;
  target_permission_id bigint;
  r record;
BEGIN
  PERFORM pg_advisory_xact_lock(71919010);

  -- Missing or ambiguous prerequisites must fail instead of silently no-op.
  SELECT id INTO STRICT platform_role_id FROM sys_roles
   WHERE code='platform:admin' AND tenant_id=0 AND type='SYSTEM'
     AND status='ON' AND deleted_at IS NULL;

  FOR r IN SELECT * FROM (VALUES
    ('quota-definition:view','/api/v1/admin/quota-definitions'),
    ('tenant:quota-read','/api/v1/admin/tenants/{id}/quota-accounts')
  ) AS targets(permission_code,route_path) LOOP
    SELECT id INTO STRICT target_permission_id FROM sys_permissions
     WHERE code=r.permission_code AND status='ON' AND deleted_at IS NULL;

    -- The permission must already be linked to its route; otherwise granting it
    -- would be meaningless (the authorizer builds policies from that link).
    IF NOT EXISTS (
      SELECT 1 FROM sys_permission_apis pa
       JOIN sys_apis a ON a.id=pa.api_id
       WHERE pa.permission_id=target_permission_id AND pa.deleted_at IS NULL
         AND a.path=r.route_path AND a.method='GET'
         AND a.status='ON' AND a.deleted_at IS NULL
    ) THEN
      RAISE EXCEPTION 'Permission % is not linked to GET %; register the API catalog first',
        r.permission_code, r.route_path;
    END IF;

    -- Refuse to overwrite an existing inactive or denied grant.
    IF EXISTS (
      SELECT 1 FROM sys_role_permissions
       WHERE tenant_id=0 AND role_id=platform_role_id
         AND permission_id=target_permission_id
         AND (status IS DISTINCT FROM 'ON'
           OR effect IS DISTINCT FROM 'ALLOW'
           OR deleted_at IS NOT NULL)
    ) THEN
      RAISE EXCEPTION 'Existing platform:admin grant for % is inactive or denied; review explicitly',
        r.permission_code;
    END IF;

    INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at)
    SELECT 0,platform_role_id,target_permission_id,'ALLOW','ON',now()
     WHERE NOT EXISTS (
       SELECT 1 FROM sys_role_permissions
        WHERE tenant_id=0 AND role_id=platform_role_id AND permission_id=target_permission_id
     );
  END LOOP;
END
$grant$;
