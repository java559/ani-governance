-- Stage A: rename existing sys_apis paths /admin/v1/* -> /api/v1/admin/*.
-- Special cases: modeldev business domain stays under /api/v1/modeldev/*;
-- mfa/verify (login challenge, unauthenticated) moves to /api/v1/auth/mfa/verify.
--
-- Apply explicitly with the migration/operator identity, inside ONE transaction:
--   psql -v ON_ERROR_STOP=1 -1 -f sql/data/20261013_api_prefix_rename.sql
--
-- Method: prefix replacement (not per-row literal match) so that path parameter
-- names already stored in the DB are preserved and stay consistent with the
-- embedded OpenAPI. Renames the `path` column only; api_id, status, scope,
-- module, business_module and all sys_permission_apis relations are untouched.
-- Not driven by sync-apis (which would INSERT new rows instead of renaming).
-- Fails closed on: unexpected source count, occupied target prefix, or a
-- mismatched affected-row count. Idempotent: already-migrated state is skipped.
--
-- Expected counts (source of truth: route-map.tsv, action=rename):
--   total=191  generic=180  modeldev=10  mfa_verify=1

DO $$
DECLARE
  src_total   bigint;
  src_gen     bigint;
  src_mdv     bigint;
  src_mfav    bigint;
  tgt_admin   bigint;
  tgt_mdv     bigint;
  tgt_mfav    bigint;
  affected    bigint;
  applied     bigint := 0;
BEGIN
  SELECT count(*) INTO src_total FROM sys_apis WHERE deleted_at IS NULL AND path LIKE '/admin/v1/%';
  SELECT count(*) INTO src_gen   FROM sys_apis WHERE deleted_at IS NULL AND path LIKE '/admin/v1/%' AND path NOT LIKE '/admin/v1/modeldev/%' AND path <> '/admin/v1/mfa/verify';
  SELECT count(*) INTO src_mdv   FROM sys_apis WHERE deleted_at IS NULL AND path LIKE '/admin/v1/modeldev/%';
  SELECT count(*) INTO src_mfav  FROM sys_apis WHERE deleted_at IS NULL AND path = '/admin/v1/mfa/verify';

  SELECT count(*) INTO tgt_admin FROM sys_apis WHERE deleted_at IS NULL AND path LIKE '/api/v1/admin/%';
  SELECT count(*) INTO tgt_mdv   FROM sys_apis WHERE deleted_at IS NULL AND path LIKE '/api/v1/modeldev/%';
  SELECT count(*) INTO tgt_mfav  FROM sys_apis WHERE deleted_at IS NULL AND path = '/api/v1/auth/mfa/verify';

  -- Idempotent: everything already migrated, nothing in old space.
  IF src_total = 0 AND 191 = (tgt_admin + tgt_mdv + tgt_mfav) THEN
    RAISE NOTICE 'api prefix rename: already applied (target=% rows), nothing to do', tgt_admin + tgt_mdv + tgt_mfav;
    RETURN;
  END IF;

  -- Fail-open guards.
  IF src_total <> 191 OR src_gen <> 180 OR src_mdv <> 10 OR src_mfav <> 1 THEN
    RAISE EXCEPTION 'unexpected source state: total=% (want 191), generic=% (want 180), modeldev=% (want 10), mfa/verify=% (want 1)',
      src_total, src_gen, src_mdv, src_mfav;
  END IF;
  IF tgt_admin <> 0 OR tgt_mdv <> 0 OR tgt_mfav <> 0 THEN
    RAISE EXCEPTION 'target prefix already occupied: admin=%, modeldev=%, mfa/verify=%; refusing to proceed',
      tgt_admin, tgt_mdv, tgt_mfav;
  END IF;

  -- 1) mfa/verify -> /api/v1/auth/mfa/verify
  UPDATE sys_apis SET path='/api/v1/auth/mfa/verify', updated_at=NOW()
    WHERE deleted_at IS NULL AND path='/admin/v1/mfa/verify';
  GET DIAGNOSTICS affected = ROW_COUNT;
  IF affected <> 1 THEN RAISE EXCEPTION 'mfa/verify rename affected % rows, want 1', affected; END IF;
  applied := applied + affected;

  -- 2) modeldev/* -> /api/v1/modeldev/*
  UPDATE sys_apis SET path = '/api/v1/modeldev/' || substring(path from '/admin/v1/modeldev/(.*)'), updated_at=NOW()
    WHERE deleted_at IS NULL AND path LIKE '/admin/v1/modeldev/%';
  GET DIAGNOSTICS affected = ROW_COUNT;
  IF affected <> 10 THEN RAISE EXCEPTION 'modeldev rename affected % rows, want 10', affected; END IF;
  applied := applied + affected;

  -- 3) everything else /admin/v1/ -> /api/v1/admin/
  UPDATE sys_apis SET path = '/api/v1/admin/' || substring(path from '/admin/v1/(.*)'), updated_at=NOW()
    WHERE deleted_at IS NULL AND path LIKE '/admin/v1/%';
  GET DIAGNOSTICS affected = ROW_COUNT;
  IF affected <> 180 THEN RAISE EXCEPTION 'generic rename affected % rows, want 180', affected; END IF;
  applied := applied + affected;

  RAISE NOTICE 'api prefix rename: applied=% rows', applied;
END $$;
