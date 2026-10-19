-- Retire the pre-search API catalog entry that is no longer exposed by the
-- Model BFF. The current endpoint is GET /api/v1/models:search.
--
-- Run once with the migration/operator identity after the API catalog sync:
--   psql "$DSN" -v ON_ERROR_STOP=1 \
--     -f sql/data/20261015_retire_stale_model_search_api.sql
--
-- The row is kept for audit history and marked OFF. Permission relations are
-- deliberately retained; an inactive catalog row is fail-closed at runtime.
BEGIN;

DO $$
DECLARE
  active_count integer;
BEGIN
  PERFORM pg_advisory_xact_lock(718294633);

  SELECT count(*) INTO active_count
    FROM sys_apis
   WHERE method = 'POST'
     AND path = '/api/v1/models:search-external'
     AND module = 'ModelService'
     AND business_module = 'MODEL'
     AND status = 'ON'
     AND deleted_at IS NULL;

  IF active_count > 1 THEN
    RAISE EXCEPTION 'Expected at most one active stale Model search API, found %', active_count;
  END IF;

  UPDATE sys_apis
     SET status = 'OFF', updated_at = now()
   WHERE method = 'POST'
     AND path = '/api/v1/models:search-external'
     AND module = 'ModelService'
     AND business_module = 'MODEL'
     AND status = 'ON'
     AND deleted_at IS NULL;
END $$;

COMMIT;
