-- GPU v1.1. Run only after admin sync-apis --dry-run and explicit sync.
-- Reuses already-authorized read capabilities; changes no users, roles, policy,
-- permission status, plans or quota accounts. Missing prerequisites fail closed.
-- Match the generated OpenAPI catalogue's camelCase parameter names. Runtime
-- proto templates use snake_case; existing permission normalization equates them.
BEGIN;
DO $$
DECLARE r record; aid integer; pid integer;
BEGIN
 FOR r IN SELECT * FROM (VALUES
  ('GET','/api/v1/admin/gpu/devices','accelerator:device:list','ACCELERATOR'),
  ('GET','/api/v1/admin/gpu/tenants','tenant:quota-read','ACCELERATOR'),
  ('GET','/api/v1/admin/gpu/devices/{deviceId}/metrics/latest','accelerator:device:list','ACCELERATOR'),
  ('POST','/api/v1/admin/gpu/devices/{deviceId}/metrics/query','accelerator:device:list','ACCELERATOR'),
  ('GET','/api/v1/admin/gpu/metrics/latest','accelerator:device:list','ACCELERATOR'),
  ('POST','/api/v1/admin/gpu/metrics/query','accelerator:device:list','ACCELERATOR'),
  ('GET','/api/v1/admin/gpu/tenants/{tenantId}/metrics/latest','tenant:quota-read','ACCELERATOR'),
  ('POST','/api/v1/admin/gpu/tenants/{tenantId}/metrics/query','tenant:quota-read','ACCELERATOR'),
  ('GET','/api/v1/gpu/metrics/latest','accelerator:usage:list','ACCELERATOR'),
  ('POST','/api/v1/gpu/metrics/query','accelerator:usage:list','ACCELERATOR'),
  ('GET','/api/v1/gpu/resources/{resourceKind}/{resourceId}/metrics/latest','accelerator:usage:get','ACCELERATOR'),
  ('POST','/api/v1/gpu/resources/{resourceKind}/{resourceId}/metrics/query','accelerator:usage:get','ACCELERATOR'),
  ('GET','/api/v1/admin/tenants/quota-account-summaries','tenant:quota-read','TENANT')
 ) AS routes(method,path,permission,business_module) LOOP
  SELECT id INTO STRICT aid FROM sys_apis WHERE method=r.method AND path=r.path AND business_module=r.business_module;
  SELECT id INTO STRICT pid FROM sys_permissions WHERE code=r.permission AND status='ON';
  INSERT INTO sys_permission_apis(permission_id,api_id) VALUES(pid,aid) ON CONFLICT(permission_id,api_id) DO NOTHING;
 END LOOP;
END $$;
COMMIT;
