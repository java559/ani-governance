-- Retire Governance's deferred Inference management surface after deploying
-- a build without those routes. Review the matching rows and back them up
-- before applying to an explicitly selected database. API IDs, grants,
-- historical quota operations and Inference workload data are retained.
BEGIN;

UPDATE sys_apis
   SET status = 'OFF', updated_at = now()
 WHERE module = 'InferenceService'
   AND business_module = 'INFERENCE'
   AND path LIKE '/api/v1/inference/%'
   AND deleted_at IS NULL;

-- A permission shared with any other active API must remain available.
UPDATE sys_permissions AS permission
   SET status = 'OFF', updated_at = now()
 WHERE permission.code IN (
   'inference:service:read', 'inference:service:create',
   'inference:service:delete', 'inference:service:update',
   'inference:service:start', 'inference:service:stop',
   'inference:service:restart'
 )
   AND permission.deleted_at IS NULL
   AND NOT EXISTS (
     SELECT 1 FROM sys_permission_apis AS link
     JOIN sys_apis AS api ON api.id = link.api_id
     WHERE link.permission_id = permission.id AND link.deleted_at IS NULL
       AND api.status = 'ON' AND api.deleted_at IS NULL
   );

COMMIT;
