-- 只为两条关联实例路由建立 API 关联，复用既有 network:vpc:get 与 network:subnet:get；不新建权限、不授权角色。
-- 前置：新路由已由 admin sync-apis 写入 sys_apis（目录同步不自动授权，复用码同样需要关联）。
DO $$
DECLARE selected_api bigint; selected_permission bigint; route_count bigint;
BEGIN
  SELECT count(*),min(a.id) INTO route_count,selected_api FROM sys_apis a
    WHERE a.path='/api/v1/networks/vpcs/{vpc_id}/associated-instances' AND a.method='GET'
    AND a.deleted_at IS NULL AND a.business_module='NETWORK' AND a.status='ON';
  IF route_count<>1 THEN RAISE EXCEPTION 'Expected one active NETWORK VPC associated-instances route, found %',route_count; END IF;

  SELECT p.id INTO STRICT selected_permission FROM sys_permissions p
    WHERE p.code='network:vpc:get' AND p.deleted_at IS NULL AND p.status='ON';

  IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api AND pa.permission_id<>selected_permission) THEN
    RAISE EXCEPTION 'VPC associated-instances route already linked to another permission';
  END IF;
  IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api AND pa.permission_id=selected_permission AND pa.deleted_at IS NOT NULL) THEN
    RAISE EXCEPTION 'Existing VPC associated-instances permission link is deleted; explicit review required';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id) VALUES(selected_permission,selected_api) ON CONFLICT DO NOTHING;
END $$;

DO $$
DECLARE selected_api bigint; selected_permission bigint; route_count bigint;
BEGIN
  SELECT count(*),min(a.id) INTO route_count,selected_api FROM sys_apis a
    WHERE a.path='/api/v1/networks/subnets/{subnet_id}/associated-instances' AND a.method='GET'
    AND a.deleted_at IS NULL AND a.business_module='NETWORK' AND a.status='ON';
  IF route_count<>1 THEN RAISE EXCEPTION 'Expected one active NETWORK subnet associated-instances route, found %',route_count; END IF;

  SELECT p.id INTO STRICT selected_permission FROM sys_permissions p
    WHERE p.code='network:subnet:get' AND p.deleted_at IS NULL AND p.status='ON';

  IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api AND pa.permission_id<>selected_permission) THEN
    RAISE EXCEPTION 'Subnet associated-instances route already linked to another permission';
  END IF;
  IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api AND pa.permission_id=selected_permission AND pa.deleted_at IS NOT NULL) THEN
    RAISE EXCEPTION 'Existing subnet associated-instances permission link is deleted; explicit review required';
  END IF;
  INSERT INTO sys_permission_apis(permission_id,api_id) VALUES(selected_permission,selected_api) ON CONFLICT DO NOTHING;
END $$;
