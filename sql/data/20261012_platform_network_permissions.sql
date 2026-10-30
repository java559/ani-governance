-- Apply explicitly after OpenAPI catalog sync, inside one transaction (-1).
-- Catalog only: never enables modules or grants roles/API keys. Existing OFF
-- permissions stay OFF. Ambiguous/missing routes or foreign grants fail closed.
DO $$
DECLARE r record; selected_api_id bigint; selected_permission_id bigint; route_count bigint;
BEGIN
  FOR r IN SELECT * FROM (VALUES
    ('POST','/api/v1/admin/networks/vlan-networks','network:platform:vlan-network:create'),
    ('GET','/api/v1/admin/networks/vlan-networks/{vlan_network_id}','network:platform:vlan-network:get'),
    ('GET','/api/v1/admin/networks/vlan-networks','network:platform:vlan-network:list'),
    ('DELETE','/api/v1/admin/networks/vlan-networks/{vlan_network_id}','network:platform:vlan-network:delete'),
    ('POST','/api/v1/admin/networks/egress-gateways','network:platform:egress-gateway:create'),
    ('GET','/api/v1/admin/networks/egress-gateways/{gateway_id}','network:platform:egress-gateway:get'),
    ('GET','/api/v1/admin/networks/egress-gateways','network:platform:egress-gateway:list'),
    ('DELETE','/api/v1/admin/networks/egress-gateways/{gateway_id}','network:platform:egress-gateway:delete'),
    ('POST','/api/v1/admin/networks/public-address-pools','network:platform:public-pool:create'),
    ('GET','/api/v1/admin/networks/public-address-pools/{pool_id}','network:platform:public-pool:get'),
    ('GET','/api/v1/admin/networks/public-address-pools','network:platform:public-pool:list'),
    ('DELETE','/api/v1/admin/networks/public-address-pools/{pool_id}','network:platform:public-pool:delete'),
    ('POST','/api/v1/admin/networks/public-address-pools/{pool_id}/verification','network:platform:public-pool:verify'),
    ('PATCH','/api/v1/admin/networks/public-address-pools/{pool_id}/allocation','network:platform:public-pool:allocate'),
    ('POST','/api/v1/admin/networks/public-address-pools/{pool_id}/default','network:platform:public-pool:default'),
    ('POST','/api/v1/admin/networks/intranet-address-pools','network:platform:intranet-pool:create'),
    ('GET','/api/v1/admin/networks/intranet-address-pools/{pool_id}','network:platform:intranet-pool:get'),
    ('GET','/api/v1/admin/networks/intranet-address-pools','network:platform:intranet-pool:list'),
    ('DELETE','/api/v1/admin/networks/intranet-address-pools/{pool_id}','network:platform:intranet-pool:delete'),
    ('POST','/api/v1/admin/networks/intranet-address-pools/{pool_id}/verification','network:platform:intranet-pool:verify'),
    ('PATCH','/api/v1/admin/networks/intranet-address-pools/{pool_id}/allocation','network:platform:intranet-pool:allocate'),
    ('POST','/api/v1/admin/networks/intranet-address-pools/{pool_id}/default','network:platform:intranet-pool:default'),
    ('GET','/api/v1/admin/networks/capabilities','network:platform:capability:get'),
    ('GET','/api/v1/admin/networks/operations/{operation_id}','network:platform:operation:get')
  ) AS contracts(method,path,code) LOOP
    SELECT count(*),min(a.id) INTO route_count,selected_api_id FROM sys_apis a
      WHERE a.path=r.path AND a.method=r.method AND a.deleted_at IS NULL
      AND a.business_module='NETWORK' AND a.status='ON';
    IF route_count<>1 THEN RAISE EXCEPTION 'Network catalog must contain one active route for % %',r.method,r.path; END IF;
    INSERT INTO sys_permissions(name,code,status) VALUES(r.code,r.code,'ON') ON CONFLICT(code) DO NOTHING;
    SELECT p.id INTO STRICT selected_permission_id FROM sys_permissions p WHERE p.code=r.code AND p.deleted_at IS NULL;
    IF EXISTS(SELECT 1 FROM sys_permission_apis pa WHERE pa.api_id=selected_api_id AND pa.permission_id<>selected_permission_id) THEN
      RAISE EXCEPTION 'Network route has another permission; review existing grants before import';
    END IF;
    INSERT INTO sys_permission_apis(permission_id,api_id) VALUES(selected_permission_id,selected_api_id) ON CONFLICT DO NOTHING;
  END LOOP;
END $$;
