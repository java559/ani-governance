-- Existing installations only. Run explicitly against the selected Governance
-- database BEFORE syncing the new API catalog, then refresh authorization
-- policies using docs/deployment.md. This changes template variable names only:
-- concrete public URLs, API IDs, status and permission/role/plan links remain.
-- Do not delete/recreate APIs or run enable-inference-access.sql for this change.
BEGIN;
LOCK TABLE sys_apis IN SHARE ROW EXCLUSIVE MODE;
DO $$
DECLARE action text; old_path text; new_path text; old_count bigint; new_count bigint;
BEGIN
 FOREACH action IN ARRAY ARRAY['delete', 'update', 'start', 'stop', 'restart'] LOOP
  old_path := '/api/v1/inference/services/{data.resource_id}:' || action;
  new_path := '/api/v1/inference/services/{resource_id}:' || action;
  SELECT count(*) INTO old_count FROM sys_apis
   WHERE method='POST' AND path=old_path AND deleted_at IS NULL;
  SELECT count(*) INTO new_count FROM sys_apis
   WHERE method='POST' AND path=new_path AND deleted_at IS NULL;
  IF old_count=0 AND new_count=1 AND EXISTS (
   SELECT 1 FROM sys_apis WHERE method='POST' AND path=new_path
    AND module='InferenceService' AND business_module='INFERENCE' AND scope='ADMIN' AND deleted_at IS NULL
  ) THEN CONTINUE; END IF;
  IF old_count<>1 OR new_count<>0 THEN
   RAISE EXCEPTION 'Expected exactly one legacy API or one migrated API for %, found legacy %, new %; review catalog before migration', action, old_count, new_count;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM sys_apis WHERE method='POST' AND path=old_path
    AND module='InferenceService' AND business_module='INFERENCE' AND scope='ADMIN' AND deleted_at IS NULL) THEN
   RAISE EXCEPTION 'Unexpected API owner for %', old_path;
  END IF;
  UPDATE sys_apis SET path=new_path, updated_at=now()
   WHERE method='POST' AND path=old_path AND module='InferenceService'
    AND business_module='INFERENCE' AND scope='ADMIN' AND deleted_at IS NULL;
 END LOOP;
END $$;
COMMIT;
