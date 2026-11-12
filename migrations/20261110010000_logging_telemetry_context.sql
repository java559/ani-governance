-- Logging phase one: independent telemetry contexts on the persisted records a
-- background worker later claims. Each column carries only TelemetryContext
-- {traceparent, tracestate, origin_trace_id}; none of them participates in
-- canonical_request, request_hash, idempotency keys, frozen specs or ACK
-- identity, and no new table is created.
--
-- Creation and close intents are stored separately because the source already
-- allows a close/stop action to reuse the creation operation id; a later user
-- action must never overwrite the creation trace. Existing rows stay NULL and
-- are treated as "missing metadata"; there is no historical backfill.
ALTER TABLE "sys_quota_operations" ADD COLUMN "telemetry_context" jsonb NULL;
ALTER TABLE "sys_modeldev_acceptances" ADD COLUMN "telemetry_context" jsonb NULL;
ALTER TABLE "sys_modeldev_acceptances" ADD COLUMN "close_telemetry_context" jsonb NULL;
ALTER TABLE "sys_tenants" ADD COLUMN "tenant_space_telemetry_context" jsonb NULL;
ALTER TABLE "sys_tenants" ADD COLUMN "tenant_space_close_telemetry_context" jsonb NULL;
ALTER TABLE "sys_sandbox_create_intents" ADD COLUMN "telemetry_context" jsonb NULL;
