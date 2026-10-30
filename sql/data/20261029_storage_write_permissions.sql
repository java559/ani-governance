-- Run after admin sync-apis; catalog only, without role or plan grants.
DO $$
DECLARE r record; selected_api bigint; selected_permission bigint; route_count bigint;
BEGIN
  FOR r IN SELECT * FROM (VALUES
    ('POST','/api/v1/storage/buckets','storage:bucket:create'),
    ('POST','/api/v1/storage/buckets/{data.bucket_id}/objects:upload-url','storage:object:upload'),
    ('POST','/api/v1/storage/buckets/{data.bucket_id}/objects:delete','storage:object:delete')
  ) AS contracts(method,path,code) LOOP
    SELECT count(*),min(a.id) INTO route_count,selected_api FROM sys_apis a
      WHERE a.path=r.path AND a.method=r.method AND a.deleted_at IS NULL
      AND a.business_module='STORAGE' AND a.status='ON';
    IF route_count<>1 THEN RAISE EXCEPTION 'Expected one active STORAGE route: % %',r.method,r.path; END IF;
    INSERT INTO sys_permissions(name,code,status) VALUES(r.code,r.code,'ON') ON CONFLICT(code) DO NOTHING;
    SELECT p.id INTO STRICT selected_permission FROM sys_permissions p
      WHERE p.code=r.code AND p.deleted_at IS NULL AND p.status='ON';
    IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api AND pa.permission_id<>selected_permission) THEN
      RAISE EXCEPTION 'Storage route already linked to another permission';
    END IF;
    IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api AND pa.permission_id=selected_permission AND pa.deleted_at IS NOT NULL) THEN
      RAISE EXCEPTION 'Existing storage permission link is deleted; explicit review required';
    END IF;
    INSERT INTO sys_permission_apis(permission_id,api_id) VALUES(selected_permission,selected_api) ON CONFLICT DO NOTHING;
  END LOOP;
END $$;
