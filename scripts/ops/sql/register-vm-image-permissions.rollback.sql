-- DO NOT RUN BY DEFAULT.
--
-- Explicit revocation of the VM image permission catalog entries. Pairs with
-- register-vm-image-permissions.sql. The normal rollback of a feature batch keeps
-- this layer in place (owner decision 2026-11-03), so run it only when the permission
-- layer itself is being withdrawn.
--
-- Delete order follows the foreign keys: links, then permissions, then API rows. The
-- four tables are one unit: keeping a grant while deleting the permission it points at
-- would leave a dangling reference, and keeping the permission while deleting its route
-- leaves a capability that can never be exercised.
--
-- Usage:
--   psql -X -v ON_ERROR_STOP=1 \
--        -v link_ids='12,13,14,15,16,17' \
--        -v permission_ids='260,261,262' \
--        -v api_ids='520,521,522,523,524,525' \
--        -f scripts/ops/sql/register-vm-image-permissions.rollback.sql
-- All three lists are required and come from evidence/granted-ids.json.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
LOCK TABLE sys_permissions,sys_apis,sys_permission_apis IN SHARE ROW EXCLUSIVE MODE;

CREATE TEMP TABLE _revoke_link_ids (id bigint PRIMARY KEY) ON COMMIT DROP;
CREATE TEMP TABLE _revoke_permission_ids (id bigint PRIMARY KEY) ON COMMIT DROP;
CREATE TEMP TABLE _revoke_api_ids (id bigint PRIMARY KEY) ON COMMIT DROP;

INSERT INTO _revoke_link_ids SELECT DISTINCT btrim(x)::bigint FROM unnest(string_to_array(:'link_ids', ',')) AS x WHERE btrim(x) <> '';
INSERT INTO _revoke_permission_ids SELECT DISTINCT btrim(x)::bigint FROM unnest(string_to_array(:'permission_ids', ',')) AS x WHERE btrim(x) <> '';
INSERT INTO _revoke_api_ids SELECT DISTINCT btrim(x)::bigint FROM unnest(string_to_array(:'api_ids', ',')) AS x WHERE btrim(x) <> '';

DO $$
DECLARE
  foreign_rows bigint;
BEGIN
  -- Every API row must belong to the image catalog and the image service identity.
  SELECT count(*) INTO foreign_rows FROM sys_apis a
    JOIN _revoke_api_ids v ON v.id=a.id
   WHERE NOT (a.path LIKE '/api/v1/virtual-machine-images%' AND a.module='VMImageService');
  IF foreign_rows > 0 THEN
    RAISE EXCEPTION '% supplied api ids are not VM image catalog rows; refusing to delete', foreign_rows;
  END IF;

  -- Every permission must be one of the three image codes. vm:read is deliberately
  -- excluded: it predates this batch and stays registered even if the image layer is
  -- withdrawn, otherwise the existing VM routes would lose their own link.
  SELECT count(*) INTO foreign_rows FROM sys_permissions p
    JOIN _revoke_permission_ids v ON v.id=p.id
   WHERE NOT (p.code IN ('vm:image:upload','vm:image:update','vm:image:delete'));
  IF foreign_rows > 0 THEN
    RAISE EXCEPTION '% supplied permission ids are not image codes; refusing to delete', foreign_rows;
  END IF;

  -- Any live role grant still pointing at these permissions blocks the revocation.
  SELECT count(*) INTO foreign_rows FROM sys_role_permissions rp
    JOIN _revoke_permission_ids v ON v.id=rp.permission_id
   WHERE rp.deleted_at IS NULL;
  IF foreign_rows > 0 THEN
    RAISE EXCEPTION '% live role grants still reference these permissions; run the grant rollback first', foreign_rows;
  END IF;

  DELETE FROM sys_permission_apis WHERE id IN (SELECT id FROM _revoke_link_ids);
  DELETE FROM sys_permissions WHERE id IN (SELECT id FROM _revoke_permission_ids);
  DELETE FROM sys_apis WHERE id IN (SELECT id FROM _revoke_api_ids);
END $$;
COMMIT;
