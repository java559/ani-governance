package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"go-wind-admin/pkg/quotaerrors"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	quotapb "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"

	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
)

// QuotaInternalServerConfig 内部退额 listener 配置（§9.3）。
// 默认 disabled；enabled 时地址/证书/owner 映射缺失或不合法，启动报错，不降级明文。
type QuotaInternalServerConfig struct {
	Enabled  bool
	Address  string
	CAFile   string
	CertFile string
	KeyFile  string
	// CertOwnerMap 将客户端证书的精确 DNS SAN 映射到已注册 owner_service。
	// Only explicit production integrations may register an owner identity.
	CertOwnerMap map[string]string
}

// QuotaInternalConfigFromEnv 从环境变量解析：
// ANI_QUOTA_ENABLED / ANI_QUOTA_INTERNAL_ADDR / ANI_QUOTA_CA_FILE /
// ANI_QUOTA_CERT_FILE / ANI_QUOTA_KEY_FILE /
// ANI_QUOTA_RESOURCE_DNS_SAN。
// The environment configures the exact deployed name of the compiled Resource
// owner, never an arbitrary owner or wildcard.
func QuotaInternalConfigFromEnv() QuotaInternalServerConfig {
	config := QuotaInternalServerConfig{
		Enabled:  os.Getenv("ANI_QUOTA_ENABLED") == "true",
		Address:  os.Getenv("ANI_QUOTA_INTERNAL_ADDR"),
		CAFile:   os.Getenv("ANI_QUOTA_CA_FILE"),
		CertFile: os.Getenv("ANI_QUOTA_CERT_FILE"),
		KeyFile:  os.Getenv("ANI_QUOTA_KEY_FILE"),
	}
	if name := os.Getenv("ANI_QUOTA_RESOURCE_DNS_SAN"); name != "" {
		config.CertOwnerMap = map[string]string{name: "ani-resource-service"}
	}
	if os.Getenv("ANI_QUOTA_MODELDEV_ENABLED") == "true" {
		if config.CertOwnerMap == nil {
			config.CertOwnerMap = make(map[string]string)
		}
		if owner, exists := config.CertOwnerMap[commandcontract.ModelDevOwner]; exists && owner != commandcontract.ModelDevOwner {
			// Retain an invalid registration for Validate to reject; never replace
			// an existing owner's identity with another compiled owner.
			config.CertOwnerMap[commandcontract.ModelDevOwner] = ""
		} else {
			config.CertOwnerMap[commandcontract.ModelDevOwner] = commandcontract.ModelDevOwner
		}
	}
	return config
}

// Validate enabled 配置完整性；fail-closed，不降级明文。
func (c *QuotaInternalServerConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Address == "" || c.CAFile == "" || c.CertFile == "" || c.KeyFile == "" {
		return fmt.Errorf("quota internal server requires ANI_QUOTA_INTERNAL_ADDR, ANI_QUOTA_CA_FILE, ANI_QUOTA_CERT_FILE and ANI_QUOTA_KEY_FILE when enabled")
	}
	if len(c.CertOwnerMap) == 0 {
		return fmt.Errorf("quota internal server requires at least one registered owner mapping")
	}
	for name, owner := range c.CertOwnerMap {
		if !exactQuotaDNSName(name) || owner == "" {
			return fmt.Errorf("quota internal server requires exact registered DNS SAN identities")
		}
		if (owner == commandcontract.ModelDevOwner || name == commandcontract.ModelDevOwner) && owner != name {
			return fmt.Errorf("ModelDev quota release requires its unique compiled DNS identity")
		}
	}
	return nil
}

func exactQuotaDNSName(name string) bool {
	if name == "" || len(name) > 253 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "*:/\\") || net.ParseIP(name) != nil {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
}

// QuotaInternalServer 独立内部 mTLS gRPC listener（QUOTA-03）。
// TLS 最低 1.3，RequireAndVerifyClientCert；验证链后必须匹配精确 DNS SAN
// 与已注册 owner。不接受 X-Forwarded-*、owner header、Bearer 或 tenant 参数
// 替代证书身份。
type QuotaInternalServer struct {
	quotapb.UnimplementedQuotaReleaseServiceServer

	grpcServer *grpc.Server
	listener   net.Listener
	ledger     *data.QuotaLedgerRepo
	owners     map[string]string // 精确 SAN -> owner_service

	lifecycleMu sync.Mutex
	started     bool
	stopping    bool
	serveDone   chan struct{}
	stopDone    chan struct{}
}

