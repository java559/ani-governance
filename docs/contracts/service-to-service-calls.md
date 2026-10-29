# ANI 服务间调用规则

Governance 当前通过受信 Inference API 客户端提供推理服务管理 BFF。Gov → Inference 使用 TLS 1.3 双向认证、精确 DNS SAN 和登记的 RPC；客户端重建租户与 actor metadata，不透传公网 Authorization 或自报 owner。Sandbox 管理使用同一 Resource mTLS listener 的 `sandbox.v1.SandboxService` 精确八方法白名单，Gov 先完成用户权限、INSTANCE 套餐检查和持久化 `resource_tenant_id` 映射，再重建 `x-ani-tenant-id`、`x-ani-actor`、`x-ani-request-id`。Resource 负责 immutable image、冻结规格、持久幂等和 PostgreSQL/Kubernetes worker。 Sandbox 新创建同时携带公共意图摘要 `create_intent_digest` 与完整冻结命令摘要；Governance 使用 Resource API 模块的 canonical helper，不另建摘要算法。重放先读原 Sandbox 的持久摘要与创建 operation，不依赖当前 preset 或运行配置；仅新请求进入解析/受理。Inference 的 GPU 命令还携带冻结业务摘要、执行/展示快照和操作幂等 ID，Gov 原始 quota_items 仍只保存在 Gov。Model、Resource 与 ACC 的现行合同保持各自范围。

