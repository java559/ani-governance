-- Explicitly apply after the reviewed OpenAPI API-catalog sync. No module,
-- plan, tenant or account is created. MODEL entitlement remains independently
-- required. Default grants cover only exact tenant-manager/template roles.
BEGIN;
DO $$
DECLARE item record; selected_api_id bigint; selected_permission_id bigint; route_count bigint;
BEGIN
 FOR item IN SELECT * FROM (VALUES
  ('POST','/api/v1/modeldev/environment:ensure','modeldev:manage_environment'),
  ('GET','/api/v1/modeldev/environment','modeldev:view_environment')
 ) AS contracts(method,path,code) LOOP
  SELECT count(*),min(a.id) INTO route_count,selected_api_id FROM public.sys_apis a
   WHERE a.path=item.path AND a.method=item.method AND a.deleted_at IS NULL
    AND a.scope='ADMIN' AND a.business_module='MODEL' AND a.status='ON';
  IF route_count<>1 THEN RAISE EXCEPTION 'ModelDev environment requires exactly one active catalog API for % %',item.method,item.path;END IF;
  INSERT INTO public.sys_permissions(name,code,status) VALUES(item.code,item.code,'ON') ON CONFLICT(code) DO NOTHING;
  SELECT p.id INTO STRICT selected_permission_id FROM public.sys_permissions p WHERE p.code=item.code AND p.deleted_at IS NULL;
  IF EXISTS(SELECT 1 FROM public.sys_permission_apis pa WHERE pa.api_id=selected_api_id AND pa.permission_id<>selected_permission_id AND pa.deleted_at IS NULL) THEN
   RAISE EXCEPTION 'ModelDev environment API has a different permission';
  END IF;
  INSERT INTO public.sys_permission_apis(permission_id,api_id) VALUES(selected_permission_id,selected_api_id) ON CONFLICT DO NOTHING;
  INSERT INTO public.sys_role_permissions(tenant_id,role_id,permission_id,status,effect)
   SELECT r.tenant_id,r.id,selected_permission_id,'ON','ALLOW' FROM public.sys_roles r
   WHERE r.deleted_at IS NULL AND r.status='ON' AND
    ((r.tenant_id=0 AND r.type='TEMPLATE' AND r.code='template:tenant:manager') OR
     (r.tenant_id>0 AND r.type='TENANT' AND r.code='tenant:manager'))
   ON CONFLICT DO NOTHING;
 END LOOP;
END $$;
COMMIT;