// NewQuotaInternalServer 构造；disabled 返回 (nil, nil)。
func NewQuotaInternalServer(cfg QuotaInternalServerConfig, ledger *data.QuotaLedgerRepo) (*QuotaInternalServer, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// The caller verifies this server certificate against its explicitly
	// configured Governance DNS name; deployment identities are not inferred.
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load quota internal server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read quota internal CA: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("quota internal CA contains no certificates")
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
	}

	lis, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("listen quota internal address: %w", err)
	}

	owners := make(map[string]string, len(cfg.CertOwnerMap))
	for name, owner := range cfg.CertOwnerMap {
		owners[name] = owner
	}
	srv := &QuotaInternalServer{
		ledger:   ledger,
		owners:   owners,
		listener: lis,
	}
	srv.grpcServer = grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.ChainUnaryInterceptor(srv.authInterceptor),
	)
	quotapb.RegisterQuotaReleaseServiceServer(srv.grpcServer, srv)
	return srv, nil
}

// AuthInterceptor 从客户端证书提取精确 DNS SAN 并映射到已注册 owner；
// 无证书/未知 SAN/多 SAN 混用一律拒绝（§9.3）。
func (s *QuotaInternalServer) authInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		return nil, quotaerrors.ReleaseUnauthenticated("missing peer")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 {
		return nil, quotaerrors.ReleaseUnauthenticated("client certificate required")
	}
	if info == nil || (info.FullMethod != quotapb.QuotaReleaseService_ReportResourceReleased_FullMethodName && info.FullMethod != quotapb.QuotaReleaseService_ReportQuotaRelease_FullMethodName) {
		return nil, quotaerrors.ReleasePermissionDenied("RPC is not allowed")
	}
	leaf := tlsInfo.State.VerifiedChains[0][0]
	if len(leaf.DNSNames) != 1 || len(leaf.URIs) != 0 || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 || strings.Contains(leaf.DNSNames[0], "*") {
		return nil, quotaerrors.ReleaseUnauthenticated("one exact DNS service identity required")
	}
	// 同一 SAN 集内必须恰好命中一个已注册 owner；同 CA 的另一服务不能退他人额度。
	ownerService := ""
	for _, name := range leaf.DNSNames {
		if mapped, registered := s.owners[name]; registered {
			if ownerService != "" && ownerService != mapped {
				return nil, quotaerrors.ReleaseUnauthenticated("ambiguous certificate identity")
			}
			ownerService = mapped
		}
	}
	if ownerService == "" {
		return nil, quotaerrors.ReleaseUnauthenticated("unknown client identity")
	}
	return handler(withQuotaOwner(ctx, ownerService), req)
}

type quotaOwnerKey struct{}

func withQuotaOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, quotaOwnerKey{}, owner)
}

func quotaOwnerFromContext(ctx context.Context) (string, error) {
	owner, ok := ctx.Value(quotaOwnerKey{}).(string)
	if !ok || owner == "" {
		return "", quotaerrors.ReleaseUnauthenticated("missing verified owner")
	}
	return owner, nil
}

