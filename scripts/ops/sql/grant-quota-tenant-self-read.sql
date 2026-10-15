-- Explicit data change for tenant self-service quota reads.
--
-- Missing role grant, not a missing route: GET /api/v1/me/quota-accounts maps to
-- quota:account:self-read, which is registered and linked to its route, but was
-- never granted to any role. Tenant users therefore get 403 on their own quota
-- accounts. This is a tenant-self read (tenant comes from the Principal), so it
-- belongs on tenant-scoped roles, never on the platform administrator.
--
-- Scope:
--   1. Grants the permission to the tenant-manager template
--      (template:tenant:manager, tenant_id=0) so tenants created later inherit it.
--   2. Grants it to every existing active tenant:manager role, using each role's
--      own tenant_id.
-- It does not touch platform roles, other role codes, permission/API links, plans
-- or modules, and it creates no tenant data. It does not, by itself, put TENANT
-- in a plan whitelist: the tenant's plan must still allow the TENANT module.
--
-- Atomic and repeatable. Run with psql -v ON_ERROR_STOP=1 -f this file, then
-- restart (or refresh) every Governance instance so the in-memory Casbin
-- policies are rebuilt.
DO $grant$
DECLARE
  target_permission_id bigint;
  template_role_id bigint;
  role_rec record;
BEGIN
  PERFORM pg_advisory_xact_lock(71919011);

  SELECT id INTO STRICT target_permission_id FROM sys_permissions
   WHERE code='quota:account:self-read' AND status='ON' AND deleted_at IS NULL;

  -- The permission must already be linked to the self-service route.
  IF NOT EXISTS (
    SELECT 1 FROM sys_permission_apis pa
     JOIN sys_apis a ON a.id=pa.api_id
     WHERE pa.permission_id=target_permission_id AND pa.deleted_at IS NULL
       AND a.path='/api/v1/me/quota-accounts' AND a.method='GET'
       AND a.status='ON' AND a.deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'quota:account:self-read is not linked to GET /api/v1/me/quota-accounts; register the API catalog first';
  END IF;

  SELECT id INTO STRICT template_role_id FROM sys_roles
   WHERE code='template:tenant:manager' AND type='TEMPLATE'
     AND status='ON' AND deleted_at IS NULL;
  IF EXISTS (
    SELECT 1 FROM sys_role_permissions
     WHERE tenant_id=0 AND role_id=template_role_id AND permission_id=target_permission_id
       AND (status IS DISTINCT FROM 'ON'
         OR effect IS DISTINCT FROM 'ALLOW' OR deleted_at IS NOT NULL)
  ) THEN
    RAISE EXCEPTION 'Existing template:tenant:manager grant for quota:account:self-read is inactive or denied; review explicitly';
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
      RAISE EXCEPTION 'Existing tenant:manager role % (tenant %) grant is inactive or denied; review explicitly',
        role_rec.id, role_rec.tenant_id;
    END IF;
    INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at)
    SELECT role_rec.tenant_id,role_rec.id,target_permission_id,'ALLOW','ON',now()
     WHERE NOT EXISTS (
       SELECT 1 FROM sys_role_permissions
        WHERE tenant_id=role_rec.tenant_id AND role_id=role_rec.id AND permission_id=target_permission_id
     );
  END LOOP;
END
$grant$;
