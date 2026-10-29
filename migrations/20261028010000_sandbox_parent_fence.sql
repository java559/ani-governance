-- Sandbox commands and instance delete preflight share the sys_tenants row lock.
-- An unacknowledged command must survive process death and may not expire.
CREATE TABLE sys_sandbox_create_intents (
 tenant_id bigint NOT NULL REFERENCES sys_tenants(id),
 resource_tenant_id text NOT NULL,
 instance_id text NOT NULL,
 sandbox_id text NOT NULL,
 operation_id text NOT NULL,
 intent_digest text NOT NULL,
 actor text NOT NULL,
 command jsonb NOT NULL,
 acknowledged boolean NOT NULL DEFAULT false,
 lease_generation bigint NOT NULL DEFAULT 0,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 retry_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 last_error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY (tenant_id, sandbox_id),
 UNIQUE (tenant_id, operation_id)
);
CREATE INDEX sys_sandbox_create_pending_idx ON sys_sandbox_create_intents (tenant_id, instance_id) WHERE NOT acknowledged;
CREATE INDEX sys_sandbox_create_dispatch_idx ON sys_sandbox_create_intents (retry_at, lease_until, created_at) WHERE NOT acknowledged;
CREATE TABLE sys_sandbox_parent_delete_checks (
 tenant_id bigint NOT NULL REFERENCES sys_tenants(id),
 instance_id text NOT NULL,
 check_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY (tenant_id, instance_id),
 UNIQUE (check_id)
);
-- Historical queued deletes have no marker and are never dispatched by the
-- new worker. New markers are written in the same transaction as the ledger.
CREATE TABLE sys_sandbox_guarded_instance_deletes (
 tenant_id bigint NOT NULL REFERENCES sys_tenants(id),
 instance_id text NOT NULL,
 operation_id text NOT NULL,
 check_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY (tenant_id, operation_id)
);
