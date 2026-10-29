-- Repair deployments that applied the early create-intents-only revision.
-- Fresh installations already have both tables from sandbox_parent_fence.
CREATE TABLE IF NOT EXISTS sys_sandbox_parent_delete_checks (
 tenant_id bigint NOT NULL REFERENCES sys_tenants(id),
 instance_id text NOT NULL,
 check_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY (tenant_id, instance_id),
 UNIQUE (check_id)
);
-- Historical queued deletes have no marker and are never dispatched by the
-- new worker. New markers are written in the same transaction as the ledger.
CREATE TABLE IF NOT EXISTS sys_sandbox_guarded_instance_deletes (
 tenant_id bigint NOT NULL REFERENCES sys_tenants(id),
 instance_id text NOT NULL,
 operation_id text NOT NULL,
 check_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY (tenant_id, operation_id)
);
