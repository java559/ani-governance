-- DO NOT RUN BY DEFAULT.
--
-- Explicit revocation of the VM image tenant-manager grants. The product owner ruled
-- on 2026-11-03 that these grants are a permanent forward change: the normal rollback
-- of a feature batch does NOT execute this file. Use it only when the owner explicitly
-- asks to withdraw the capability.
--
-- It deletes exactly the sys_role_permissions ids passed in. No pattern matching on
-- role, tenant or permission code, because vm:read is also used by the pre-existing
-- VM routes and a pattern delete would silently remove unrelated grants.
--
-- Usage (ids come from evidence/granted-ids.json, captured by the register/grant run):
--   psql -X -v ON_ERROR_STOP=1 -v grant_ids='246,247,248' \
--        -f scripts/ops/sql/grant-vm-image-tenant-permissions.rollback.sql
-- Then restart every Governance instance so Casbin rebuilds its policies.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
LOCK TABLE sys_role_permissions IN SHARE ROW EXCLUSIVE MODE;

-- psql does not interpolate inside dollar-quoted bodies, so the manifest enters as
-- ordinary SQL and the guard block only ever reads the table.
CREATE TEMP TABLE _revoke_role_permission_ids (id bigint PRIMARY KEY) ON COMMIT DROP;
INSERT INTO _revoke_role_permission_ids
  SELECT DISTINCT btrim(x)::bigint
    FROM unnest(string_to_array(:'grant_ids', ',')) AS x
   WHERE btrim(x) <> '';

DO $$
DECLARE
  missing bigint;
  foreign_rows bigint;
  supplied bigint;
BEGIN
  SELECT count(*) INTO supplied FROM _revoke_role_permission_ids;
  IF supplied = 0 THEN
    RAISE EXCEPTION 'grant_ids resolved to an empty list; pass the ids from evidence/granted-ids.json';
  END IF;

  SELECT count(*) INTO missing FROM _revoke_role_permission_ids v
    WHERE NOT EXISTS (SELECT 1 FROM sys_role_permissions rp WHERE rp.id=v.id);
  IF missing > 0 THEN
    RAISE EXCEPTION '% supplied ids no longer exist in sys_role_permissions; refresh the manifest first', missing;
  END IF;

  -- Every id must be a live ALLOW grant for one of the four image-related codes on a
  -- tenant-manager or template role. Anything else means the manifest and the
  -- database disagree, so nothing is deleted.
  SELECT count(*) INTO foreign_rows FROM sys_role_permissions rp
    JOIN _revoke_role_permission_ids v ON v.id=rp.id
    JOIN sys_roles r ON r.id=rp.role_id
    JOIN sys_permissions p ON p.id=rp.permission_id
   WHERE NOT (r.code IN ('template:tenant:manager','tenant:manager')
              AND r.type IN ('TEMPLATE','TENANT')
              AND p.code IN ('vm:read','vm:image:upload','vm:image:update','vm:image:delete')
              AND rp.deleted_at IS NULL
              AND rp.status='ON' AND rp.effect='ALLOW');
  IF foreign_rows > 0 THEN
    RAISE EXCEPTION '% of the supplied ids are not tenant-manager image grants; refusing to delete', foreign_rows;
  END IF;

  DELETE FROM sys_role_permissions rp
    WHERE rp.id IN (SELECT id FROM _revoke_role_permission_ids);
END $$;
COMMIT;
