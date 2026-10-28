# GPU monitoring phase 1

The accepted functional specification is [v1.1](../plans/gpu-monitoring-backend-phase1-plan.md), SHA256 `d599fb35d6ec179bc1f9b547f890465f36f7416a2e76a329cfa957aa42b451a7`. This contract describes the read interfaces; deployment evidence and actual history coverage are separate acceptance facts.

## Public reads

All methods require a user JWT, current role permission and the registered module/plan gate. API keys are not enabled by this slice. Existing roles, users, permission status, plans and quota policies are not changed by API registration.

| Method and path | Capability and scope |
|---|---|
| GET `/api/v1/admin/gpu/devices` | `accelerator:device:list`, platform; `clusterId`, optional `modelKey`, `page`, `pageSize` |
| GET `/api/v1/admin/gpu/devices/{device_id}/metrics/latest` | `accelerator:device:list`, platform; `clusterId`, optional `metricCodes` |
| POST `/api/v1/admin/gpu/devices/{device_id}/metrics/query` | Same capability; one ACC-registered device |
| GET/POST `/api/v1/admin/gpu/metrics/latest` or `metrics/query` | Same capability; controlled cluster/model group; utilization requires one model |
| GET/POST `/api/v1/admin/gpu/tenants/{tenant_id}/metrics/latest` or `metrics/query` | `tenant:quota-read`, platform tenant aggregate; no resource detail browsing |
| GET/POST `/api/v1/gpu/metrics/latest` or `metrics/query` | `accelerator:usage:list`, authenticated tenant aggregate |
| GET/POST `/api/v1/gpu/resources/{resource_kind}/{resource_id}/metrics/latest` or `metrics/query` | `accelerator:usage:get`, authenticated tenant; also current owner-type resource-read permission |
| GET `/api/v1/admin/tenants/quota-account-summaries` | `tenant:quota-read`, platform; `page`, `pageSize`, optional `tenantId`, `quotaCode` |
| GET `/api/v1/admin/gpu/tenants` | `tenant:quota-read`, platform; `page`, `pageSize` (default/max6), optional `tenantId`; current allocations, latest memory and current quota |

For POST query requests the body is `{ "data": { "window": "24h", "metricCodes": [...] } }`. Use `start` and `end` RFC3339 UTC timestamps instead of `window` for a controlled custom range. `clusterId` and `modelKey` are available only on physical platform reads. Latest requests use query parameters. Unknown/duplicate selector fields, public tenant/owner/Pod selectors and ambiguous JSON are rejected. The route's path identities cannot be supplied through the request body.

Default range is 24h; presets are 1h, 6h, 24h, 7d and 30d. Intervals are `[start,end)` within the last 30 days. Obs chooses display buckets independently of the 30s resource/15s physical base grid. Response values are nullable; complete-only mean/peak, partial known sums, original sample-time bounds and quality/coverage remain distinct. Optional event/state metrics expose snapshot semantics and Xid process-start boundaries, never an invented 30-day event total.

Resource types enabled for details are `container` (owner `ani-resource-service`) and `inference` (owner `ani-inference`). Original create-operation identity comes from the Gov ledger after current resource read/owner checks. Tenant aggregates use ACC history, including deleted resources and eligible other owners; they do not enumerate only current business resources. ModelDev's future adapter uses execution ID and original create operation, with multiple ACC scopes. ModelDev details remain explicitly unsupported in this phase.

Tenant replies contain business target identity and aggregate quality only. Pod, node, GPU UUID, binding IDs and internal scope watermarks are excluded from the public projection.

## Service calls

| Caller -> receiver | Registered RPC / authority | Deadline |
|---|---|---|
| Gov -> ACC | `AcceleratorAdminService/ListDevices`, existing platform `AdminRead` | Existing bounded ACC timeout |
| Gov -> ACC | `AcceleratorAdminService/ListAdminGpuMetricScopes`, platform `AdminRead`; optional selected tenant | Existing bounded ACC timeout |
| Gov -> ACC | `AcceleratorUsageService/ListTenantGpuMetricScopes`, original `TenantContext` | Existing bounded ACC timeout |
| Gov -> Obs | `/observability.gpu.v1.GpuMonitoringService/QueryGpuLatest`, `QueryGpuRange` | 32s Gov call, 30s Obs batch, earlier parent honored |

