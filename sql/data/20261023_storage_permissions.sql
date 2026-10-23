-- 存储目录导入：在 OpenAPI 目录同步（admin sync-apis）之后单独执行。
-- 只建权限点与 API 关联，不授权角色、不启用套餐、不改变已有 OFF 权限；
-- 路由缺失、重复或已关联其他权限时 fail closed。
-- 执行：psql -v ON_ERROR_STOP=1 -1 -f 20261023_storage_permissions.sql
DO $$
DECLARE r record; selected_api_id bigint; selected_permission_id bigint; route_count bigint;
BEGIN
  FOR r IN SELECT * FROM (VALUES
    ('GET','/api/v1/storage/classes','storage:volume:create'),
    ('POST','/api/v1/storage/volumes','storage:volume:create'),
    ('GET','/api/v1/storage/volumes','storage:volume:list'),
    ('GET','/api/v1/storage/volumes/{pvc_name}','storage:volume:get'),
    ('POST','/api/v1/storage/volumes/{data.pvc_name}:update','storage:volume:update'),
    ('POST','/api/v1/storage/volumes/{data.pvc_name}:delete','storage:volume:delete'),
    ('GET','/api/v1/storage/buckets','storage:bucket:list'),
    ('GET','/api/v1/storage/buckets/{bucket_id}/objects','storage:object:list'),
    ('POST','/api/v1/storage/buckets/{data.bucket_id}/objects:head','storage:object:get'),
    ('POST','/api/v1/storage/buckets/{data.bucket_id}/objects:download-url','storage:object:download')
  ) AS contracts(method,path,code) LOOP
    SELECT count(*),min(a.id) INTO route_count,selected_api_id FROM sys_apis a
      WHERE a.path=r.path AND a.method=r.method AND a.deleted_at IS NULL
      AND a.business_module='STORAGE' AND a.status='ON';
    IF route_count<>1 THEN RAISE EXCEPTION 'Storage catalog must contain one active route for % %',r.method,r.path; END IF;
    INSERT INTO sys_permissions(name,code,status) VALUES(r.code,r.code,'ON') ON CONFLICT(code) DO NOTHING;
    SELECT p.id INTO STRICT selected_permission_id FROM sys_permissions p WHERE p.code=r.code AND p.deleted_at IS NULL;
    IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api_id AND pa.permission_id<>selected_permission_id) THEN
      RAISE EXCEPTION 'Storage route has another permission; review existing grants before import';
    END IF;
    INSERT INTO sys_permission_apis(permission_id,api_id) VALUES(selected_permission_id,selected_api_id) ON CONFLICT DO NOTHING;
  END LOOP;
END $$;
