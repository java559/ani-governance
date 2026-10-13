# 租户 Network / Image 的 AK/SK 请求合同

公开入口的身份有两种：用户 Bearer JWT，或独立 API Key 主体。AK/SK 只开放 [NetworkOperations](../../pkg/middleware/auth/network_policy.go) 的 25 个租户 Network 方法（含 NET-25 `ListVPCCIDRPresets`）与 [ImageOperations](../../pkg/middleware/auth/image_policy.go) 的 11 个方法。平台 Network、Attachment、ImageRuntime 和 Key 管理不开放机器访问。接口、权限及实际验证统一见 [接口登记](../interface-integration-register.md)；OpenAPI 的 security 声明不替客户端计算签名。

## 签名

请求同时提供单值 `X-Access-Key`、`X-Timestamp`、`X-Signature`，不得同时提供 Authorization。时间戳为规范正整数 Unix 秒，服务端接受前后 300 秒窗口；每次请求重新检查 Key 状态、有效期、角色、租户及套餐。机器主体的 user_id 为零，下游 actor 为 `governance:access-key:<key-id>`，不继承创建者或伪造用户。

签名输入是下列七行 UTF-8 字节，以 LF 分隔，最后无换行；空 query、空 body 也保留相应行：

```text
ANI-HMAC-SHA256
<大写 HTTP method>
<规范 path>
<规范 query>
<公开 AK>
<Unix 秒>
<原始 body 的 SHA256 小写十六进制>
```

用 SK 原始 UTF-8 字节计算 HMAC-SHA256，结果为 64 字符小写十六进制。规范 query 是单值参数按键排序后以 Go `url.Values.Encode` 编码（空格为 `+`）；Python 示例使用 `urllib.parse.urlencode(sorted(query.items()))`。签名时使用的 body 字节必须原样发送，不得在签名后重新序列化 JSON。path 不允许编码别名、重复分隔或尾斜杠，ID 使用登记的资源格式。

GET/DELETE 使用空 body；POST/PATCH 使用 JSON 对象及 `application/json`，Network 限 64 KiB。Network query 限 8 KiB，允许字段以对应列表 DTO 和策略为准，单个字段不重复；详情/写入不接受 query。未知字段、重复 JSON 字段、同一字段的两种 JSON 命名、非法 ID/版本/枚举和公网身份字段均拒绝。出站 metadata 从认证主体重新构建。

签名本身不声明一次性请求。写入的重试安全由 Resource 持久幂等合同保证：创建、SNAT 切换和 LB 更新传 `idempotency_key`；需要并发控制的修改另传 `expected_version`。删除沿用 Resource 的幂等生命周期。受理快照和 operation 成功不同，调用方应查询资源及 operation；LB `desired_version`、只读 `provider_target_version` 与 `applied_version`、SNAT 期望与应用状态各自保留。LB 空后端保存及后续激活复用现有签名方法与权限；从 GET 组装监听器 PATCH 只复制输入 DTO 的可写字段，不回填 `deployment_state`/`reason`。旧受理回执重放保持原样，新增派生字段不改变原输入 fingerprint；延迟下发响应合同见 [LB-DEFERRED-01](../interface-integration-register.md#lb-deferred-01)。

## Python 调用

[签名客户端](../../scripts/ops/aksk_vpc_client.py) 的 `sign_vpc` 保留原 GET 向量，`sign_network` 与 `request_network` 当前支持原 24 个 Network 方法，尚未包含 NET-25 VPC CIDR 预设；服务端支持 25 方法不等于该 helper 已全部覆盖。NET-25 为无 body/query 的 `GET /api/v1/networks/vpc-cidr-presets`，仍采用上述七行签名。真实 AK/SK 由调用环境注入，示例、日志和提交中不填写凭据：

```python
import importlib.util
import json
import os

spec = importlib.util.spec_from_file_location("ani_hmac", "scripts/ops/aksk_vpc_client.py")
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)
body = json.dumps({
    "name": "application-subnet",
    "vpc_id": os.environ["ANI_VPC_ID"],
    "cidr": "10.61.1.0/24",
    "idempotency_key": "application-subnet-create-1",
}, separators=(",", ":")).encode("utf-8")
target, headers = client.sign_network(
    os.environ["ANI_ACCESS_KEY"], os.environ["ANI_SECRET_KEY"],
    "POST", "/api/v1/networks/subnets", body=body,
)
# 将 target、headers 和同一份 body 交给 HTTPS 客户端；返回直接 Subnet 对象。
# 查询资源 state 和 last_operation_id，异步完成后继续后续业务。
```

错误、过期、撤销、篡改、认证混用等请求在业务副作用之前拒绝。审计保留可区分的主体，原始请求体、query、referer、认证头、SK 和签名原文不进入日志。HTTP 返回敏感 secret 的既有组合响应继续按其专门合同处理。