适用：Gov → svc、svc → Gov、svc → svc 的内部业务 RPC。本文是新接入及修改服务间调用时必须遵守的统一规范；存量差异须在明确范围的独立迁移中处理，见[当前差异与迁移边界](#current-differences)。规范入仓不表示当前运行实现已全部符合。本文不改变各服务的业务职责、已发布字段或历史验收结论。

## 1. 三种方向，一套规则

| 方向 | 可以调用什么 | 必须验证什么 |
|---|---|---|
| Gov → svc | 已登记的用户业务委托；Gov 自己负责的账本同步 | svc 验证 Gov 证书、RPC 权限、租户和资源归属。用户委托还必须经过 Gov 的用户认证、角色/action、套餐模块检查；消耗额度的操作另做配额检查 |
| svc → Gov | 已登记的内部接口，例如上报删除完成后的退额事实 | Gov 验证 svc 证书、RPC 权限、登记的业务 owner、原操作和账本。不能凭 svc 自报的 tenant/actor/owner 写账 |
| svc → svc | 明确登记的领域依赖，例如 Inference → Accelerator 的只读释放观察 | 接收方验证调用服务、RPC 权限、该调用允许的业务范围、租户和资源归属。不能借此绕过 Gov 的用户授权或占额 |

不是所有调用都经过 Gov。每个服务只写自己负责的数据，通过对方公布的 API 请求对方完成业务；不直接写对方数据库，不跨仓导入 `internal`。

每条调用关系必须登记：**调用服务身份、接收服务身份、完整 RPC 方法、用户委托/后台事实、允许的业务范围、租户来源、幂等键、超时**。接收方维护允许清单，未知服务或方法默认拒绝。登记“服务可调用”不等于“可操作任意租户”。

## 2. mTLS 和服务身份

| 项目 | 规则 |
|---|---|
| 传输 | TLS 1.3 + 双向证书验证。无证书、错 CA、过期证书、身份不符直接失败；禁止明文 fallback 和 `InsecureSkipVerify` |
| 调用者身份 | 从 TLS 已验证的客户端证书读取**唯一、精确登记的 DNS SAN**。客户端证书不混入其他 DNS、URI、IP、email 身份；不使用 CN、通配符或前缀匹配 |
| 接收者身份 | 调用方用可信配置的 `server_name` 验证服务器证书；证书必须含这个字面 DNS SAN，不能依赖通配符匹配 |
| 连接地址 | `address` 只决定连到哪里，可以是 IP:port 或受管 DNS 地址。不能从地址推导服务权限，也不能让用户指定地址、`server_name`、CA 或 owner |
| 业务 owner | 服务证书身份与业务 owner 显式登记映射。例如已有账本 owner `ani-inference` 不因证书命名变化而改名 |
| 证书 | CA/签发权限属于部署信任边界；不同服务不得共用私钥或冒用 Gov 证书。轮换必须有明确的重载/重启办法 |

**统一的是身份格式和校验办法，不是让双方使用同一个名字。** Gov 的客户端 DNS 身份是 `ani-governance`；它连接的服务使用该接收方已登记的服务器 DNS。其他生产 SAN 以实际签发登记为准，不能从本文示例猜值。

每个服务采用**一个内部业务 mTLS listener**，在同一进程中注册需要的业务 RPC，按调用者和方法授权。不为 Gov 单独再开一套业务入口。Gov 公网用户入口、健康/管理入口、推理数据面属于其他用途；若 health/reflection 注册在业务 listener 上，也必须显式授权。

## 3. 租户、操作人和后台事实

1. **服务身份来自证书，用户身份来自 Gov 已认证的 Principal。** Gov 先检查用户权限，再构造下游请求；公网传来的 tenant、actor、owner、角色、plan、charges 不能成为可信字段。创建、删除使用各自的 action；套餐模块决定功能是否可用，配额决定可消耗多少，二者不互相替代。
2. Gov 内部租户主键与业务服务租户 UUID 必须通过已持久化的映射转换，不能转成字符串就当同一个租户。接收方对查询、写入、关联及异步处理都检查资源归属。
3. 租户/actor 放在接口已定义的结构化请求或附件中；已有 metadata 合同继续按该合同处理。出站 **重建 metadata**，不透传公网 Authorization、AK/SK、租户、actor 或 owner header。身份字段如出现在多个位置，必须单值且一致；冲突直接拒绝。
4. svc → svc 转交用户委托，必须在该 RPC 的登记中明确允许，且取自前一步已验证并保存的业务上下文。原 actor 可以用于审计；仅有 `actor=user` 字段不能证明 Gov 已授权。接收方仍检查该服务能否操作这项资源。
5. 后台同步、退额、完成通知使用服务自己的持久事实，不伪造一个用户 Principal。Gov 从原操作/账本确定 tenant 和 owner，再核验通知；证书属于同一 CA 不足以获得退额权。
6. `request_id` 用于追踪；`operation_id`、`release_event_id` 用于业务幂等。重试可以有新的追踪 ID，不能换业务幂等 ID。

## 4. Go 参考代码

下面是传输参考示例，展示精确 DNS、双向 TLS、Unary/Stream 方法授权和 metadata 重建，本轮未编译或运行验证。接入时沿用各仓现有框架和 adapter，不把示例建立为共享 ANI runtime；Kratos 的 TLS 配置和拦截位置落实相同校验，并按实际接入验证。

```go
package callrules

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type TLSFiles struct{ CA, Cert, Key string }

func load(f TLSFiles) (*x509.CertPool, tls.Certificate, error) {
	pem, err := os.ReadFile(f.CA)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, tls.Certificate{}, errors.New("CA contains no certificates")
	}
	pair, err := tls.LoadX509KeyPair(f.Cert, f.Key)
	return roots, pair, err
}

func clientDNS(c *x509.Certificate) (string, error) {
	if c == nil || len(c.DNSNames) != 1 || len(c.URIs) != 0 ||
		len(c.IPAddresses) != 0 || len(c.EmailAddresses) != 0 ||
		c.DNSNames[0] == "" || strings.Contains(c.DNSNames[0], "*") {
		return "", errors.New("client requires one exact DNS identity")
	}
	return c.DNSNames[0], nil
}

func Dial(f TLSFiles, address, selfDNS, serverDNS string) (*grpc.ClientConn, error) {
	// 参数来自部署登记，不来自业务请求。证书损坏时不降级。
	if address == "" || selfDNS == "" || serverDNS == "" ||
		strings.ContainsAny(serverDNS, "*:/\\ \t\r\n") {
		return nil, errors.New("missing or invalid managed endpoint identity")
	}
	roots, pair, err := load(f)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	name, err := clientDNS(leaf)
	if err != nil || name != selfDNS {
		return nil, errors.New("local client identity mismatch")
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots,
		Certificates: []tls.Certificate{pair}, ServerName: serverDNS,
		// 保留 TLS 默认的 CA、有效期、用途及主机名校验，再补字面 SAN 校验。
		VerifyConnection: func(s tls.ConnectionState) error {
			if len(s.VerifiedChains) == 0 || len(s.PeerCertificates) == 0 {
				return errors.New("unverified server certificate")
			}
			matched := false
			for _, dns := range s.PeerCertificates[0].DNSNames {
				if strings.Contains(dns, "*") {
					return errors.New("wildcard server identity is forbidden")
				}
				matched = matched || dns == serverDNS
			}
			if !matched {
				return errors.New("server exact DNS SAN mismatch")
			}
			return nil
		},
	}
	// NewClient 不做握手；连通和身份须由实际 RPC 验证。
	return grpc.NewClient(address,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithDisableServiceConfig(), grpc.WithDisableRetry())
}

// 只做“直接调用服务 + 完整 RPC”校验；业务 handler 还要校验租户和资源。
// 允许清单由接收方登记，启动后不可修改。禁止 "*" 或方法前缀授权。
type Policy map[string]map[string]bool
type callerKey struct{}

func Caller(ctx context.Context) (string, bool) {
	name, ok := ctx.Value(callerKey{}).(string)
	return name, ok
}

func (p Policy) authorize(ctx context.Context, method string) (context.Context, error) {
	remote, ok := peer.FromContext(ctx)
	if !ok || remote == nil {
		return nil, status.Error(codes.Unauthenticated, "missing TLS peer")
	}
	info, ok := remote.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.PeerCertificates) == 0 {
		return nil, status.Error(codes.Unauthenticated, "unverified TLS peer")
	}
	name, err := clientDNS(info.State.PeerCertificates[0])
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid service identity")
	}
	if !p[name][method] {
		return nil, status.Error(codes.PermissionDenied, "RPC not allowed for service")
	}
	// 可信身份存进私有 context key，不读报文自称的 caller/owner。
	return context.WithValue(ctx, callerKey{}, name), nil
}

type authorizedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authorizedStream) Context() context.Context { return s.ctx }

func NewServer(f TLSFiles, p Policy) (*grpc.Server, error) {
	roots, pair, err := load(f)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair},
		ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert,
	}
	return grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)),
		grpc.UnaryInterceptor(func(ctx context.Context, req any,
			info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			trusted, err := p.authorize(ctx, info.FullMethod)
			if err != nil {
				return nil, err
			}
			return next(trusted, req)
		}),
		grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream,
			info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			trusted, err := p.authorize(stream.Context(), info.FullMethod)
			if err != nil {
				return err
			}
			// 仅认证建流不够；handler 必须逐消息校验租户和资源归属。
			return next(srv, &authorizedStream{ServerStream: stream, ctx: trusted})
		})), nil
}

func CallContext(ctx context.Context, requestID string, timeout time.Duration) (context.Context, context.CancelFunc) {
	// timeout 是已校验的正值；requestID 是已校验的追踪 ID。
	// tenant/actor 使用该 RPC 的结构化合同；若合同要求 metadata，则从可信值重建。
	md := metadata.Pairs("x-ani-request-id", requestID)
	return context.WithTimeout(metadata.NewOutgoingContext(ctx, md), timeout)
}
```

例如 Inference 的用户业务委托允许清单只放明确批准的方法；同一 listener 上其他业务 API 也逐项登记，不因注册了整个 service 自动开放：

```go
policy := callrules.Policy{
	"ani-governance": {
		"/inference.v1.InferenceServiceManager/CreateInferenceService": true,
		"/inference.v1.InferenceServiceManager/DeleteInferenceService": true,
	},
}
// NewServer 返回后，在这个 server 注册 Inference API 和需要的模型引用 API。
// handler 从 Caller(ctx) 获取 Gov 身份，再验证附件、原操作、租户和资源。
// 用户委托和后台事实的约束必须在 handler 落实，不能只复制上面的布尔清单。
```

配置缺失处理只有两种：必须依赖则启动失败；可选依赖则明确禁用对应功能并返回不可用。禁止伪造成功、改走普通 listener 或回退明文。CA、证书、私钥、地址、`server_name` 和正超时必须齐全。客户端连接由 composition root 统一装配并关闭；证书只在启动时读取的实现，续期后必须重启重载。

TLS 校验与拦截器行为参照 [Go TLS 文档](https://pkg.go.dev/crypto/tls#Config)和 [gRPC Go 文档](https://pkg.go.dev/google.golang.org/grpc#NewClient)。身份命名、允许清单和业务范围是 ANI 的项目规则。

## 5. 超时、重试、受理和退额

| 问题 | 规则 |
|---|---|
| 超时/取消 | 每次 RPC 有明确 deadline，继承调用链取消；请求处理不用 `context.Background()` 切断取消。已持久受理后的后台 worker 使用自己的生命周期 |
| 写入重试 | 使用原 operation/event ID 和原内容，只对合同明确允许的瞬时失败有限重试。关闭未审查的自动 retry/hedging；超时不证明没有执行 |
| 重放 | 同 ID、同内容返回原结果；同 ID、异内容拒绝。业务变更与幂等记录同事务保存，不能先 ACK 再落库 |
| ACK | durable ACK 只表示命令已持久受理，不表示工作负载 Ready、删除完成或已退额；异步结果由已登记的查询/完成通知观察 |
| 后台通知 | 先持久保存完成事实和待发送事件，再可靠投递；重试沿用事件 ID。触发判定属于实际业务 owner，传输 adapter 不替它判定完成 |
| GPU 退额 | 只报告实际完整清理后的事实；使用稳定 release event、原 CREATE、resource 和 reason 四字段。Gov 核验认证 owner、原操作和原账后幂等全退；owner 不传 charge/code/units。完整物理清理和创建封闭属于 owner，Gov 不根据 Usage 或当前目录推断 |
| 不能触发退额的信号 | stop、restart、进程退出、NotFound、超时、ACK、usage ENDED 或单次 binding 缺失，都不能自动证明已释放 |
| 错误 | 保留 RPC 状态和领域 reason，不吞错或把失败写成成功；对公网映射时隐藏内部证书、地址、密钥和跨租户数据 |

协议只消费接收方公布的固定版本 API 模块，不复制 Proto/生成代码，不用本地 `replace` 冒充正式互操作。新增调用先登记并打通真实最小链路：**调用方业务 → mTLS → 接收方权限/持久化 → 可观察结果**。只允许在链路外部依赖处用明确替身。

<a id="current-differences"></a>

## 6. 当前差异与迁移边界

下表保留制定规则时核对的跨仓源码快照（2026-10-10），不是本轮已实施或重新验证的改造。尤其 Inference 的 listener 状态仍是该历史快照，不作为本轮运行结论。当前 Governance 客户端的身份、受管配置和 metadata 索引见[业务服务接入指南](../service-integration.md#current-client-contracts)。

2026-10-11 的首个实现批次仅收敛 Gov → Resource 的 Network/Image 调用，候选源码与实际验证见 [S2S-RES-01](../interface-integration-register.md#s2s-res-01)。其他服务仍按各自迁移记录处理；本批未部署，不将下表的历史差异全部标为已解决。

| 现状 | 按本文收敛 |
|---|---|
| Gov → Accelerator 的历史版本及受管 Inference 使用客户端 URI SAN | 统一为已登记的唯一客户端 DNS SAN；双方同步修改校验、签发和合同，不长期双轨兼容 |
| Network/Image、ModelDev 使用 Gov 客户端 DNS，但证书严格度和配置不同 | 对齐唯一客户端 DNS、字面服务器 DNS、方法允许清单和可信上下文规则 |
| Inference 同时存在普通和受管业务 listener | 迁移必要 API、现有调用者和证书到同一个业务 mTLS listener，再删除普通入口；不只改 Gov 客户端就宣布完成 |
| Gov 退额已有客户端 DNS → 固定 `ani-inference` owner 映射 | 保留显式映射和原账本 owner；按真实证书登记收敛配置，不开放任意 owner |

本次 Gov/ACC 授权改造源码移除逐用户/逐租户静态 grant，直接复用 ACC provider.json 的受管连接，并将服务证书校验收敛为唯一 DNS SAN。它不改变角色/套餐由 Gov 管、资源归属由接收方核验的规则。现网 URI 证书和旧配置须配套迁移；源码与定向测试不代表已经部署。

业务合同继续以 [GPU owner 指南](gpu-owner-integration-guide.md)、[AK/SK 合同](aksk-signature.md)等领域文档为准；本文对新接入和修改调用的传输、身份、授权和上下文规则有优先权。专项文档中的现有 URI SAN、listener 等实现记录应与迁移目标分开阅读。迁移需要独立的实现、证书配置和双方联调；**2026-10-10 制定规范的批次只将正文入仓，没有修改运行逻辑或宣称存量服务已符合新规。后续实现以各批次结果为准。**

### Model 操作历史查询登记（2026-10-21）

`ani-governance` → `ani-model-service` 的
`/model.v1.ModelService/ListModelOperations` 是用户委托只读调用（完整 Proto
package 以 Model 发布的 API 为准）。公网通过 `model:service:read` 和 `MODEL`
套餐后，从认证 Principal 解析 actor、持久租户映射解析 Model tenant UUID；
ModelClient 重建 `x-ani-tenant-id`、`x-ani-actor` 和 `x-ani-request-id`，替换请求
中的 tenant，保留调用取消并使用现有受管 Model timeout。不透传 Authorization。
Model 服务端只允许登记的 Governance mTLS 身份读取当前租户所属模型的历史，
Inference 身份不获得该 RPC 权限。查询不产生副作用，不使用业务幂等键。

### Sandbox 独立模型切换门禁

新 Gov 不传 Sandbox 的 `instance_id`；Resource 新版本拒绝携带该字段的读取和动作。发布两个版本前必须先停止旧 Gov 的 Sandbox 新建及全部旧投递 worker，并保持停用直到完成协调切换；对目标 Gov 数据库执行只读 [旧创建意图检查](../../scripts/ops/sql/preflight-sandbox-independent.sql)。存在未确认且带旧实例引用的命令时脚本报错，禁止切换，须逐条查明并安全处理。只读检查通过不代表两版本可以混用；升级期间还需限制旧客户端和旧 worker。历史命令保留摘要与原字段，超时或查无对象不证明命令未执行；不得自动清除或重建幂等键。
