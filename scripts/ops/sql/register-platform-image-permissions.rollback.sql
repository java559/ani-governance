-- DO NOT RUN BY DEFAULT.
--
-- Explicit revocation of the platform image permission catalog entries. Pairs with
-- register-platform-image-permissions.sql. The normal rollback of a feature batch keeps
-- this layer in place, so run it only when the permission layer itself is being
-- withdrawn.
--
-- Delete order follows the foreign keys: links, then permissions. The sys_apis rows
-- are NOT deleted here: they belong to the API catalog that sync-apis maintains, and
-- withdrawing them would make the running binary disagree with the catalog.
--
-- Usage:
--   psql -X -v ON_ERROR_STOP=1 \
--        -v link_ids='12,13' \
--        -v permission_ids='260,261' \
--        -f scripts/ops/sql/register-platform-image-permissions.rollback.sql
-- Both lists come from the register SQL output.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='45s';
LOCK TABLE sys_permissions,sys_apis,sys_permission_apis IN SHARE ROW EXCLUSIVE MODE;

CREATE TEMP TABLE _revoke_link_ids (id bigint PRIMARY KEY) ON COMMIT DROP;
CREATE TEMP TABLE _revoke_permission_ids (id bigint PRIMARY KEY) ON COMMIT DROP;

INSERT INTO _revoke_link_ids SELECT DISTINCT btrim(x)::bigint FROM unnest(string_to_array(:'link_ids', ',')) AS x WHERE btrim(x) <> '';
INSERT INTO _revoke_permission_ids SELECT DISTINCT btrim(x)::bigint FROM unnest(string_to_array(:'permission_ids', ',')) AS x WHERE btrim(x) <> '';

DO $$
DECLARE
  foreign_rows bigint;
BEGIN
  -- Every permission must be one of the 22 platform image codes.
  SELECT count(*) INTO foreign_rows FROM sys_permissions p
    JOIN _revoke_permission_ids v ON v.id=p.id
   WHERE NOT (p.code LIKE 'image:platform:%');
  IF foreign_rows > 0 THEN
    RAISE EXCEPTION '% supplied permission ids are not platform image codes; refusing to delete', foreign_rows;
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
END $$;
COMMIT;
