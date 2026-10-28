-- Narrow v2.0 registration. Run with psql -X -v ON_ERROR_STOP=1 -f this-file.
-- Required: -v tenant_code=tenant-a -v role_code=tenant:manager
--           -v expected_tenant_id=2 -v expected_plan_id=3 -v expected_role_id=5
-- Default: dry-run. Add -v apply=true only after reviewing the dry-run/backup.
-- Before this script, review `admin sync-apis --dry-run` from the exact merged
-- dev binary. Do not run full register-instance-permissions or SyncPermissions.
-- After apply, refresh all Gov policy instances as docs/deployment.md requires.
--
-- Sources: sql/bootstrap/catalog.go (Catalog/SyncAPIs), the four corresponding
-- operations in app/admin/service/cmd/server/assets/openapi.yaml, and the Ent
-- schemas api/permission/permission_api/role_permission/plan_module/tenant/role.
-- Catalog has empty operation/tag descriptions for these four APIs. Physical
-- API uniqueness is (module,path,method,scope), but SyncAPIs identifies routes by
-- (method,path), so ambiguity across modules/scopes/deleted rows is rejected.
-- Global permission code and (permission_id,api_id) are unique; role grants have
-- both (role_id,permission_id) and (tenant_id,role_id,permission_id) uniqueness.
-- Plan-module has no (plan_id,module) unique index; the table lock protects gaps.
-- Existing IDs, statuses, display names/groups, other API mappings and grants
-- are retained. Soft-deleted or differently identified target rows fail closed.
-- New rows use actual schema defaults; no tenant/role/plan/quota row is changed.
-- Dry-run does not INSERT or allocate sequence values. Apply aligns only the
-- identity sequences of tables needing inserts, using SyncAPIs' MAX/nextval
-- rule. PostgreSQL sequence allocation in apply is not undone by rollback.

\set ON_ERROR_STOP on
\if :{?tenant_code}
\else
  \set tenant_code ''
\endif
\if :{?role_code}
\else
  \set role_code ''
\endif
\if :{?expected_tenant_id}
\else
  \set expected_tenant_id ''
\endif
\if :{?expected_plan_id}
\else
  \set expected_plan_id ''
\endif
\if :{?expected_role_id}
\else
  \set expected_role_id ''
\endif
\if :{?apply}
\else
  \set apply false
\endif

BEGIN;
SET LOCAL search_path = pg_catalog, public;
SET LOCAL lock_timeout = '10s';
SET LOCAL statement_timeout = '30s';
SELECT set_config('ani.restart_pvc.tenant_code', :'tenant_code', true),
       set_config('ani.restart_pvc.role_code', :'role_code', true),
       set_config('ani.restart_pvc.expected_tenant_id', :'expected_tenant_id', true),
       set_config('ani.restart_pvc.expected_plan_id', :'expected_plan_id', true),
       set_config('ani.restart_pvc.expected_role_id', :'expected_role_id', true),
       set_config('ani.restart_pvc.apply', :'apply', true);

DO $registration$
DECLARE
  tenant_row public.sys_tenants%ROWTYPE;
  plan_row public.sys_plans%ROWTYPE;
  role_row public.sys_roles%ROWTYPE;
  api_row public.sys_apis%ROWTYPE;
  permission_row public.sys_permissions%ROWTYPE;
  mapping_row public.sys_permission_apis%ROWTYPE;
  grant_row public.sys_role_permissions%ROWTYPE;
  module_row public.sys_plan_modules%ROWTYPE;
  apply_text text := current_setting('ani.restart_pvc.apply');
  do_apply boolean;
  target_tenant bigint := 2;
  target_plan bigint := 3;
  target_role bigint := 5;
  target_resource_tenant text := '64c5c35a-f554-432c-b4bc-7b4f36f08420';
  permission_ids jsonb := '{}';
  api_ids jsonb := '{}';
  missing jsonb := '{"sys_apis":0,"sys_permissions":0,"sys_permission_apis":0,"sys_role_permissions":0,"sys_plan_modules":0}';
  routes jsonb := '[
    {"method":"POST","path":"/api/v1/instances/{data.instance_id}:restart","operation":"InstanceService_RestartInstance","code":"instance:restart"},
    {"method":"POST","path":"/api/v1/instances/{data.instance_id}:attach-pvc","operation":"InstanceService_AttachInstancePVC","code":"instance:pvc:attach"},
    {"method":"POST","path":"/api/v1/instances/{data.instance_id}:detach-pvc","operation":"InstanceService_DetachInstancePVC","code":"instance:pvc:detach"},
    {"method":"GET","path":"/api/v1/instances/{instance_id}/mountable-pvcs","operation":"InstanceService_ListInstanceMountablePVCs","code":"instance:pvc:attach"}
  ]';
  codes text[] := ARRAY['instance:restart','instance:pvc:attach','instance:pvc:detach'];
  route record;
  permission_code text;
  route_key text;
  aid bigint;
  pid bigint;
  row_count bigint;
  target_table text;
  maximum_id bigint;
  identity_sequence regclass;
