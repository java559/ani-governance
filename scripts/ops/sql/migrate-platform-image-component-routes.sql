-- 平台镜像（22 条）与平台组件（2 条）路由前缀迁移：/api/v1/platform/** → /api/v1/admin/**。
-- 采用原位 UPDATE path：保留 sys_apis.id，sys_permission_apis 关联与
-- sys_role_permissions 角色授权自动延续，无需重授权。
--
-- 配套脚本（既有环境）执行顺序：
--   1) 本脚本（Gov 停机或接受短暂 403 窗口；先于 sync-apis，与
--      migrate-compute-catalog-route.sql 的先例一致）
--   2) admin sync-apis --dry-run   # 预期本批 24 条不再出现 ADD/UPDATE
--   3) admin sync-apis             # 对本批应为空操作
--   4) register-platform-image-permissions.sql / grant-platform-image-permissions.sql
--      register-platform-component-permissions.sql / register-platform-component-log-permissions.sql
--      # 全部应为幂等空操作（回退清单为空），仅作 fail-closed 校验
--   5) 滚动重启 Governance 实例，刷新内存 Casbin 策略；平台管理员重新登录
--      （JWT 里的 pms 是登录快照）
--
-- 幂等可重复执行：旧路径行缺失且新路径行已存在且归属正确时视为已迁移，直接跳过。
-- 任一守卫不满足即整批回滚（fail closed）：路由缺失/重复/归属不符/新路径已存在/权限关联混入外码。
-- 回滚：见 migrate-platform-image-component-routes.rollback.sql（同样幂等，方向相反）。
--
-- 执行：
--   kubectl -n ani-platform exec -i postgresql-0 -- \
--     psql -U postgres -v ON_ERROR_STOP=1 -d ani_governance -f - < migrate-platform-image-component-routes.sql
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='120s';
LOCK TABLE sys_apis, sys_permissions, sys_permission_apis IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
  route record;
  aid bigint;
  expected_codes text;
BEGIN
  -- 逐条原位改路径。method + old_path 唯一命中、新路径无占用、归属（module +
  -- business_module）与权限关联（只允许挂本路由的权限码）全部校验后才 UPDATE。
  FOR route IN
    SELECT * FROM (VALUES
      -- 平台镜像 11 条（PlatformImageService / IMAGE）
      ('GET',   '/api/v1/platform/images/space',                                  '/api/v1/admin/images/space',                                  'PlatformImageService', 'image:platform:space:get'),
      ('POST',  '/api/v1/platform/images/space:enable',                           '/api/v1/admin/images/space:enable',                           'PlatformImageService', 'image:platform:space:enable'),
      ('GET',   '/api/v1/platform/images/publisher-credential',                   '/api/v1/admin/images/publisher-credential',                   'PlatformImageService', 'image:platform:credential:get'),
      ('POST',  '/api/v1/platform/images/publisher-credential:issue',             '/api/v1/admin/images/publisher-credential:issue',             'PlatformImageService', 'image:platform:credential:issue'),
      ('POST',  '/api/v1/platform/images/publisher-credential:reset',             '/api/v1/admin/images/publisher-credential:reset',             'PlatformImageService', 'image:platform:credential:reset'),
      ('POST',  '/api/v1/platform/images/publisher-credential:disable',           '/api/v1/admin/images/publisher-credential:disable',           'PlatformImageService', 'image:platform:credential:disable'),
      ('GET',   '/api/v1/platform/images/registrations',                          '/api/v1/admin/images/registrations',                          'PlatformImageService', 'image:platform:registration:list'),
      ('POST',  '/api/v1/platform/images/registrations',                          '/api/v1/admin/images/registrations',                          'PlatformImageService', 'image:platform:registration:create'),
      ('GET',   '/api/v1/platform/images/registrations/{image_id}',               '/api/v1/admin/images/registrations/{image_id}',               'PlatformImageService', 'image:platform:registration:get'),
      ('PATCH', '/api/v1/platform/images/registrations/{image_id}',               '/api/v1/admin/images/registrations/{image_id}',               'PlatformImageService', 'image:platform:registration:update'),
      ('POST',  '/api/v1/platform/images/registrations/{image_id}:unregister',    '/api/v1/admin/images/registrations/{image_id}:unregister',    'PlatformImageService', 'image:platform:registration:unregister'),
      -- 代管租户镜像 11 条（AdminTenantImageService / IMAGE）
      ('GET',   '/api/v1/platform/tenants/{target_tenant_id}/images/space',                               '/api/v1/admin/tenants/{target_tenant_id}/images/space',                               'AdminTenantImageService', 'image:platform:tenant-space:get'),
      ('POST',  '/api/v1/platform/tenants/{target_tenant_id}/images/space:enable',                        '/api/v1/admin/tenants/{target_tenant_id}/images/space:enable',                        'AdminTenantImageService', 'image:platform:tenant-space:enable'),
      ('GET',   '/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential',                '/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential',                'AdminTenantImageService', 'image:platform:tenant-credential:get'),
      ('POST',  '/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:issue',          '/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential:issue',          'AdminTenantImageService', 'image:platform:tenant-credential:issue'),
      ('POST',  '/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:reset',          '/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential:reset',          'AdminTenantImageService', 'image:platform:tenant-credential:reset'),
      ('POST',  '/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:disable',        '/api/v1/admin/tenants/{target_tenant_id}/images/publisher-credential:disable',        'AdminTenantImageService', 'image:platform:tenant-credential:disable'),
      ('GET',   '/api/v1/platform/tenants/{target_tenant_id}/images/registrations',                       '/api/v1/admin/tenants/{target_tenant_id}/images/registrations',                       'AdminTenantImageService', 'image:platform:tenant-registration:list'),
      ('POST',  '/api/v1/platform/tenants/{target_tenant_id}/images/registrations',                       '/api/v1/admin/tenants/{target_tenant_id}/images/registrations',                       'AdminTenantImageService', 'image:platform:tenant-registration:create'),
      ('GET',   '/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}',            '/api/v1/admin/tenants/{target_tenant_id}/images/registrations/{image_id}',            'AdminTenantImageService', 'image:platform:tenant-registration:get'),
      ('PATCH', '/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}',            '/api/v1/admin/tenants/{target_tenant_id}/images/registrations/{image_id}',            'AdminTenantImageService', 'image:platform:tenant-registration:update'),
      ('POST',  '/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}:unregister', '/api/v1/admin/tenants/{target_tenant_id}/images/registrations/{image_id}:unregister', 'AdminTenantImageService', 'image:platform:tenant-registration:unregister'),
      -- 平台组件 2 条（PlatformComponentService / SYSTEM）
      ('GET',   '/api/v1/platform/components',                                    '/api/v1/admin/platform/components',                           'PlatformComponentService', 'component:status:read'),
      ('GET',   '/api/v1/platform/components/{component}/logs/stream',            '/api/v1/admin/platform/components/{component}/logs/stream',   'PlatformComponentService', 'component:log:read')
    ) AS t(method, old_path, new_path, module_name, code)
  LOOP
    IF (SELECT count(*) FROM sys_apis
         WHERE path = route.old_path AND method = route.method AND deleted_at IS NULL) > 1 THEN
      RAISE EXCEPTION 'ambiguous old route % %', route.method, route.old_path;
    END IF;

    SELECT id INTO aid FROM sys_apis
     WHERE path = route.old_path AND method = route.method AND deleted_at IS NULL;

    IF aid IS NULL THEN
      -- 已迁移（或新装环境只有新路径）：新路径行必须存在且归属正确，否则报错。
      IF NOT EXISTS(SELECT 1 FROM sys_apis
                     WHERE path = route.new_path AND method = route.method AND deleted_at IS NULL
                       AND module = route.module_name
                       AND ((route.module_name IN ('PlatformImageService','AdminTenantImageService') AND business_module = 'IMAGE')
                         OR (route.module_name = 'PlatformComponentService' AND business_module = 'SYSTEM'))
                       AND scope = 'ADMIN' AND status = 'ON') THEN
        RAISE EXCEPTION 'route % % missing at both old and new path', route.method, route.old_path;
      END IF;
      CONTINUE;
    END IF;

    -- 归属校验：module / business_module 必须与登记清单一致。
    IF NOT EXISTS(SELECT 1 FROM sys_apis WHERE id = aid AND module = route.module_name
                   AND ((route.module_name IN ('PlatformImageService','AdminTenantImageService') AND business_module = 'IMAGE')
                     OR (route.module_name = 'PlatformComponentService' AND business_module = 'SYSTEM'))
                   AND scope = 'ADMIN' AND status = 'ON') THEN
      RAISE EXCEPTION 'route % % has unexpected module/business_module', route.method, route.old_path;
    END IF;

    -- 新路径不得已被占用。
    IF EXISTS(SELECT 1 FROM sys_apis
               WHERE path = route.new_path AND method = route.method AND deleted_at IS NULL) THEN
      RAISE EXCEPTION 'new route % % already exists', route.method, route.new_path;
    END IF;

    -- 权限关联校验：该 API 只允许挂本路由自己的权限码（live 关联）。
    IF EXISTS(SELECT 1 FROM sys_permission_apis pa
               JOIN sys_permissions p ON p.id = pa.permission_id
               WHERE pa.api_id = aid AND pa.deleted_at IS NULL AND p.code <> route.code) THEN
      RAISE EXCEPTION 'route % % has foreign permission link', route.method, route.old_path;
    END IF;

    UPDATE sys_apis SET path = route.new_path, updated_at = NOW() WHERE id = aid;
  END LOOP;

  -- 终态校验：旧前缀下不得再有任何 live 行；新路径 24 行必须齐。
  IF EXISTS(SELECT 1 FROM sys_apis
             WHERE (path LIKE '/api/v1/platform/images/%'
                 OR path LIKE '/api/v1/platform/tenants/{target_tenant_id}/images/%'
                 OR path LIKE '/api/v1/platform/components%')
               AND deleted_at IS NULL) THEN
    RAISE EXCEPTION 'stale /api/v1/platform/** rows remain';
  END IF;
  IF (SELECT count(*) FROM sys_apis
       WHERE (path LIKE '/api/v1/admin/images/%'
           OR path LIKE '/api/v1/admin/tenants/{target_tenant_id}/images/%'
           OR path = '/api/v1/admin/platform/components'
           OR path = '/api/v1/admin/platform/components/{component}/logs/stream')
         AND deleted_at IS NULL) <> 24 THEN
    RAISE EXCEPTION 'expected exactly 24 migrated rows under /api/v1/admin/**';
  END IF;
END $$;
COMMIT;

-- 回显迁移结果
SELECT a.method, a.path, a.module, a.business_module FROM sys_apis a
 WHERE (a.path LIKE '/api/v1/admin/images/%'
     OR a.path LIKE '/api/v1/admin/tenants/{target_tenant_id}/images/%'
     OR a.path LIKE '/api/v1/admin/platform/components%')
   AND a.deleted_at IS NULL
 ORDER BY a.module, a.path, a.method;
SELECT p.code, count(*) AS api_links FROM sys_permission_apis pa
 JOIN sys_permissions p ON p.id = pa.permission_id
 WHERE p.code LIKE 'image:platform:%' OR p.code LIKE 'component:%'
 GROUP BY p.code ORDER BY p.code;
