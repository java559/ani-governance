-- Explicit data step after the coordinated ModelDev/Gov contract migration.
-- The former capability already authorized the same input imports; preserve
-- permission IDs, status and existing role assignments while removing its
-- retired Release/binding name. No new grant or account is created here.
BEGIN;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM public.sys_permissions WHERE code='modeldev:manage_release_binding')
     AND EXISTS (SELECT 1 FROM public.sys_permissions WHERE code='modeldev:manage_input') THEN
    RAISE EXCEPTION 'conflicting ModelDev input capabilities require reconciliation';
  END IF;
  IF EXISTS (SELECT 1 FROM public.sys_apis WHERE path='/api/v1/modeldev/presets' AND method='GET')
     AND EXISTS (SELECT 1 FROM public.sys_apis WHERE path='/api/v1/modeldev/implementations' AND method='GET') THEN
    RAISE EXCEPTION 'conflicting ModelDev rule API rows require reconciliation';
  END IF;
END $$;
UPDATE public.sys_permissions
SET code='modeldev:manage_input'
WHERE code='modeldev:manage_release_binding';
-- Same read-only rule-query purpose; preserve API IDs, status, permission and
-- plan associations. This does not grant create/GPU/input-management rights.
UPDATE public.sys_apis
SET path='/api/v1/modeldev/implementations', operation='ModelDevService_ListImplementations'
WHERE path='/api/v1/modeldev/presets' AND method='GET';
COMMIT;
