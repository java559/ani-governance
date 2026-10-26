package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
)

// QuotaDispatchWorker 实现持久化转发（计划 §8）。
// 状态机固定为 QUEUED → DISPATCHING → {ACKED | UNKNOWN}；CANCELED_UNSENT 仅由
// §8.3 本地撤销写入。worker 使用 PostgreSQL 领取；同进程和多进程通过行锁/租约协调。
// 构造函数不查询数据库、不启动 goroutine；Start 后运行，Stop 停止领取并等待退出。
type QuotaDispatchWorker struct {
	log      *bLogger.Helper
	ledger   *data.QuotaLedgerRepo
	registry *QuotaAdapterRegistry

	workerID     string
	lease        time.Duration // 固定 15 秒
	pollInterval time.Duration
	batchLimit   int

	notify chan struct{}
	stopCh chan struct{}
	done   chan struct{}
	stopMu sync.Mutex
	cancel context.CancelFunc
}

// 固定退避 1/2/4/8/16/30 秒（§8.2）。
func backoffForAttempt(attempt int) time.Duration {
	shift := attempt
	if shift > 6 {
		shift = 6
	}
	if shift < 1 {
		shift = 1
	}
	d := time.Duration(1<<uint(shift-1)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func NewQuotaDispatchWorker(
	ctx *bootstrap.Context,
	ledger *data.QuotaLedgerRepo,
	registry *QuotaAdapterRegistry,
) *QuotaDispatchWorker {
	return &QuotaDispatchWorker{
		log:          ctx.NewLoggerHelper("quota-dispatch/worker/admin-service"),
		ledger:       ledger,
		registry:     registry,
		workerID:     "worker-" + uuid.NewString(),
		lease:        15 * time.Second,
		pollInterval: 300 * time.Millisecond,
		batchLimit:   1, // claim just before dispatch; no aging leases in a serial batch
		notify:       make(chan struct{}, 1),
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Notify 在占额事务提交成功后唤醒 worker（提交前不得调用）。
func (w *QuotaDispatchWorker) Notify() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// Start 实现 transport.Server 生命周期；不阻塞调用方。
func (w *QuotaDispatchWorker) Start(ctx context.Context) error {
	w.stopMu.Lock()
	defer w.stopMu.Unlock()
	if w.cancel != nil {
		return fmt.Errorf("quota dispatch worker already started")
	}
	ctx, w.cancel = context.WithCancel(ctx)
	go w.loop(ctx)
	return nil
}

// Stop 停止领取并等待有限时间退出。
func (w *QuotaDispatchWorker) Stop(ctx context.Context) error {
	w.stopMu.Lock()
	if w.cancel == nil {
		w.stopMu.Unlock()
		return nil
	}
	w.cancel()
	select {
	case <-w.stopCh:
	default:
		close(w.stopCh)
	}
	w.stopMu.Unlock()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return fmt.Errorf("quota dispatch worker stop timeout")
	}
}

func (w *QuotaDispatchWorker) loop(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-w.notify:
		case <-time.After(w.pollInterval):
		}
		w.drain(ctx)
	}
}

// drain 扫描并投递一批到期操作；每次网络调用不在任何数据库事务内（§7.1）。
func (w *QuotaDispatchWorker) drain(ctx context.Context) {
	for {
		select {
		case <-w.stopCh:
			return
		default:
		}
		ops, err := w.ledger.ClaimDispatchable(ctx, w.workerID, w.lease, w.batchLimit)
		if err != nil {
			w.log.Errorf(context.Background(), "claim dispatchable failed: %s", err.Error())
			return
		}
		if len(ops) == 0 {
			return
		}
		for i := range ops {
			select {
			case <-w.stopCh:
				return
			default:
			}
			w.dispatchOne(ctx, &ops[i])
		}
	}
}

// dispatchOne 投递单个已领取操作：先提交领取（ClaimDispatchable 已提交），再发 RPC。
func (w *QuotaDispatchWorker) dispatchOne(ctx context.Context, op *data.ClaimedOperation) {
	adapter := w.registry.Lookup(op.OwnerService, op.Action)
	if adapter == nil {
		// 恢复遇到缺失 adapter：保留账本并报错，不退额、不改余额（§8.2）。
		w.log.Errorf(context.Background(), "missing quota adapter for owner=%s action=%s operation=%s: ledger kept, dispatch deferred",
			op.OwnerService, op.Action, op.OperationID)
		next := time.Now().Add(60 * time.Second)
		if _, err := w.ledger.MarkUnknown(ctx, op.TenantID, op.OperationID, op.LeaseGeneration, next, "ADAPTER_MISSING", false); err != nil {
			w.log.Errorf(context.Background(), "mark unknown failed: %s", err.Error())
		}
		return
	}

	// 删除操作不新建 charge（§6.2）：命令携带原创建操作的 charge。
	createID := op.OperationID
	if op.CreateOperationID != "" {
		createID = op.CreateOperationID
	}
	original, readErr := w.ledger.GetChargesForOperation(ctx, op.TenantID, createID)
	if readErr != nil {
		w.deferInvalidCommand(ctx, op, "ORIGINAL_CHARGES_UNAVAILABLE")
		return
	}
	charges := make([]data.QuotaChargeRef, 0, len(original))
	seenCode, seenID := map[string]bool{}, map[string]bool{}
	for _, c := range original {
		if c == nil || c.TenantID == nil || *c.TenantID != op.TenantID || c.OperationID != createID || c.OriginalUnits <= 0 || c.ChargeID == "" || c.QuotaCode == "" || seenCode[c.QuotaCode] || seenID[c.ChargeID] {
			w.deferInvalidCommand(ctx, op, "ORIGINAL_CHARGES_INVALID")
			return
		}
		seenCode[c.QuotaCode], seenID[c.ChargeID] = true, true
		charges = append(charges, data.QuotaChargeRef{ChargeID: c.ChargeID, QuotaCode: c.QuotaCode, ChargedUnits: c.OriginalUnits})
	}

	cmd := &QuotaDispatchCommand{
		OperationID:       op.OperationID,
		ResourceID:        op.ResourceID,
		ResourceTenantID:  op.ResourceTenantID,
		Actor:             QuotaActor{Type: op.ActorType, ID: op.ActorID},
		Action:            op.Action,
		RequestHash:       op.RequestHash,
		CanonicalRequest:  []byte(op.CanonicalRequest),
		CreateOperationID: op.CreateOperationID,
		Charges:           charges,
	}
	if commandcontract.ManagedOwner(op.OwnerService) {
		if _, err := validateDispatchCommand(op.OwnerService, cmd); err != nil {
			w.deferInvalidCommand(ctx, op, "ORIGINAL_COMMAND_INVALID")
			return
		}
	}

	if ctx.Err() != nil {
		return
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	ack, derr := adapter.Dispatch(dispatchCtx, cmd)
	cancel()
	if derr == nil {
		derr = ValidateDurableOwnerAck(cmd, ack)
	}

	if derr == nil {
		ok, aerr := w.ledger.AckDispatched(ctx, op.TenantID, op.OperationID, op.LeaseGeneration, string(ack))
		if aerr != nil {
			w.log.Errorf(context.Background(), "ack writeback failed: %s", aerr.Error())
			return
		}
		if !ok {
			// 迟到回写：租约已被接管，放弃本次结果（FAIL-18）。
			w.log.Warnf(context.Background(), "ack writeback skipped (stale generation) operation=%s generation=%d", op.OperationID, op.LeaseGeneration)
		}
		return
	}

	if op.OwnerService == commandcontract.InstanceOwner && op.Action == commandcontract.VMDeleteAction && vmDeleteVersionRejected(derr) {
		if _, err := w.ledger.RejectVMDeleteVersion(ctx, op.TenantID, op.OperationID, op.LeaseGeneration); err != nil {
			w.log.Errorf(context.Background(), "VM delete rejection writeback failed: %v", err)
		}
		return
	}
	// 分类：永久合同错误暂停重试；传输不确定保持占额并退避重试（同 ID 同报文）。
	perm := isPermanentContractError(derr)
	next := time.Now().Add(backoffForAttempt(op.AttemptCount))
	ok, aerr := w.ledger.MarkUnknown(ctx, op.TenantID, op.OperationID, op.LeaseGeneration, next, errorReason(derr), perm)
	if aerr != nil {
		w.log.Errorf(context.Background(), "unknown writeback failed: %s", aerr.Error())
		return
	}
	if !ok {
		w.log.Warnf(context.Background(), "unknown writeback skipped (stale generation) operation=%s generation=%d", op.OperationID, op.LeaseGeneration)
		return
	}
	if perm {
		w.log.Errorf(context.Background(), "permanent contract error for operation=%s: %s; retry paused (retry_blocked), ledger kept", op.OperationID, derr.Error())
	} else {
		w.log.Warnf(context.Background(), "dispatch unknown for operation=%s: %s; will retry with same id/payload", op.OperationID, derr.Error())
	}
}

func vmDeleteVersionRejected(err error) bool {
	st, ok := status.FromError(err)
	if !ok || (st.Code() != codes.Aborted && st.Code() != codes.FailedPrecondition) {
		return false
	}
	if st.Message() == "VM_VERSION_CONFLICT" {
		return true
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Reason == "VM_VERSION_CONFLICT" {
			return true
		}
	}
	return false
}

func (w *QuotaDispatchWorker) deferInvalidCommand(ctx context.Context, op *data.ClaimedOperation, reason string) {
	_, err := w.ledger.MarkUnknown(ctx, op.TenantID, op.OperationID, op.LeaseGeneration, time.Now().Add(time.Minute), reason, false)
	if err != nil {
		w.log.Errorf(context.Background(), "defer invalid command operation=%s: %s", op.OperationID, err)
	}
}

// isPermanentContractError 判定下游明确的永久合同错误（gRPC InvalidArgument/
// FailedPrecondition/NotFound/PermissionDenied/Unauthenticated）。
// 下游明确拒绝业务执行也不能由本侧凭错误字符串退额；仅暂停重试。
func isPermanentContractError(err error) bool {
	return grpcCodeIn(err,
		codesInvalidArgument,
		codesFailedPrecondition,
		codesNotFound,
		codesPermissionDenied,
		codesUnauthenticated,
	)
}
