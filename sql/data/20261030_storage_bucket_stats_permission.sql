-- 只为桶统计路由建立 API 关联，复用既有 storage:bucket:list；不新建权限、不授权。
DO $$
DECLARE selected_api bigint; selected_permission bigint; route_count bigint;
BEGIN
  SELECT count(*),min(a.id) INTO route_count,selected_api FROM sys_apis a
    WHERE a.path='/api/v1/storage/buckets/{bucket_id}/stats' AND a.method='GET'
    AND a.deleted_at IS NULL AND a.business_module='STORAGE' AND a.status='ON';
  IF route_count<>1 THEN RAISE EXCEPTION 'Expected one active STORAGE stats route, found %',route_count; END IF;

  SELECT p.id INTO STRICT selected_permission FROM sys_permissions p
    WHERE p.code='storage:bucket:list' AND p.deleted_at IS NULL AND p.status='ON';

  IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api AND pa.permission_id<>selected_permission) THEN
    RAISE EXCEPTION 'Storage stats route already linked to another permission';
  END IF;
  IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api AND pa.permission_id=selected_permission AND pa.deleted_at IS NOT NULL) THEN
    RAISE EXCEPTION 'Existing storage stats permission link is deleted; explicit review required';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id) VALUES(selected_permission,selected_api) ON CONFLICT DO NOTHING;
END $$;
