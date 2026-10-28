-- Explicit data change: grant tenant storage permissions to tenant managers.
--
-- Symptom: every /api/v1/storage/** route returned 403 FORBIDDEN
-- ("unauthorized access") for tenant administrators, while the permission codes,
-- their API links and the STORAGE plan module already existed. Casbin denies
-- because no tenant role holds the grant: only one historical tenant role was
-- granted the storage codes, and template:tenant:manager had none, so later
-- tenants inherited nothing.
--
-- Scope:
--   1. Grants all nine STORAGE permission codes to the tenant-manager template
--      (template:tenant:manager, tenant_id=0) so tenants created later inherit them.
--   2. Grants them to every existing active tenant:manager role, using each
--      role's own tenant_id.
-- It does not touch platform roles, other role codes, permission/API links,
-- plans or modules, and it creates no tenant data. STORAGE must still be enabled
-- on the tenant's plan separately. Object storage stays read-only by design: no
-- upload/bucket-management permission exists in the catalog.
--
-- Each code is verified against its live route before granting, so a catalog
-- mismatch fails the whole transaction instead of granting a dead capability.
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
  PERFORM pg_advisory_xact_lock(71919012);

  SELECT id INTO STRICT template_role_id FROM sys_roles
   WHERE code='template:tenant:manager' AND type='TEMPLATE'
     AND status='ON' AND deleted_at IS NULL;

  FOR target IN SELECT * FROM (VALUES
    ('storage:volume:create','GET','/api/v1/storage/classes'),
    ('storage:volume:create','POST','/api/v1/storage/volumes'),
    ('storage:volume:list','GET','/api/v1/storage/volumes'),
    ('storage:volume:get','GET','/api/v1/storage/volumes/{pvc_name}'),
    ('storage:volume:update','POST','/api/v1/storage/volumes/{data.pvc_name}:update'),
    ('storage:volume:delete','POST','/api/v1/storage/volumes/{data.pvc_name}:delete'),
    ('storage:bucket:list','GET','/api/v1/storage/buckets'),
    ('storage:object:list','GET','/api/v1/storage/buckets/{bucket_id}/objects'),
    ('storage:object:get','POST','/api/v1/storage/buckets/{data.bucket_id}/objects:head'),
    ('storage:object:download','POST','/api/v1/storage/buckets/{data.bucket_id}/objects:download-url')
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
  END LOOP;  -- storage permission codes
END
$grant$;
