-- Remove legacy umbrella permission -> API links superseded by the explicit
-- Model/Inference capability codes. Permission rows and role grants remain;
-- only stale API relations are removed.
--
-- Run after API catalog sync and before the explicit Model/Inference
-- registration scripts:
--   psql "$DSN" -v ON_ERROR_STOP=1 \
--     -f sql/data/20261015_retire_legacy_model_inference_manage_links.sql
BEGIN;

DO $$
BEGIN
  PERFORM pg_advisory_xact_lock(718294634);

  -- Model reads use model:service:read. Keep model:service:manage on
  -- mutating routes only.
  DELETE FROM sys_permission_apis pa
   USING sys_permissions p, sys_apis a
   WHERE pa.permission_id = p.id
     AND pa.api_id = a.id
     AND p.code = 'model:service:manage'
     AND a.module = 'ModelService'
     AND a.business_module = 'MODEL'
     AND a.method = 'GET'
     AND a.deleted_at IS NULL;

  -- Inference lifecycle routes now use action-specific permissions and reads
  -- use inference:service:read. The old umbrella relation is no longer part
  -- of the contract.
  DELETE FROM sys_permission_apis pa
   USING sys_permissions p, sys_apis a
   WHERE pa.permission_id = p.id
     AND pa.api_id = a.id
     AND p.code = 'inference:service:manage'
     AND a.module = 'InferenceService'
     AND a.business_module = 'INFERENCE'
     AND a.deleted_at IS NULL;
END $$;

COMMIT;
