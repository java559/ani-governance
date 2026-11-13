-- migrate-platform-image-component-routes.sql 的回滚：/api/v1/admin/** → /api/v1/platform/**。
-- 与正向脚本同样的守卫与幂等语义（旧=新路径行缺失且旧路径行已存在且归属正确时视为已回滚）。
-- 前提：新二进制已回退到旧前缀版本，否则旧路径无路由。
--
-- 执行：
--   kubectl -n ani-platform exec -i postgresql-0 -- \
--     psql -U postgres -v ON_ERROR_STOP=1 -d ani_governance -f - < migrate-platform-image-component-routes.rollback.sql
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='120s';
LOCK TABLE sys_apis, sys_permissions, sys_permission_apis IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
  route record;
  aid bigint;
BEGIN
  FOR route IN
    SELECT * FROM (VALUES
      ('GET',   '/api/v1/admin/images/space',                                  '/api/v1/platform/images/space',                                  'PlatformImageService'),
      ('POST',  '/api/v1/admin/images/space:enable',                           '/api/v1/platform/images/space:enable',                           'PlatformImageService'),
      ('GET',   '/api/v1/admin/images/publisher-credential',                   '/api/v1/platform/images/publisher-credential',                   'PlatformImageService'),
      ('POST',  '/api/v1/admin/images/publisher-credential:issue',             '/api/v1/platform/images/publisher-credential:issue',             'PlatformImageService'),
      ('POST',  '/api/v1/admin/images/publisher-credential:reset',             '/api/v1/platform/images/publisher-credential:reset',             'PlatformImageService'),
      ('POST',  '/api/v1/admin/images/publisher-credential:disable',           '/api/v1/platform/images/publisher-credential:disable',           'PlatformImageService'),
      ('GET',   '/api/v1/admin/images/registrations',                          '/api/v1/platform/images/registrations',                          'PlatformImageService'),
      ('POST',  '/api/v1/admin/images/registrations',                          '/api/v1/platform/images/registrations',                          'PlatformImageService'),
      ('GET',   '/api/v1/admin/images/registrations/{image_id}',               '/api/v1/platform/images/registrations/{image_id}',               'PlatformImageService'),
      ('PATCH', '/api/v1/admin/images/registrations/{image_id}',               '/api/v1/platform/images/registrations/{image_id}',               'PlatformImageService'),
      ('POST',  '/api/v1/admin/images/registrations/{image_id}:unregister',    '/api/v1/platform/images/registrations/{image_id}:unregister',    'PlatformImageService'),
      ('GET',   '/api/v1/admin/tenants/{target_tenant_id}/images/space',                               '/api/v1/platform/tenants/{target_tenant_id}/images/space',                               'AdminTenantImageService'),
      ('POST',  '/api/v1/admin/tenants/{target_tenant_id}/images/space:enable',                        '/api/v1/platform/tenants/{target_tenant_id}/images/space:enable',                        'AdminTenantImageService'),
      ('GET',   '/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential',                '/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential',                'AdminTenantImageService'),
      ('POST',  '/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential:issue',          '/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:issue',          'AdminTenantImageService'),
      ('POST',  '/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential:reset',          '/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:reset',          'AdminTenantImageService'),
      ('POST',  '/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential:disable',        '/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:disable',        'AdminTenantImageService'),
      ('GET',   '/api/v1/admin/tenants/{target_tenant_id}/images/registrations',                       '/api/v1/platform/tenants/{target_tenant_id}/images/registrations',                       'AdminTenantImageService'),
      ('POST',  '/api/v1/admin/tenants/{target_tenant_id}/images/registrations',                       '/api/v1/platform/tenants/{target_tenant_id}/images/registrations',                       'AdminTenantImageService'),
      ('GET',   '/api/v1/admin/tenants/{target_tenant_id}/images/registrations/{image_id}',            '/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}',            'AdminTenantImageService'),
      ('PATCH', '/api/v1/admin/tenants/{target_tenant_id}/images/registrations/{image_id}',            '/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}',            'AdminTenantImageService'),
      ('POST',  '/api/v1/admin/tenants/{target_tenant_id}/images/registrations/{image_id}:unregister', '/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}:unregister', 'AdminTenantImageService'),
      ('GET',   '/api/v1/admin/platform/components',                           '/api/v1/platform/components',                                    'PlatformComponentService'),
      ('GET',   '/api/v1/admin/platform/components/{component}/logs/stream',   '/api/v1/platform/components/{component}/logs/stream',            'PlatformComponentService')
    ) AS t(method, cur_path, old_path, module_name)
  LOOP
    IF (SELECT count(*) FROM sys_apis
         WHERE path = route.cur_path AND method = route.method AND deleted_at IS NULL) > 1 THEN
      RAISE EXCEPTION 'ambiguous current route % %', route.method, route.cur_path;
    END IF;

    SELECT id INTO aid FROM sys_apis
     WHERE path = route.cur_path AND method = route.method AND deleted_at IS NULL;

    IF aid IS NULL THEN
      IF NOT EXISTS(SELECT 1 FROM sys_apis
                     WHERE path = route.old_path AND method = route.method AND deleted_at IS NULL
                       AND module = route.module_name
                       AND ((route.module_name IN ('PlatformImageService','AdminTenantImageService') AND business_module = 'IMAGE')
                         OR (route.module_name = 'PlatformComponentService' AND business_module = 'SYSTEM'))) THEN
        RAISE EXCEPTION 'route % % missing at both paths', route.method, route.cur_path;
      END IF;
      CONTINUE;
    END IF;

    IF NOT EXISTS(SELECT 1 FROM sys_apis WHERE id = aid AND module = route.module_name
                   AND ((route.module_name IN ('PlatformImageService','AdminTenantImageService') AND business_module = 'IMAGE')
                     OR (route.module_name = 'PlatformComponentService' AND business_module = 'SYSTEM'))) THEN
      RAISE EXCEPTION 'route % % has unexpected module/business_module', route.method, route.cur_path;
    END IF;

    IF EXISTS(SELECT 1 FROM sys_apis
               WHERE path = route.old_path AND method = route.method AND deleted_at IS NULL) THEN
      RAISE EXCEPTION 'rollback target % % already exists', route.method, route.old_path;
    END IF;

    UPDATE sys_apis SET path = route.old_path, updated_at = NOW() WHERE id = aid;
  END LOOP;
END $$;
COMMIT;
