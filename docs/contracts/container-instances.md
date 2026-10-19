# 容器实例 HTTP 合同

本合同对应 ANI 容器实例联合批次。消息与 RPC 以 `api/protos/instance/service/v1/instance.proto` 和 admin BFF Proto 为准；源码能力与现场验收分别确认。前端只调用 Governance，不直接调用 Resource、ACC 或 Kubernetes。

## 身份与受理

租户入口使用已认证用户的当前 INSTANCE 套餐与动作权限；身份和 Resource 租户由服务器映射。平台入口要求平台身份（tenant=0）及对应 permission，并经过现有 Casbin。租户管理员不能以平台权限码调用平台入口。请求不接受 tenant、namespace、actor、owner、GPU 执行参数、charges 或 FrozenSpec。

创建 `POST /api/v1/instances` 返回 202：

```json
{"data":{"idempotencyKey":"10000000-0000-4000-8000-000000000001","name":"工作区","imageId":"实际登记镜像ID","imageScope":"platform","presetId":"20000000-0000-4000-8000-000000000001","scheduling":{"pod_placement":"SPREAD_PREFERRED","priority_class_name":"batch-low"}}}
```

`scheduling` 可以省略；出现时至少含一项。仅允许 `pod_placement` 为 `NONE`、`COLOCATE_PREFERRED`、`SPREAD_PREFERRED`，以及非空 `priority_class_name`。拒绝 null、空对象、字段 null、数值枚举、UNSPECIFIED、未知字段。优先级引用须获本租户授权、在唯一目标集群存在且受运行路径支持。显式值覆盖默认值；没有任何来源的 placement 为 NONE。软亲和仅作用本实例，不增加副本。

可信规格决定 `instance_kind=container` 与 `compute_type=cpu|gpu`。CPU 不调用 ACC，不产生 GPU 执行参数或 GPU charges。GPU 创建由 Resource 解析受管业务规格，ACC 返回 `GpuExecution`、配额向量和 `GpuDisplay`。Gov 将执行参数和展示信息冻结到同一份 `CreateSpec`；`GpuExecution` 只有 `scheduler_name/queue_name/runtime_class_name/node_selector/pod_annotations/resources/execution_digest`，`GpuDisplay` 只有 `profile_id/profile_version/profile_name/model_key/mode`。`resources` 的 GPU 键固定为 `volcano.sh/vgpu-number`、`volcano.sh/vgpu-memory`、`volcano.sh/vgpu-cores`，其中 memory 直接使用 MiB 整数值。Gov→Resource 创建合同只有命令上下文和这份扁平规格，不发送完整 GPU 计划、重复请求或 charges；原 GPU 请求、配额向量和 charge 原账由 Gov 保存。

原公开请求哈希不含动态解析的规格、默认值、执行参数或配额向量；当前授权通过后先找原幂等记录。已受理重试返回原 operation/resource，规格编辑、禁用或删除不改变原冻结参数，也不重新解析当前目录。共享按原账的 MiB 占用 `gpu.shared_memory_mib`，整卡按设备数占用 `gpu.physical.count`。最终删除须由 Resource 持久确认资源释放及不会迟到创建，再以 `release_event_id/create_operation_id/resource_id/reason` 上报；Gov 校验可信 owner 和原创建关系，在同一事务中按原账一次性全退。重复事件不重复退额，同事件内容冲突拒绝，同资源换事件也不能产生第二次退款。stop、重启及单独的 Usage ENDED 不触发退额。

响应为 `{operationId,resourceId,replayed,result:"ACCEPTED"}`。ACCEPTED 表示持久受理，运行情况通过 operation 和实例状态查询。启停与删除沿用 `{data:{idempotencyKey,expectedVersion}}` 及路径实例 ID；终端沿用既有独立接口。实例没有变配、CPU/GPU 互转或历史规格版本入口。

## 规格与列表

