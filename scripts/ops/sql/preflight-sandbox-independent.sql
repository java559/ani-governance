\set ON_ERROR_STOP on
-- Run read-only against the intended Governance database only after stopping
-- old Gov Sandbox create admission AND all old Gov dispatch workers. Keep them
-- stopped until coordinated Gov/Resource cutover; otherwise this check races.
-- A failed check requires individual investigation, never discard or rewrite a
-- frozen command or assume an unknown delivery did not execute.
BEGIN READ ONLY;
SELECT tenant_id, sandbox_id, operation_id, acknowledged, last_error_code, created_at, updated_at
FROM sys_sandbox_create_intents
WHERE NOT acknowledged AND instance_id <> ''
ORDER BY tenant_id, created_at, sandbox_id;
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM sys_sandbox_create_intents WHERE NOT acknowledged AND instance_id <> '') THEN
  RAISE EXCEPTION 'Sandbox cutover blocked: unresolved historical create intents exist';
 END IF;
END $$;
COMMIT;
