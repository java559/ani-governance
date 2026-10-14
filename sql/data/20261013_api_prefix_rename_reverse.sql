-- Stage A REVERSE: /api/v1/* -> /admin/v1/* (controlled rollback, R04).
-- Mirror of 20261013_api_prefix_rename.sql. Renames the `path` column only.
-- Apply inside ONE transaction:
--   psql -v ON_ERROR_STOP=1 -1 -f sql/data/20261013_api_prefix_rename_reverse.sql
--
-- Expected counts (inverse map):
--   total=191  generic=180  modeldev=10  mfa_verify=1

DO $$
DECLARE
  src_total bigint; src_gen bigint; src_mdv bigint; src_mfav bigint;
  tgt_old   bigint; affected bigint; applied bigint := 0;
BEGIN
  SELECT count(*) INTO src_total FROM sys_apis WHERE deleted_at IS NULL AND path LIKE '/api/v1/admin/%';
  SELECT count(*) INTO src_gen   FROM sys_apis WHERE deleted_at IS NULL AND path LIKE '/api/v1/admin/%';
  SELECT count(*) INTO src_mdv   FROM sys_apis WHERE deleted_at IS NULL AND path LIKE '/api/v1/modeldev/%';
  SELECT count(*) INTO src_mfav  FROM sys_apis WHERE deleted_at IS NULL AND path = '/api/v1/auth/mfa/verify';
  SELECT count(*) INTO tgt_old   FROM sys_apis WHERE deleted_at IS NULL AND path LIKE '/admin/v1/%';

  IF src_total = 0 AND src_mdv = 0 AND src_mfav = 0 THEN
    RAISE NOTICE 'reverse rename: nothing in new space, nothing to do';
    RETURN;
  END IF;
  IF src_total <> 180 OR src_mdv <> 10 OR src_mfav <> 1 THEN
    RAISE EXCEPTION 'unexpected source state: generic=% (want 180), modeldev=% (want 10), mfa/verify=% (want 1)',
      src_total, src_mdv, src_mfav;
  END IF;
  IF tgt_old <> 0 THEN
    RAISE EXCEPTION 'old prefix already occupied: % rows under /admin/v1/; refusing to proceed', tgt_old;
  END IF;

  UPDATE sys_apis SET path='/admin/v1/mfa/verify', updated_at=NOW()
    WHERE deleted_at IS NULL AND path='/api/v1/auth/mfa/verify';
  GET DIAGNOSTICS affected = ROW_COUNT;
  IF affected <> 1 THEN RAISE EXCEPTION 'mfa/verify reverse affected % rows, want 1', affected; END IF;
  applied := applied + affected;

  UPDATE sys_apis SET path = '/admin/v1/modeldev/' || substring(path from '/api/v1/modeldev/(.*)'), updated_at=NOW()
    WHERE deleted_at IS NULL AND path LIKE '/api/v1/modeldev/%';
  GET DIAGNOSTICS affected = ROW_COUNT;
  IF affected <> 10 THEN RAISE EXCEPTION 'modeldev reverse affected % rows, want 10', affected; END IF;
  applied := applied + affected;

  UPDATE sys_apis SET path = '/admin/v1/' || substring(path from '/api/v1/admin/(.*)'), updated_at=NOW()
    WHERE deleted_at IS NULL AND path LIKE '/api/v1/admin/%';
  GET DIAGNOSTICS affected = ROW_COUNT;
  IF affected <> 180 THEN RAISE EXCEPTION 'generic reverse affected % rows, want 180', affected; END IF;
  applied := applied + affected;

  RAISE NOTICE 'reverse rename: applied=% rows', applied;
END $$;
