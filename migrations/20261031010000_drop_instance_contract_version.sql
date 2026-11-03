-- Container configuration has one format. Original container tombstones and
-- frozen operations must be backed up and removed during the authorized
-- cutover before this explicit schema migration; startup migration stays off.
-- Other owners never assigned business meaning to this storage placeholder.
DO $$
BEGIN
 IF EXISTS (
  SELECT 1 FROM "sys_quota_operations"
  WHERE "owner_service" = 'ani-resource-service'
   AND "action" IN ('/instance.v1.InstanceService/AcceptCreate',
                    '/instance.v1.InstanceService/AcceptDelete')
 ) THEN
  RAISE EXCEPTION 'container configuration cutover requires an empty container operation ledger';
 END IF;
END
$$;
ALTER TABLE "sys_quota_operations"
 DROP CONSTRAINT "sys_quota_operations_instance_contract_version_ck",
 DROP COLUMN "instance_contract_version";