// ReportQuotaRelease 实现 QUOTA-03：请求不含 tenant_id/owner_service，
// 租户/owner 从 charge 与已验证证书取出（§9.1）。
func (s *QuotaInternalServer) ReportQuotaRelease(ctx context.Context, req *quotapb.ReportQuotaReleaseRequest) (*quotapb.ReportQuotaReleaseResponse, error) {
	owner, err := quotaOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, quotaerrors.ReleaseInvalid("empty request")
	}
	if _, perr := uuid.Parse(req.GetReleaseEventId()); perr != nil {
		return nil, quotaerrors.ReleaseInvalid("release_event_id must be a UUID")
	}
	if _, perr := uuid.Parse(req.GetOperationId()); perr != nil {
		return nil, quotaerrors.ReleaseInvalid("operation_id must be a UUID")
	}
	items := req.GetItems()
	if len(items) == 0 {
		return nil, quotaerrors.ReleaseInvalid("items must not be empty")
	}
	if len(items) > 16 {
		return nil, quotaerrors.ReleaseInvalid("at most 16 items")
	}
	switch req.GetReason() {
	case quotapb.ReleaseReason_ABORTED_CLEANED, quotapb.ReleaseReason_RESOURCE_RELEASED:
	default:
		return nil, quotaerrors.ReleaseInvalid("reason must be ABORTED_CLEANED or RESOURCE_RELEASED")
	}
	if len(req.GetResourceRefs()) > 64 {
		return nil, quotaerrors.ReleaseInvalid("at most 64 resource_refs")
	}

	// 同 charge 重复 item 拒绝；协议内部数量用整数。
	seen := make(map[string]struct{}, len(items))
	releaseItems := make([]data.QuotaReleaseItemInput, 0, len(items))
	for _, it := range items {
		if _, perr := uuid.Parse(it.GetChargeId()); perr != nil {
			return nil, quotaerrors.ReleaseInvalid("charge_id must be a UUID")
		}
		if strings.HasPrefix(it.GetQuotaCode(), "gpu.") {
			return nil, quotaerrors.ReleaseInvalid("GPU owners must use ReportResourceReleased")
		}
		if it.GetQuotaCode() == "" {
			return nil, quotaerrors.ReleaseInvalid("quota_code is required")
		}
		if _, dup := seen[it.GetChargeId()]; dup {
			return nil, quotaerrors.ReleaseInvalid("duplicate charge item")
		}
		seen[it.GetChargeId()] = struct{}{}
		releaseItems = append(releaseItems, data.QuotaReleaseItemInput{
			ChargeID:      it.GetChargeId(),
			QuotaCode:     it.GetQuotaCode(),
			ReleasedTotal: it.GetReleasedTotal(),
		})
	}

	payloadHash := quotaReleasePayloadHash(req.GetOperationId(), req.GetReason().String(), items, req.GetResourceRefs())

	results, err := s.ledger.Release(ctx, &data.QuotaReleaseInput{
		OwnerService:   owner,
		ReleaseEventID: req.GetReleaseEventId(),
		OperationID:    req.GetOperationId(),
		Reason:         req.GetReason().String(),
		PayloadHash:    payloadHash,
		PayloadJSON:    quotaReleasePayloadJSON(req, owner),
		Items:          releaseItems,
	})
	if err != nil {
		return nil, err // gRPC status 错误原样透传
	}

	out := make([]*quotapb.QuotaReleaseResult, 0, len(results))
	for _, r := range results {
		out = append(out, &quotapb.QuotaReleaseResult{
			ChargeId:      r.ChargeID,
			AppliedDelta:  r.AppliedDelta,
			ReleasedTotal: r.ReleasedTotal,
		})
	}
	return &quotapb.ReportQuotaReleaseResponse{Items: out}, nil
}

// ReportResourceReleased accepts only a full physical cleanup fact. Quantities,
// tenant and ownership are resolved and locked from the original create ledger.
func (s *QuotaInternalServer) ReportResourceReleased(ctx context.Context, req *quotapb.ReportResourceReleasedRequest) (*quotapb.ReportResourceReleasedResponse, error) {
	owner, err := quotaOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || len(req.ProtoReflect().GetUnknown()) != 0 {
		return nil, quotaerrors.ReleaseInvalid("invalid resource release")
	}
	for _, id := range []string{req.ReleaseEventId, req.CreateOperationId, req.ResourceId} {
		parsed, e := uuid.Parse(id)
		if e != nil || parsed == uuid.Nil || parsed.String() != id {
			return nil, quotaerrors.ReleaseInvalid("canonical UUID references required")
		}
	}
	if owner == commandcontract.ModelDevOwner && req.ReleaseEventId == req.CreateOperationId {
		return nil, quotaerrors.ReleaseInvalid("ModelDev release requires a distinct close identity")
	}
	if req.Reason != quotapb.ReleaseReason_RESOURCE_RELEASED && req.Reason != quotapb.ReleaseReason_ABORTED_CLEANED {
		return nil, quotaerrors.ReleaseInvalid("invalid complete release reason")
	}
	payload := fmt.Sprintf(`{"schema":"resource-release-v1","owner_service":%q,"release_event_id":%q,"create_operation_id":%q,"resource_id":%q,"reason":%q}`, owner, req.ReleaseEventId, req.CreateOperationId, req.ResourceId, req.Reason.String())
	sum := sha256.Sum256([]byte(payload))
	results, err := s.ledger.Release(ctx, &data.QuotaReleaseInput{OwnerService: owner, ReleaseEventID: req.ReleaseEventId, OperationID: req.CreateOperationId, ResourceID: req.ResourceId, Complete: true, Reason: req.Reason.String(), PayloadHash: hex.EncodeToString(sum[:]), PayloadJSON: payload})
	if err != nil {
		return nil, err
	}
	state := quotapb.ResourceReleaseStatus_ALREADY_APPLIED
	for _, result := range results {
		if result.AppliedDelta > 0 {
			state = quotapb.ResourceReleaseStatus_APPLIED
		}
	}
	return &quotapb.ReportResourceReleasedResponse{ReleaseEventId: req.ReleaseEventId, CreateOperationId: req.CreateOperationId, ResourceId: req.ResourceId, Status: state}, nil
}