TLS 1.3, unique exact DNS client `ani-governance`, exact registered server DNS, method allowlists and rebuilt metadata apply. GPU Obs calls use `x-ani-context=platform` with no tenant header, or `x-ani-context=tenant` with the authenticated mapped tenant UUID. The target tenant remains explicit for an administrator aggregate. Both contexts carry the real authenticated actor and a request UUID. Legacy CPU/memory/network/training RPC validation, timeouts and source rules remain unchanged. GPU HTTP gets a 35s bound.

ACC scopes include original UsageRef, node UID/name, vendor GPU UUID, namespace/Pod/container/vdevice selectors, evidence and history applicability. Candidate conflicts are resolved before tenant/resource filtering. Incomplete candidates remain unknown; cross-resource source reuse is not resolved by elapsed time or retiring old business state. New history starts with actual persisted evidence; pre-index history is not backfilled. Current unknown ownership does not erase earlier trusted periods.

Scope pagination uses a fixed snapshot identifier, cycle detection and a 64-page bound. Independent GPU processing limits are 4 metrics, 12 curves, 3000 buckets/curve, 12000 total buckets, 128 unique sources, 12M processed values, <=6h chunks, 4 Prom requests/query, 16 global executing requests, 2 queries/tenant, 10s Prom deadline, 16MiB upstream and 8MiB public response. Exceeding any limit fails explicitly.

## Quota snapshot and operations

Tenant count/page, quota catalogue, current plan policies and authoritative accounts share one dedicated readonly Repeatable Read transaction with batched queries. Existing admission/refund transactions are unchanged. `configured` comes from policy presence; configured zero and missing policy are distinct. Decimal string `limit`, `occupied`, `available=max(limit-occupied,0)`, `overLimit=occupied>limit` and actual `enforcement` preserve existing numeric behavior. No ACC-derived accounting or historical quota is introduced.

The quota-account summary response also includes `summary`: `configuredTenantCount` counts tenants with at least one selected quota policy; `accounts` contains `quotaCode`, `unit`, `configuredTenantCount`, `limit`, `occupied` and `available` for each selected quota definition. All aggregate numbers are decimal strings, including sums larger than uint64. These totals use the same `tenantId`/`quotaCode` scope and exclude soft-deleted tenants, independently of `page`/`pageSize` (including an empty out-of-range page). Limits of a shared plan are counted once per tenant. Occupation includes accounts without a configured policy. Available is summed after applying `max(limit-occupied,0)` to each tenant. A single PostgreSQL aggregate query runs in the existing read-only snapshot; it does not load all tenant projections into application memory. This is quota accounting, not actual device usage or allocatable ACC capacity.

The combined administrator tenant page uses the same quota snapshot, including its internal trusted tenant mapping. After the transaction ends it reads current ACC bindings once and latest usage with two workers. Binding commitments (MiB/count), observed runtime memory/limit (bytes) and quota accounts remain distinct. Unknown allocations keep totals null and expose the known part. The page shares the 128-source/12-curve/8MiB GPU budget and a 30s deadline; budget and timeout failures fail the page explicitly. It never expands historical curves per row.

API catalogue synchronization remains explicit: first the admin CLI dry-run, then synchronization, then [read-API associations](../../scripts/ops/sql/register-gpu-monitoring-read-apis.sql), then the documented policy refresh. The SQL requires existing enabled read permissions and adds no role grants. Source registration alone does not establish live authorization.

Obs collection manifests in `deploy/gpu/` use one controlled GPU profile per source, physical NVML v1.16.0 and the existing builtin HAMi monitor. They do not deploy another feedback writer. Node UID relabels in the test-environment manifests must be refreshed from verified ACC/Kubernetes identities after node replacement. Old profiles are not used to infer a new node's history. Prometheus retention/capacity is an external dependency; this slice does not modify storage or retention settings.