| 方法与路径 | 输入与结果 |
|---|---|
| GET `/api/v1/instance-presets` | page/pageSize，name/code/compute_type/gpu_mode；`{items,total,gpu_catalog_status,gpu_catalog_reason}` |
| GET `/api/v1/instances` | page/pageSize，name/instance_kind/compute_type/observedState；`{items,total}` |
| GET `/api/v1/instances/{instance_id}` | 冻结静态详情，与列表使用同一安全投影 |
| GET `/api/v1/instance-operations/{operation_id}` | 受理响应的 operationId；返回 id/instanceId/action/intentVersion/state/reason |
| POST `/api/v1/instances/{instance_id}:start`、`:stop` | `{data:{idempotencyKey,expectedVersion}}`，202 + 受理响应 |
| DELETE `/api/v1/instances/{instance_id}` | 同启停请求，202 + 受理响应；继续观察清理标志 |
| GET `/api/v1/admin/compute-presets` | 同规格过滤，另有 enabled；`{items,total}` |
| GET `/api/v1/admin/compute-presets/{id}` | 单个完整规格 |
| POST `/api/v1/admin/compute-presets` | `{data:{...}}`，201 + 规格 |
| PATCH `/api/v1/admin/compute-presets/{id}` | `{data:{...},update_mask:"name,cpuMillis"}`，200 + 规格 |
| POST `/api/v1/admin/compute-presets/{id}:set-enabled` | `{data:{enabled:false}}`，200 + 规格 |
| DELETE `/api/v1/admin/compute-presets/{id}` | 无正文，204 |

分页独立默认 page=1/pageSize=20，pageSize 1..100；未知、重复、零和非规范正整数参数拒绝。授权、可见性和过滤先于最终计数及分页。HTTP 的 int64/uint64 按 ProtoJSON 为十进制字符串。

创建示例中的镜像、规格与 PriorityClass 必须替换为真实可见资源；不需要调度意图时省略整个 scheduling。幂等键为 UUID，每个新动作使用新键，网络重试沿用同一键和原始正文。expectedVersion 使用最近静态详情的字符串版本。operation 的 QUEUED/执行中/完成或失败与 Pod 运行状态分别观察；删除须继续确认 computeCleaned、workspaceCleaned 和 quotaReturned。

规格字段为 id/code/name/description/compute_type/cpu_millis/memory_mib/enabled/gpu/created_at/updated_at/selectable/unavailable_reason。CPU/内存为正整数，无浮点规格。code 创建后不可改。GPU 公开输入只有 profile_id/profile_version/devices_per_instance；服务器从 ACC 可信目录核对完整引用。整卡数量为正整数，并满足 `1 ≤ devices_per_instance ≤ Profile.max_devices_per_replica`，同一个 Profile 可选择其上限内的数量；不硬编码 2 卡或逐数量分流。共享保持单卡。修改不更新已有实例，不级联删除 Profile 或实例。`selectable/unavailable_reason` 是已授权候选的当前可选性提示，不能替代创建时的正式解析，也不能改变已受理请求的重放结果。

PATCH 使用 FieldMask 的 JSON 字符串形式，路径按 ProtoJSON lowerCamel 转换；HTTP DTO 字段仍以 Proto 的 json_name 为准。只修改 mask 中的字段；清空 GPU 必须同时给 `gpu:null` 和 `gpu` mask。enabled 必须显式存在。CPU 转换须清掉 GPU，GPU 转换须提供合法 GPU 引用；行锁下合并并原子校验，不先在 Gov 读整行后覆盖。

静态详情新增 instance_kind、compute_type、cpu、memory、createdAt、workspaceSize、preset_name、preset_code、gpu_summary 和实际 scheduling。`gpu_summary` 仅含 `profile_id/profile_version/mode/model_key/devices_per_instance/memory_mib_per_device/memory_mib_total/core_limit_percent`。共享和整卡都返回具体 MiB 值，整卡采用冻结的单卡完整显存值；两个显存字段均为 int64，HTTP 按 ProtoJSON 输出十进制字符串。旧 `shared_memory_mib_per_device/shared_memory_mib_total` 字段已删除且保留编号，不接受旧字段。CPU 的 gpu_summary 为 null。规格候选中的 GPU 目录不可用或无权限时保留 CPU 候选并明确状态，不把不完整 GPU 集合当完整目录。

## 平台调度管理

| 方法与路径 | 合同与权限 |
|---|---|
| GET `/api/v1/admin/scheduler-config` | `{config,resource_version}`；`instance:scheduler:read` |
| PUT `/api/v1/admin/scheduler-config` | `{data:{config,expected_resource_version}}`；`instance:scheduler:update` |
| GET `/api/v1/admin/instance-scheduling-settings` | `{defaults,priority_class_allowlists}`；`instance:scheduling:read` |
| PUT `/api/v1/admin/instance-scheduling-settings` | `{data:{defaults,priority_class_allowlists}}`；同时要求 `instance:scheduling:defaults:update` 和 `instance:scheduling:allowlists:update` |

