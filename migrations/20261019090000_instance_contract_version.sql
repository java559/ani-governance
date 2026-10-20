-- New admissions freeze their instance contract in the same operation transaction.
-- The default classifies historical operations without rewriting their payloads.
ALTER TABLE "public"."sys_quota_operations"
 ADD COLUMN "instance_contract_version" bigint NOT NULL DEFAULT 1,
 ADD CONSTRAINT "sys_quota_operations_instance_contract_version_ck"
 CHECK (instance_contract_version IN (1, 2));
COMMENT ON COLUMN "public"."sys_quota_operations"."instance_contract_version"
 IS '不可变实例合同版本，历史记录为1';
