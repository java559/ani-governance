-- Explicit data change: grant the 22 platform image management capabilities to
-- the platform administrator.
--
-- Scope:
--   1. Grants all 22 codes to sys_roles.code='platform:admin' (type SYSTEM) at
--      tenant_id=0. The role must already exist and be active; this file never
--      creates roles.
--   2. If platform:readonly or platform:ops exists, grants only the 8 read codes
--      to each. Missing roles are skipped silently on purpose: the file must not
--      widen the deployment's role model.
--
-- Each code is verified against its live route link before granting, so a catalog
-- mismatch fails the whole transaction instead of granting a dead capability.
-- Idempotent: an existing correct grant is left alone; an existing grant that is
-- denied, deleted or inactive fails the transaction for explicit review.
--
-- Atomic and repeatable. Run with psql -v ON_ERROR_STOP=1 -f this file, then
-- restart every Governance instance so the in-memory Casbin policies are rebuilt;
-- logged-in operators must re-login to obtain the new JWT permission codes.
DO $grant$
DECLARE
  target record;
  target_permission_id bigint;
  admin_role_id bigint;
  secondary record;
  secondary_role_id bigint;
BEGIN
  PERFORM pg_advisory_xact_lock(71919014);

  SELECT id INTO STRICT admin_role_id FROM sys_roles
   WHERE code='platform:admin' AND type='SYSTEM'
     AND status='ON' AND deleted_at IS NULL;

  FOR target IN SELECT * FROM (VALUES
    ('image:platform:space:get','GET','/api/v1/platform/images/space'),
    ('image:platform:space:enable','POST','/api/v1/platform/images/space:enable'),
    ('image:platform:credential:get','GET','/api/v1/platform/images/publisher-credential'),
    ('image:platform:credential:issue','POST','/api/v1/platform/images/publisher-credential:issue'),
    ('image:platform:credential:reset','POST','/api/v1/platform/images/publisher-credential:reset'),
    ('image:platform:credential:disable','POST','/api/v1/platform/images/publisher-credential:disable'),
    ('image:platform:registration:list','GET','/api/v1/platform/images/registrations'),
    ('image:platform:registration:get','GET','/api/v1/platform/images/registrations/{image_id}'),
    ('image:platform:registration:create','POST','/api/v1/platform/images/registrations'),
    ('image:platform:registration:update','PATCH','/api/v1/platform/images/registrations/{image_id}'),
    ('image:platform:registration:unregister','POST','/api/v1/platform/images/registrations/{image_id}:unregister'),
    ('image:platform:tenant-space:get','GET','/api/v1/platform/tenants/{target_tenant_id}/images/space'),
    ('image:platform:tenant-space:enable','POST','/api/v1/platform/tenants/{target_tenant_id}/images/space:enable'),
    ('image:platform:tenant-credential:get','GET','/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential'),
    ('image:platform:tenant-credential:issue','POST','/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:issue'),
    ('image:platform:tenant-credential:reset','POST','/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:reset'),
    ('image:platform:tenant-credential:disable','POST','/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:disable'),
    ('image:platform:tenant-registration:list','GET','/api/v1/platform/tenants/{target_tenant_id}/images/registrations'),
    ('image:platform:tenant-registration:get','GET','/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}'),
    ('image:platform:tenant-registration:create','POST','/api/v1/platform/tenants/{target_tenant_id}/images/registrations'),
    ('image:platform:tenant-registration:update','PATCH','/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}'),
    ('image:platform:tenant-registration:unregister','POST','/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}:unregister')
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
       WHERE tenant_id=0 AND role_id=admin_role_id AND permission_id=target_permission_id
         AND (status IS DISTINCT FROM 'ON'
           OR effect IS DISTINCT FROM 'ALLOW' OR deleted_at IS NOT NULL)
    ) THEN
      RAISE EXCEPTION 'Existing platform:admin grant for % is inactive or denied; review explicitly', target.code;
    END IF;
    INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at)
    SELECT 0,admin_role_id,target_permission_id,'ALLOW','ON',now()
     WHERE NOT EXISTS (
       SELECT 1 FROM sys_role_permissions
        WHERE tenant_id=0 AND role_id=admin_role_id AND permission_id=target_permission_id
     );

    target_permission_id := NULL;
  END LOOP;  -- all 22 codes for platform:admin

  -- Read-only platform roles, when present, get only the 8 read codes.
  FOR secondary IN SELECT * FROM (VALUES
    ('platform:readonly'),
    ('platform:ops')
  ) AS r(code) LOOP
    IF NOT EXISTS (
      SELECT 1 FROM sys_roles
       WHERE code=secondary.code AND type='SYSTEM'
         AND status='ON' AND deleted_at IS NULL
    ) THEN
      CONTINUE;
    END IF;
    SELECT id INTO STRICT secondary_role_id FROM sys_roles
     WHERE code=secondary.code AND type='SYSTEM'
       AND status='ON' AND deleted_at IS NULL;

    FOR target IN SELECT * FROM (VALUES
      ('image:platform:space:get','GET','/api/v1/platform/images/space'),
      ('image:platform:credential:get','GET','/api/v1/platform/images/publisher-credential'),
      ('image:platform:registration:list','GET','/api/v1/platform/images/registrations'),
      ('image:platform:registration:get','GET','/api/v1/platform/images/registrations/{image_id}'),
      ('image:platform:tenant-space:get','GET','/api/v1/platform/tenants/{target_tenant_id}/images/space'),
      ('image:platform:tenant-credential:get','GET','/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential'),
      ('image:platform:tenant-registration:list','GET','/api/v1/platform/tenants/{target_tenant_id}/images/registrations'),
      ('image:platform:tenant-registration:get','GET','/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}')
    ) AS t(code,method,path) LOOP
      SELECT id INTO STRICT target_permission_id FROM sys_permissions
       WHERE code=target.code AND status='ON' AND deleted_at IS NULL;

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
         WHERE tenant_id=0 AND role_id=secondary_role_id AND permission_id=target_permission_id
           AND (status IS DISTINCT FROM 'ON'
             OR effect IS DISTINCT FROM 'ALLOW' OR deleted_at IS NOT NULL)
      ) THEN
        RAISE EXCEPTION 'Existing % grant for % is inactive or denied; review explicitly', secondary.code, target.code;
      END IF;
      INSERT INTO sys_role_permissions(tenant_id,role_id,permission_id,effect,status,created_at)
      SELECT 0,secondary_role_id,target_permission_id,'ALLOW','ON',now()
       WHERE NOT EXISTS (
         SELECT 1 FROM sys_role_permissions
          WHERE tenant_id=0 AND role_id=secondary_role_id AND permission_id=target_permission_id
       );

      target_permission_id := NULL;
    END LOOP;  -- 8 read codes for the secondary role
  END LOOP;  -- secondary roles
END
$grant$;

-- Identifiers this grant depends on, for exact reversal. Reversal is NOT part of the
-- normal rollback of a feature batch: see grant-platform-image-permissions.rollback.sql.
SELECT json_build_object(
  'admin_role_id', (SELECT id FROM sys_roles WHERE code='platform:admin' AND type='SYSTEM' AND deleted_at IS NULL),
  'role_permissions', (SELECT json_agg(json_build_object('id',rp.id,'role_id',rp.role_id,'tenant_id',rp.tenant_id,'permission_code',p.code) ORDER BY rp.id)
      FROM sys_role_permissions rp
      JOIN sys_roles r ON r.id=rp.role_id
      JOIN sys_permissions p ON p.id=rp.permission_id
      WHERE r.code IN ('platform:admin','platform:readonly','platform:ops')
        AND p.code LIKE 'image:platform:%'
        AND rp.deleted_at IS NULL)
);