config 为登记 ConfigMap 固定 key 的原生 YAML 字符串。最终更新使用编辑时的不透明 resourceVersion；冲突返回 409，客户端重新读取并由管理员处理冲突。其他键、metadata、actions 顺序、tiers 和未知项保留。ACC 同一版本归一化算法只豁免已审核的评分参数，GPU 模式、编码、单位和未知内容受保护。环境基线尚未对齐时写入口返回 503，读取保持可用。保存只确认 ConfigMap 更新；不表示调度器已经加载。

settings 是当前唯一集群的一条完整配置，PUT 原子替换 defaults 和名单；不接受租户覆盖层。两个字段均须出现，`defaults:{}` 和 `priority_class_allowlists:[]` 合法。defaults 只含可选 `pod_placement`、`priority_class_name`；名单形如 `[{"tenant_id":1,"priority_class_names":["batch-low"]}]`，tenant_id 是已存在的 Gov 租户 ID。重复租户或名称、空或非法 PriorityClass 名称、未知字段均拒绝。空配置读取为 `{defaults:{},priority_class_allowlists:[]}`。

创建在查原始幂等记录后读取一次配置；显式字段覆盖该快照的默认字段，placement 无来源时使用 NONE。实际采用的 PriorityClass 必须在本租户名单内，并通过可信 Resource 租户映射、目标集群存在性和 GPU 路径兼容检查；没有引用时不启动 PriorityClass 检查。平台设置只授权引用，不创建 PriorityClass，不修改已冻结的 GPU 执行参数或现有 PodGroup。既有实例和受理重试保持冻结值；配置读取失败返回 503，不静默采用空默认。存储复用受保护的全局配置记录，通用配置 CRUD 不公开或修改此记录。

## 诊断与 SSE

`GET /api/v1/instances/{instance_id}/runtime` 返回 instance_id/version/observedAt/pod/statefulSet；对象缺失为 null。pod 只给安全状态、环境变量名称及来源类别、挂载种类与容器内路径，不给变量值、Secret 内容或宿主机路径。

`GET /api/v1/instances/{instance_id}/events` 返回 podUid/observedAt/events/truncated；事件只属于当前核对过 owner 链的 Pod UID。未知时间和次数为 null。最多读取 1000 条（每页 200），截断集合不宣称全局最新。

`GET /api/v1/instances/{instance_id}/logs?tailLines=100&follow=true` 只读当前 workspace stdout/stderr。tailLines=1..1000、follow=true|false，均单值且可独立省略；默认 100/true。近期日志 follow=false 仍返回 SSE。目标及上游 READY 通过前返回普通 HTTP 错误；头后只发 SSE error/end，不再写 HTTP JSON 错误。

```javascript
const abort = new AbortController();
const response = await fetch(`/api/v1/instances/${instanceId}/logs?tailLines=100&follow=true`, {
  headers: { Authorization: `Bearer ${accessToken}` },
  signal: abort.signal,
});
if (!response.ok) throw await response.json();
const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
let pending = "";
try {
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    pending += value;
    for (;;) {
      const boundary = pending.indexOf("\n\n");
      if (boundary < 0) break;
      const frame = pending.slice(0, boundary);
      pending = pending.slice(boundary + 2);
      if (frame.startsWith(":")) continue; // heartbeat
      let event = "message";
      const data = [];
      for (const line of frame.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
      }
      if (data.length) onInstanceLogEvent(event, JSON.parse(data.join("\n")));
    }
  }
} finally {
  abort.abort();
  reader.releaseLock();
}
// 页面关闭/切换实例时也调用 abort.abort()。
```

`log` 数据为 `{text:"..."}`，`end` 为 `{reason:"EOF|TIME_LIMIT|POD_CHANGED"}`，`error` 为稳定脱敏 reason。没有事件 ID 或断点恢复。Bearer 不放 URL。连接有 no-store、flush、关闭代理缓冲和 15 秒注释心跳。默认每帧 16KiB、连接缓冲最多 256KiB、写超时 10 秒、寿命 10 分钟，进程/租户/实例上限 100/10/3。慢客户端终止，断连取消传递到上游，不切换到新 Pod。

非法参数 400，权限 403，不存在 404，幂等/版本冲突 409，日志连接上限 429，依赖不可用 503；错误体沿现有 code/reason/message/metadata。运行、GPU 硬件、浏览器及现场基线验收以本批执行证据为准。