// quotaReleasePayloadHash 基于固定结构、排序后的唯一 items/resource_refs 计算；
// 排除传输 request-id，不把 JSON 字段顺序视为不同业务内容（§9.2）。
func quotaReleasePayloadHash(operationID, reason string, items []*quotapb.QuotaReleaseItem, refs []*quotapb.QuotaResourceRef) string {
	lines := make([]string, 0, len(items)+len(refs)+2)
	lines = append(lines, "v1", operationID, reason)
	sorted := make([]*quotapb.QuotaReleaseItem, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].GetChargeId() != sorted[j].GetChargeId() {
			return sorted[i].GetChargeId() < sorted[j].GetChargeId()
		}
		return sorted[i].GetQuotaCode() < sorted[j].GetQuotaCode()
	})
	seenItem := make(map[string]struct{}, len(sorted))
	for _, it := range sorted {
		line := fmt.Sprintf("item:%s:%s:%d", it.GetChargeId(), it.GetQuotaCode(), it.GetReleasedTotal())
		key := line
		if _, dup := seenItem[key]; dup {
			continue
		}
		seenItem[key] = struct{}{}
		lines = append(lines, line)
	}
	refLines := make([]string, 0, len(refs))
	for _, r := range refs {
		refLines = append(refLines, "ref:"+r.GetResourceId())
	}
	sort.Strings(refLines)
	lines = append(lines, refLines...)

	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// quotaReleasePayloadJSON 保存整个已验证批次的原文（含 owner，供审计）。
func quotaReleasePayloadJSON(req *quotapb.ReportQuotaReleaseRequest, owner string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"schema_version":1,"owner_service":%q,"release_event_id":%q,"operation_id":%q,"reason":%q,"items":[`,
		owner, req.GetReleaseEventId(), req.GetOperationId(), req.GetReason().String())
	for i, it := range req.GetItems() {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"charge_id":%q,"quota_code":%q,"released_total":%d}`,
			it.GetChargeId(), it.GetQuotaCode(), it.GetReleasedTotal())
	}
	b.WriteString("]")
	if refs := req.GetResourceRefs(); len(refs) > 0 {
		b.WriteString(`,"resource_refs":[`)
		for i, r := range refs {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "%q", r.GetResourceId())
		}
		b.WriteString("]")
	}
	b.WriteString("}")
	return b.String()
}

// Start 实现 transport.Server 生命周期。Kratos 自己并发启动各个 Server；
// 此处阻塞到 Serve 退出，才能把异常交还给 App.Run。
func (s *QuotaInternalServer) Start(_ context.Context) error {
	s.lifecycleMu.Lock()
	if s.started || s.stopping {
		s.lifecycleMu.Unlock()
		return fmt.Errorf("quota internal server already started or stopped")
	}
	s.started = true
	s.serveDone = make(chan struct{})
	s.lifecycleMu.Unlock()

	defer close(s.serveDone)
	err := s.grpcServer.Serve(s.listener)
	if err == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	stopping := s.stopping
	s.lifecycleMu.Unlock()
	if stopping && errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return fmt.Errorf("serve quota internal gRPC: %w", err)
}

// Stop 优雅停止，以调用者期限和三秒上限中较早者为准。
// 构造期 listener 尚未交给 gRPC Serve 时也必须由本对象关闭。
func (s *QuotaInternalServer) Stop(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.stopping {
		done := s.stopDone
		s.lifecycleMu.Unlock()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.stopping = true
	started := s.started
	serveDone := s.serveDone
	done := make(chan struct{})
	s.stopDone = done
	s.lifecycleMu.Unlock()

	if !started {
		_ = s.listener.Close()
	}
	go func() {
		s.grpcServer.GracefulStop()
		_ = s.listener.Close()
		if started {
			<-serveDone
		}
		close(done)
	}()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.grpcServer.Stop()
		return ctx.Err()
	case <-timer.C:
		s.grpcServer.Stop()
		return fmt.Errorf("quota internal graceful stop timed out: %w", context.DeadlineExceeded)
	}
}
