-- GOV-ACC-V12-01. Explicit deployment step AFTER admin sync-apis --dry-run
-- and admin sync-apis. This registers permissions; it grants no role, enables
-- no plan, changes no existing permission status and creates no tenant quota.
BEGIN;
DO $$
DECLARE r record; aid integer; pid integer;
BEGIN
 FOR r IN SELECT * FROM (VALUES
  ('GET','/api/v1/admin/accelerator/initialization','accelerator:initialization:read','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/discovered-nodes','accelerator:discovery:list','ACCELERATOR'),
  ('POST','/api/v1/admin/accelerator/initializations','accelerator:initialization:create','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/initializations/{operation_id}','accelerator:initialization:read','ACCELERATOR'),
  ('POST','/api/v1/admin/accelerator/initializations/{operation_id}:retry','accelerator:initialization:retry','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/clusters','accelerator:cluster:list','ACCELERATOR'),
  ('POST','/api/v1/admin/accelerator/clusters','accelerator:cluster:register','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/pools','accelerator:pool:list','ACCELERATOR'),
  ('POST','/api/v1/admin/accelerator/pools','accelerator:pool:create','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/supply-groups','accelerator:supply-group:list','ACCELERATOR'),
  ('POST','/api/v1/admin/accelerator/supply-groups:adopt','accelerator:supply-group:adopt','ACCELERATOR'),
  ('POST','/api/v1/admin/accelerator/supply-groups/{group_id}:set-admission','accelerator:supply-group:set-admission','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/profiles','accelerator:profile:admin-list','ACCELERATOR'),
  ('POST','/api/v1/admin/accelerator/profiles','accelerator:profile:publish','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/profiles:preview','accelerator:profile:preview','ACCELERATOR'),
  ('POST','/api/v1/admin/accelerator/profiles/{profile_id}:set-state','accelerator:profile:set-state','ACCELERATOR'),
  ('POST','/api/v1/admin/accelerator/profiles/{profile_id}:delete','accelerator:profile:delete','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/devices','accelerator:device:list','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/bindings','accelerator:binding:admin-list','ACCELERATOR'),
  ('GET','/api/v1/admin/accelerator/capacity','accelerator:capacity:admin-get','ACCELERATOR'),
  ('GET','/api/v1/accelerator/profiles','accelerator:profile:list','ACCELERATOR'),
  ('GET','/api/v1/accelerator/profiles/{profile_id}','accelerator:profile:get','ACCELERATOR'),
  ('GET','/api/v1/accelerator/capacity','accelerator:capacity:get','ACCELERATOR'),
  ('POST','/api/v1/accelerator/admission-preview','accelerator:admission:preview','ACCELERATOR'),
  ('GET','/api/v1/accelerator/usages','accelerator:usage:list','ACCELERATOR'),
  ('GET','/api/v1/accelerator/usages/{owner_service}/{resource_id}','accelerator:usage:get','ACCELERATOR'),
  ('GET','/api/v1/accelerator/usages/{owner_service}/{resource_id}/bindings','accelerator:binding:list','ACCELERATOR'),
  ('GET','/api/v1/me/quota-accounts','quota:account:self-read','TENANT')
 ) AS routes(method,path,permission,business_module) LOOP
  SELECT id INTO STRICT aid FROM sys_apis
   WHERE path=r.path AND method=r.method AND business_module=r.business_module;
  INSERT INTO sys_permissions(name,code,status) VALUES(r.permission,r.permission,'ON')
   ON CONFLICT(code) DO NOTHING;
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.permission;
  INSERT INTO sys_permission_apis(permission_id,api_id) VALUES(pid,aid)
   ON CONFLICT(permission_id,api_id) DO NOTHING;
 END LOOP;
END $$;
COMMIT;