BEGIN
  IF current_setting('ani.restart_pvc.tenant_code') IS DISTINCT FROM 'tenant-a'
     OR current_setting('ani.restart_pvc.role_code') IS DISTINCT FROM 'tenant:manager'
     OR current_setting('ani.restart_pvc.expected_tenant_id') IS DISTINCT FROM '2'
     OR current_setting('ani.restart_pvc.expected_plan_id') IS DISTINCT FROM '3'
     OR current_setting('ani.restart_pvc.expected_role_id') IS DISTINCT FROM '5' THEN
    RAISE EXCEPTION 'explicit tenant_code=tenant-a, role_code=tenant:manager, expected_tenant_id=2, expected_plan_id=3 and expected_role_id=5 are required';
  END IF;
  IF apply_text NOT IN ('false','true') THEN
    RAISE EXCEPTION 'apply must be exactly false (default dry-run) or true';
  END IF;
  do_apply := apply_text = 'true';

  SELECT * INTO STRICT tenant_row FROM public.sys_tenants WHERE id=target_tenant FOR SHARE;
  IF tenant_row.code IS DISTINCT FROM 'tenant-a'
     OR tenant_row.resource_tenant_id IS DISTINCT FROM target_resource_tenant
     OR tenant_row.plan_id IS DISTINCT FROM target_plan
     OR tenant_row.deleted_at IS NOT NULL
     OR tenant_row.status IS NULL OR tenant_row.status NOT IN ('ON','OFF') THEN
    RAISE EXCEPTION 'tenant identity drift: expected tenant-a/2, reviewed Resource UUID and subscribed plan 3';
  END IF;
  SELECT * INTO STRICT plan_row FROM public.sys_plans WHERE id=target_plan FOR SHARE;
  IF plan_row.deleted_at IS NOT NULL THEN
    RAISE EXCEPTION 'plan 3 is deleted; refusing to change entitlement';
  END IF;
  SELECT * INTO STRICT role_row FROM public.sys_roles WHERE id=target_role FOR SHARE;
  IF role_row.tenant_id IS DISTINCT FROM target_tenant
     OR role_row.code IS DISTINCT FROM 'tenant:manager'
     OR role_row.type IS DISTINCT FROM 'TENANT'
     OR role_row.deleted_at IS NOT NULL
     OR role_row.status IS NULL OR role_row.status NOT IN ('ON','OFF') THEN
    RAISE EXCEPTION 'role identity drift: expected tenant:manager/5 owned by tenant-a/2';
  END IF;

  -- Serializes catalog/grant writes (including gaps); ordinary reads continue.
  -- All row checks finish before the first insert or sequence allocation.
  LOCK TABLE public.sys_apis, public.sys_permissions, public.sys_permission_apis,
             public.sys_role_permissions, public.sys_plan_modules IN SHARE ROW EXCLUSIVE MODE;

  FOREACH permission_code IN ARRAY codes LOOP
    SELECT count(*) INTO row_count FROM public.sys_permissions WHERE code=permission_code;
    IF row_count > 1 THEN
      RAISE EXCEPTION 'ambiguous permission code %', permission_code;
    ELSIF row_count = 1 THEN
      SELECT * INTO STRICT permission_row FROM public.sys_permissions WHERE code=permission_code;
      IF permission_row.deleted_at IS NOT NULL OR nullif(permission_row.name,'') IS NULL
         OR permission_row.status IS NULL OR permission_row.status NOT IN ('ON','OFF') THEN
        RAISE EXCEPTION 'permission identity/state drift for %; existing row will not be replaced', permission_code;
      END IF;
      pid := permission_row.id;
      RAISE NOTICE 'KEEP permission % id=% status=% (name/group unchanged)', permission_code,pid,permission_row.status;
    ELSE
      pid := NULL;
      missing := jsonb_set(missing,'{sys_permissions}',to_jsonb((missing->>'sys_permissions')::int+1));
      RAISE NOTICE 'ADD permission %',permission_code;
    END IF;
    permission_ids := permission_ids || jsonb_build_object(permission_code,pid);

    IF pid IS NOT NULL THEN
      SELECT count(*) INTO row_count FROM public.sys_role_permissions WHERE role_id=target_role AND permission_id=pid;
      IF row_count > 1 THEN
        RAISE EXCEPTION 'ambiguous role/permission mapping for %',permission_code;
      ELSIF row_count = 1 THEN
        SELECT * INTO STRICT grant_row FROM public.sys_role_permissions WHERE role_id=target_role AND permission_id=pid;
        IF grant_row.tenant_id IS DISTINCT FROM target_tenant OR grant_row.deleted_at IS NOT NULL
           OR grant_row.effect IS DISTINCT FROM 'ALLOW' OR grant_row.status IS NULL OR grant_row.status NOT IN ('ON','OFF') THEN
          RAISE EXCEPTION 'role grant identity/effect drift for %; existing grant will not be overwritten',permission_code;
        END IF;
        RAISE NOTICE 'KEEP role grant % id=% status=%',permission_code,grant_row.id,grant_row.status;
      ELSE
        missing := jsonb_set(missing,'{sys_role_permissions}',to_jsonb((missing->>'sys_role_permissions')::int+1));
      END IF;
    ELSE
      missing := jsonb_set(missing,'{sys_role_permissions}',to_jsonb((missing->>'sys_role_permissions')::int+1));
    END IF;
  END LOOP;

  FOR route IN SELECT * FROM jsonb_to_recordset(routes) AS r(method text,path text,operation text,code text) LOOP
    route_key := route.method||' '||route.path;
    SELECT count(*) INTO row_count FROM public.sys_apis WHERE method=route.method AND path=route.path;
    IF row_count > 1 THEN
      RAISE EXCEPTION 'ambiguous API % across module/scope/deleted rows',route_key;
    ELSIF row_count = 1 THEN
      SELECT * INTO STRICT api_row FROM public.sys_apis WHERE method=route.method AND path=route.path;
      IF api_row.module IS DISTINCT FROM 'InstanceService' OR api_row.scope IS DISTINCT FROM 'ADMIN'
         OR api_row.operation IS DISTINCT FROM route.operation OR api_row.business_module IS DISTINCT FROM 'INSTANCE'
         OR coalesce(api_row.module_description,'') <> '' OR coalesce(api_row.description,'') <> ''
         OR api_row.deleted_at IS NOT NULL OR api_row.status IS NULL OR api_row.status NOT IN ('ON','OFF') THEN
        RAISE EXCEPTION 'API catalog identity/metadata drift for %; run reviewed catalog reconciliation first',route_key;
      END IF;
      aid := api_row.id;
      RAISE NOTICE 'KEEP API % id=% status=%',route_key,aid,api_row.status;
    ELSE
      aid := NULL;
      missing := jsonb_set(missing,'{sys_apis}',to_jsonb((missing->>'sys_apis')::int+1));
      RAISE NOTICE 'ADD API % operation=%',route_key,route.operation;
    END IF;
    IF EXISTS(SELECT 1 FROM public.sys_apis WHERE operation=route.operation AND (method IS DISTINCT FROM route.method OR path IS DISTINCT FROM route.path)) THEN
      RAISE EXCEPTION 'operation identity drift for %: same operation registered at another method/path',route.operation;
    END IF;
    api_ids := api_ids || jsonb_build_object(route_key,aid);
    pid := (permission_ids->>route.code)::bigint;
    IF aid IS NOT NULL AND pid IS NOT NULL THEN
      SELECT count(*) INTO row_count FROM public.sys_permission_apis WHERE api_id=aid AND permission_id=pid;
      IF row_count > 1 THEN
        RAISE EXCEPTION 'ambiguous permission/API mapping for %',route_key;
      ELSIF row_count = 1 THEN
        SELECT * INTO STRICT mapping_row FROM public.sys_permission_apis WHERE api_id=aid AND permission_id=pid;
        IF mapping_row.deleted_at IS NOT NULL THEN
          RAISE EXCEPTION 'soft-deleted permission/API mapping for %; explicit review required',route_key;
        END IF;
        RAISE NOTICE 'KEEP permission/API % id=%',route_key,mapping_row.id;
      ELSE
        missing := jsonb_set(missing,'{sys_permission_apis}',to_jsonb((missing->>'sys_permission_apis')::int+1));
      END IF;
    ELSE
      missing := jsonb_set(missing,'{sys_permission_apis}',to_jsonb((missing->>'sys_permission_apis')::int+1));
    END IF;
    -- Other permission_ids on this API and other API_ids on this permission
    -- are legitimate independent grants. Never delete, replace, or absorb them.
  END LOOP;

  SELECT count(*) INTO row_count FROM public.sys_plan_modules WHERE plan_id=target_plan AND module='INSTANCE';
  IF row_count > 1 THEN
    RAISE EXCEPTION 'ambiguous plan 3 INSTANCE associations; no automatic deduplication';
  ELSIF row_count = 1 THEN
    SELECT * INTO STRICT module_row FROM public.sys_plan_modules WHERE plan_id=target_plan AND module='INSTANCE';
    IF module_row.deleted_at IS NOT NULL THEN
      RAISE EXCEPTION 'plan 3 INSTANCE association is soft-deleted; no automatic reactivation';
    END IF;
    RAISE NOTICE 'KEEP plan 3 INSTANCE id=%',module_row.id;
  ELSE
    missing := jsonb_set(missing,'{sys_plan_modules}','1'::jsonb);
  END IF;

  RAISE NOTICE 'planned missing rows=% apply=%; existing IDs/status/foreign mappings remain unchanged',missing,do_apply;
  IF NOT do_apply THEN RETURN; END IF;

  -- SyncAPIs handles imported explicit IDs this way. Do it only for the five
  -- allowlisted tables actually needing new rows; an idempotent apply is inert.
  FOREACH target_table IN ARRAY ARRAY['sys_apis','sys_permissions','sys_permission_apis','sys_role_permissions','sys_plan_modules'] LOOP
    IF (missing->>target_table)::int > 0 THEN
      identity_sequence := pg_get_serial_sequence('public.'||target_table,'id')::regclass;
      IF identity_sequence IS NULL THEN RAISE EXCEPTION 'missing expected identity sequence for %',target_table; END IF;
      EXECUTE format('SELECT coalesce(max(id),0) FROM public.%I',target_table) INTO maximum_id;
      PERFORM setval(identity_sequence,GREATEST(maximum_id+1,nextval(identity_sequence)),false);
    END IF;
  END LOOP;

  FOREACH permission_code IN ARRAY codes LOOP
    pid := (permission_ids->>permission_code)::bigint;
    IF pid IS NULL THEN
      INSERT INTO public.sys_permissions(name,code,status,created_at)
        VALUES(permission_code,permission_code,'ON',now()) RETURNING id INTO pid;
      -- Association columns have no FK in the current schema. Do not revive
      -- an orphan grant merely because an identity sequence reused its ID.
      IF EXISTS(SELECT 1 FROM public.sys_permission_apis WHERE permission_id=pid)
         OR EXISTS(SELECT 1 FROM public.sys_role_permissions WHERE permission_id=pid) THEN
        RAISE EXCEPTION 'new permission % id=% collides with a pre-existing orphan association',permission_code,pid;
      END IF;
      permission_ids := permission_ids || jsonb_build_object(permission_code,pid);
    END IF;
  END LOOP;
  FOR route IN SELECT * FROM jsonb_to_recordset(routes) AS r(method text,path text,operation text,code text) LOOP
    route_key := route.method||' '||route.path;
    aid := (api_ids->>route_key)::bigint;
    pid := (permission_ids->>route.code)::bigint;
    IF aid IS NULL THEN
      INSERT INTO public.sys_apis(path,method,operation,module,module_description,description,business_module,scope,status,created_at)
        VALUES(route.path,route.method,route.operation,'InstanceService','','','INSTANCE','ADMIN','ON',now()) RETURNING id INTO aid;
      IF EXISTS(SELECT 1 FROM public.sys_permission_apis WHERE api_id=aid) THEN
        RAISE EXCEPTION 'new API % id=% collides with a pre-existing orphan association',route_key,aid;
      END IF;
    END IF;
    IF NOT EXISTS(SELECT 1 FROM public.sys_permission_apis WHERE permission_id=pid AND api_id=aid) THEN
      INSERT INTO public.sys_permission_apis(permission_id,api_id,created_at) VALUES(pid,aid,now());
    END IF;
  END LOOP;
  FOREACH permission_code IN ARRAY codes LOOP
    pid := (permission_ids->>permission_code)::bigint;
    IF NOT EXISTS(SELECT 1 FROM public.sys_role_permissions WHERE role_id=target_role AND permission_id=pid) THEN
      INSERT INTO public.sys_role_permissions(tenant_id,role_id,permission_id,effect,priority,status,created_at)
        VALUES(target_tenant,target_role,pid,'ALLOW',0,'ON',now());
    END IF;
  END LOOP;
  IF (missing->>'sys_plan_modules')::int = 1 THEN
    INSERT INTO public.sys_plan_modules(plan_id,module,created_at) VALUES(target_plan,'INSTANCE',now());
  END IF;
  RAISE NOTICE 'registered only 4 API identities, 3 permission identities and their missing target associations';
END
$registration$;

SELECT :'apply' = 'true' AS registration_apply \gset
\if :registration_apply
  COMMIT;
  \echo 'Applied narrow Instance Restart/PVC registration; refresh all Gov policy instances before acceptance.'
\else
  ROLLBACK;
  \echo 'DRY RUN rolled back; no catalog/grant inserts or identity sequence allocations were performed.'
\endif
