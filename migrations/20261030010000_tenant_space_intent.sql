-- A tenant row is the durable provisioning intent. Existing tenants are not
-- silently enrolled; newly created tenants are explicitly queued by Gov.
ALTER TABLE public.sys_tenants
  ADD COLUMN tenant_space_state character varying NOT NULL DEFAULT 'LEGACY',
  ADD COLUMN tenant_space_operation_id character varying,
  ADD COLUMN tenant_space_lease_generation bigint NOT NULL DEFAULT 0,
  ADD COLUMN tenant_space_lease_owner character varying(128),
  ADD COLUMN tenant_space_lease_until timestamptz,
  ADD COLUMN tenant_space_next_attempt_at timestamptz,
  ADD CONSTRAINT sys_tenants_space_state_ck CHECK
    (tenant_space_state IN ('LEGACY','QUEUED','DISPATCHING','UNKNOWN','READY','CLOSING','CLOSED')),
  ADD CONSTRAINT sys_tenants_space_identity_ck CHECK
    ((tenant_space_state = 'LEGACY' AND tenant_space_operation_id IS NULL) OR
     (tenant_space_state <> 'LEGACY' AND tenant_space_operation_id IS NOT NULL AND
      tenant_space_operation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')),
  ADD CONSTRAINT sys_tenants_space_lease_generation_ck CHECK (tenant_space_lease_generation >= 0),
  ADD CONSTRAINT sys_tenants_space_lease_ck CHECK
    ((tenant_space_state = 'DISPATCHING' AND tenant_space_lease_owner IS NOT NULL AND
      length(tenant_space_lease_owner) > 0 AND tenant_space_lease_until IS NOT NULL AND tenant_space_lease_generation > 0) OR
     (tenant_space_state <> 'DISPATCHING' AND tenant_space_lease_owner IS NULL AND tenant_space_lease_until IS NULL));

CREATE UNIQUE INDEX sys_tenants_tenant_space_operation_id_key ON public.sys_tenants (tenant_space_operation_id);
