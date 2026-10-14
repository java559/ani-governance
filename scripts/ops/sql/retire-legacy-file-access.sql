-- Explicit data cleanup for installations initialized before file management was
-- removed (2026-09-21; storage/file-avatar capabilities move to dedicated services).
-- Run with: psql -v ON_ERROR_STOP=1 -f sql/patches/20260921_drop_file_storage.sql.
-- Atomic and repeatable; never invoked by service startup or admin init.
--
-- Scope: DATA only. The schema removal (DROP TABLE "files"; ALTER TABLE "sys_users"
-- DROP COLUMN "avatar") is an Atlas migration and must be applied separately via
-- scripts/atlas.sh (see docs/file-storage-removal-plan.md 阶段 2.11 / 3.5).
-- The enum-valued columns (sys_apis.module, sys_apis.business_module,
-- sys_plan_modules.module) are character varying, not native PG enums, so removing
-- the FILE value needs no type change — only the row deletes below.
DO $patch$
BEGIN
  PERFORM pg_advisory_xact_lock(718294632);

  -- 1. Permission → API grants pointing at removed file/avatar routes.
  DELETE FROM sys_permission_apis
  WHERE api_id IN (
    SELECT id FROM sys_apis
    WHERE path LIKE '/api/v1/admin/files%'
       OR path LIKE '/api/v1/admin/file/%'
       OR path LIKE '/api/v1/admin/me/avatar'
  );

  -- 2. The removed file/avatar API catalog rows.
  DELETE FROM sys_apis
  WHERE path LIKE '/api/v1/admin/files%'
     OR path LIKE '/api/v1/admin/file/%'
     OR path LIKE '/api/v1/admin/me/avatar';

  -- 3. Permission → menu grants for the removed FileManagement menu.
  DELETE FROM sys_permission_menus
  WHERE menu_id IN (SELECT id FROM sys_menus WHERE name = 'FileManagement');

  -- 4. The removed FileManagement menu itself.
  DELETE FROM sys_menus WHERE name = 'FileManagement';

  -- 5. Plan whitelist rows carrying the removed FILE module value.
  DELETE FROM sys_plan_modules WHERE module = 'FILE';
END
$patch$;
