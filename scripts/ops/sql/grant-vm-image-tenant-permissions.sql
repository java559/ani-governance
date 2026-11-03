-- Explicit data change: grant VM image capabilities to tenant managers.
--
-- Decision record: the product owner ruled on 2026-11-03 that a newly created
-- tenant's administrator gets the image upload, update and delete capabilities by
-- default, instead of requiring a manual grant per tenant. That is what granting
-- them to template:tenant:manager means.
--
-- vm:read is included on purpose. The image list and detail routes are protected
-- by vm:read, and the measured baseline shows template:tenant:manager held no vm:*
-- code at all. Granting only the three write codes would produce a tenant that can
-- upload an image but cannot read its own catalog.
--
-- Known consequence to keep in view: vm:read also covers the existing VM detail,
-- /logs, /disks and /virtual-machine-media reads. The three tenant roles that
-- already hold vm:read were granted it individually, so this change aligns future
-- tenants with the current ones rather than adding a brand-new capability.
--
-- Scope:
--   1. Grants the four codes to the tenant-manager template (tenant_id=0) so later
--      tenants inherit them.
--   2. Grants them to every existing active tenant:manager role, using each role's
--      own tenant_id. Idempotent: an existing correct grant is left alone, so the
--      roles that already hold vm:read gain only the three image codes.
-- It does not touch platform roles, other role codes, permission/API links, plans,
-- modules or plan quotas, and it creates no tenant data. VIRTUAL_MACHINE must still
-- be enabled on the tenant's plan separately.
--
-- Each code is verified against its live image route before granting, so a catalog
-- mismatch fails the whole transaction instead of granting a dead capability.
--
-- Atomic and repeatable. Run with psql -v ON_ERROR_STOP=1 -f this file, then
-- restart (or refresh) every Governance instance so the in-memory Casbin policies
-- are rebuilt. Without that restart the routes keep answering 403.
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

  FOR target IN SELECT * FROM (VALUES
    ('vm:read','GET','/api/v1/virtual-machine-images'),
    ('vm:read','GET','/api/v1/virtual-machine-images/{image_id}'),
    ('vm:image:upload','POST','/api/v1/virtual-machine-images'),
    ('vm:image:upload','POST','/api/v1/virtual-machine-images/{image_id}/upload-sessions'),
    ('vm:image:update','PATCH','/api/v1/virtual-machine-images/{image_id}'),
    ('vm:image:delete','DELETE','/api/v1/virtual-machine-images/{image_id}')
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
      RAISE EXCEPTION '% is not linked to % %; run sync-apis and the register SQL first', target.code, target.method, target.path;
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
  END LOOP;  -- image permission codes
END
$grant$;

-- Identifiers this grant depends on, for exact reversal. Reversal is NOT part of the
-- normal rollback of a feature batch: see grant-vm-image-tenant-permissions.rollback.sql
-- for the explicit revocation path.
SELECT json_build_object(
  'granted_permission_codes', (SELECT json_agg(DISTINCT code) FROM (VALUES
      ('vm:read'),('vm:image:upload'),('vm:image:update'),('vm:image:delete')) AS c(code)),
  'template_role_id', (SELECT id FROM sys_roles WHERE code='template:tenant:manager' AND type='TEMPLATE' AND deleted_at IS NULL),
  'role_permissions', (SELECT json_agg(json_build_object('id',rp.id,'role_id',rp.role_id,'tenant_id',rp.tenant_id,'permission_code',p.code))
      FROM sys_role_permissions rp
      JOIN sys_roles r ON r.id=rp.role_id
      JOIN sys_permissions p ON p.id=rp.permission_id
      WHERE r.code IN ('template:tenant:manager','tenant:manager')
        AND p.code IN ('vm:read','vm:image:upload','vm:image:update','vm:image:delete')
        AND rp.deleted_at IS NULL ORDER BY rp.id)
);
