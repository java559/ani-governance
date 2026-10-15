// TaskService 的真实 asynq 队列验收测试（白盒，包内测试）。
//
// 关闭 task_service_sqlite_test.go 明确记录的跳过项「真实 asynq 调度器与 Redis 队列」：
// StartAllTask / RestartAllTask / StopAllTask 不再走本地桩，而是装配
// go-wind-admin/pkg/localdeps/kratos-transport/transport/asynq 的真实 Server，
// 连接每个测试自己启动的隔离 Redis，并真的消费消息。
//
// 顺序与生产一致：NewAsynqServer 先注册 handler、再 StartAllTask，最后由 kratos
// app 启动 transport.Server（Scheduler 只在 Start 时才运行，这是 v1.3.14 的既有行为，
// 未经启动的 Scheduler 只登记调度项、不会产出消息——本用例因此把 Start 也跑起来）。
package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	taskV1 "go-wind-admin/api/gen/go/task/service/v1"
	"go-wind-admin/app/admin/service/tests/testutil"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	asynqServer "go-wind-admin/pkg/localdeps/kratos-transport/transport/asynq"
	"go-wind-admin/pkg/task"
)

// taskRedisURI starts an isolated Redis owned by this test. It never uses a
// developer or shared Redis instance.
func taskRedisURI(t *testing.T) string {
	t.Helper()
	redis := miniredis.RunT(t)
	return "redis://" + redis.Addr() + "/0"
}

// acceptedPayload 是业务任务载荷在队列往返后的类型化形态：seed 行写入
// {"marker":"t11-marker"}，handler 收到的必须就是它。
type acceptedPayload struct {
	Marker string `json:"marker"`
}

// queueProcessed 读取 default 队列的当日已处理计数，作为队列侧的真实证据。
func queueProcessed(t *testing.T, insp *asynq.Inspector) int {
	t.Helper()
	info, err := insp.GetQueueInfo("default")
	require.NoError(t, err, "读取 default 队列统计失败")
	return info.Processed
}

// acceptRecorder 记录每个 typeName 的真实投递次数与收到的载荷。
// handler 运行在 asynq 消费协程里，因此只加锁记账，绝不调用 testing 方法。
type acceptRecorder struct {
	mu      sync.Mutex
	counts  map[string]int
	markers map[string][]string
	firings chan string
}

func newAcceptRecorder() *acceptRecorder {
	return &acceptRecorder{
		counts:  map[string]int{},
		markers: map[string][]string{},
		firings: make(chan string, 512),
	}
}

func (r *acceptRecorder) handler(typeName string) func(string, *acceptedPayload) error {
	return func(_ string, payload *acceptedPayload) error {
		r.mu.Lock()
		r.counts[typeName]++
		if payload != nil {
			r.markers[typeName] = append(r.markers[typeName], payload.Marker)
		}
		r.mu.Unlock()
		select {
		case r.firings <- typeName:
		default:
		}
		return nil
	}
}

func (r *acceptRecorder) count(typeName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[typeName]
}

func (r *acceptRecorder) seenMarkers(typeName string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.markers[typeName]...)
}

// seedAcceptedTaskRow 经 repo 直落一条带载荷的任务行（平台上下文，绕过服务层校验）。
func seedAcceptedTaskRow(t *testing.T, svc *TaskService, ctx context.Context, tenantID uint32,
	typeName string, typ taskV1.Task_Type, cron, payload string) {
	t.Helper()
	_, err := svc.taskRepo.Create(ctx, &taskV1.CreateTaskRequest{
		Data: &taskV1.Task{
			TenantId:    trans.Ptr(tenantID),
			Type:        trans.Ptr(typ),
			TypeName:    trans.Ptr(typeName),
			CronSpec:    trans.Ptr(cron),
			Enable:      trans.Ptr(true),
			Remark:      trans.Ptr("T11 真实队列验收行"),
			TaskPayload: trans.Ptr(payload),
		},
	})
	require.NoError(t, err, "落任务行 %s 应成功", typeName)
}

// TestTaskService_RealAsynqSchedulerLifecycle 验证业务装配下的真实端到端生命周期：
// 一次性任务入队并被消费、周期项由真实调度器持续产出、重启重建调度项并继续产出、
// 全量停止后不再产出且注册表清空。
func TestTaskService_RealAsynqSchedulerLifecycle(t *testing.T) {
	uri := taskRedisURI(t)
	ctx := context.Background()
	sysCtx := testutil.NewSystemViewerCtx(ctx)

	const (
		periodicType = "t11_service_real_periodic"
		delayType    = "t11_service_real_delay"
		marker       = "t11-marker"
	)

	srv := asynqServer.NewServer(
		asynqServer.WithRedisURI(uri),
		asynqServer.WithQueues(map[string]int32{"default": 1}),
		asynqServer.WithConcurrency(1),
		asynqServer.WithShutdownTimeout(5*time.Second),
		asynqServer.WithEnableKeepAlive(false),
	)

	rec := newAcceptRecorder()
	svc, _ := newTaskServiceForTest(t, nil, false)

	// 注册顺序与生产 NewAsynqServer 一致：调度器启动前完成全部 handler 登记。
	require.NoError(t, asynqServer.RegisterSubscriber(srv, periodicType, rec.handler(periodicType)))
	require.NoError(t, asynqServer.RegisterSubscriber(srv, delayType, rec.handler(delayType)))
	require.NoError(t, asynqServer.RegisterSubscriber(srv, task.TenantExpiryScanTaskType, svc.AsyncTenantExpiryScan))
	require.NoError(t, asynqServer.RegisterSubscriber(srv, task.AuditLogArchiveTaskType, svc.AsyncAuditLogArchive))
	svc.RegisterTaskScheduler(srv)

	seedAcceptedTaskRow(t, svc, sysCtx, 42, periodicType, taskV1.Task_PERIODIC, "@every 400ms",
		`{"marker":"`+marker+`"}`)
	seedAcceptedTaskRow(t, svc, sysCtx, 42, delayType, taskV1.Task_DELAY, "",
		`{"marker":"`+marker+`"}`)

	// 启动调度项（此时 Scheduler 尚未运行，登记的是待 Start 后生效的调度项）。
	_, err := svc.StartAllTask(sysCtx, &emptypb.Empty{})
	require.NoError(t, err, "StartAllTask 应成功")

	entryID := srv.QueryPeriodicTaskEntryID(periodicType)
	require.NotEmpty(t, entryID, "周期任务 entryID 未登记")
	assert.NotEmpty(t, srv.QueryPeriodicTaskEntryID(task.TenantExpiryScanTaskType), "系统级到期扫描应随 startAllTask 注册")
	assert.NotEmpty(t, srv.QueryPeriodicTaskEntryID(task.AuditLogArchiveTaskType), "系统级审计归档应随 startAllTask 注册")

	// 真实消费端：Start 之后调度器才开始产出消息，处理器才会被调用。
	connOpt, err := asynq.ParseRedisURI(uri)
	require.NoError(t, err)
	insp := asynq.NewInspector(connOpt)
	t.Cleanup(func() { require.NoError(t, insp.Close()) })

	processedBefore := queueProcessed(t, insp)

	returns := make(chan error, 1)
	go func() { returns <- srv.Start(ctx) }()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, srv.Stop(stopCtx), "Stop 必须成功")
		select {
		case err := <-returns:
			require.NoError(t, err, "Start 必须在 Stop 后返回")
		case <-time.After(30 * time.Second):
			t.Error("Start 的协程在 Stop 之后仍未返回")
		}
	})

	// 一次性任务：入队 → 被真实消费 → 载荷原样回到类型化 handler。
	require.Eventually(t, func() bool { return rec.count(delayType) >= 1 },
		30*time.Second, 100*time.Millisecond, "DELAY 任务没有经真实队列被消费")
	assert.Contains(t, rec.seenMarkers(delayType), marker, "一次性任务载荷在队列往返后丢失")

	// 周期任务：真实调度器持续产出。
	require.Eventually(t, func() bool { return rec.count(periodicType) >= 2 },
		30*time.Second, 100*time.Millisecond, "周期任务未由真实调度器连续产出")
	assert.Contains(t, rec.seenMarkers(periodicType), marker, "周期任务载荷在队列往返后丢失")

	// Inspector 侧也看得到真实队列痕迹，不是只靠内存记账。完成集只在任务带
	// Retention 时才保留（业务路径不设置），因此读队列统计里的当日已处理计数，
	// 并与启动前的基线比较：同一实例上跑过别的用例时绝对值没有意义。
	require.Eventually(t, func() bool {
		return queueProcessed(t, insp) >= processedBefore+2
	}, 15*time.Second, 200*time.Millisecond, "队列统计里没有业务任务被处理的增量")

	// 重启：全量注销后重建调度项，且产出继续。
	resp, err := svc.RestartAllTask(sysCtx, &emptypb.Empty{})
	require.NoError(t, err, "RestartAllTask 应成功")
	assert.GreaterOrEqual(t, resp.GetCount(), int32(2), "两条任务应各成功开启一次")

	restartedID := srv.QueryPeriodicTaskEntryID(periodicType)
	require.NotEmpty(t, restartedID, "重启后周期任务 entryID 应重新登记")
	assert.NotEqual(t, entryID, restartedID, "重启应产生新的调度项，而不是复用旧 entryID")

	before := rec.count(periodicType)
	require.Eventually(t, func() bool { return rec.count(periodicType) > before },
		30*time.Second, 100*time.Millisecond, "重启后真实调度器没有继续产出周期消息")

	// 停止：注册表清空且不再有新的投递。
	_, err = svc.StopAllTask(sysCtx, &emptypb.Empty{})
	require.NoError(t, err, "StopAllTask 应成功")
	assert.Empty(t, srv.QueryPeriodicTaskEntryID(periodicType), "停止后周期任务注册表应清空")
	assert.Empty(t, srv.QueryPeriodicTaskEntryID(task.TenantExpiryScanTaskType), "停止后系统级任务注册表应清空")

	// asynq 的关停是异步的，而停止之前已入队的在途投递可能因为 CPU 竞争远晚于调度时刻
	// 到达（整包并发下实测排空延迟可超过一个采样窗口），所以这里要求的是"最终静默"：
	// 连续 5 个 500ms 采样不再有新增。周期是 @every 400ms，因此只要调度器还在产出就不
	// 可能在 10s 时限内凑出这 2.5s 静默；静默确认后再等一个窗口，确认没有恢复 trickle。
	quietSamples := 0
	const quietNeeded = 5
	last := rec.count(periodicType)
	deadline := time.Now().Add(10 * time.Second)
	for quietSamples < quietNeeded && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if now := rec.count(periodicType); now == last {
			quietSamples++
		} else {
			quietSamples, last = 0, now
		}
	}
	require.Equal(t, quietNeeded, quietSamples,
		"StopAllTask 后调度器仍在产出周期消息：%d 次采样内未静默", quietNeeded+1)
	settled := last
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, settled, rec.count(periodicType), "静默之后周期消息又重新开始产出")

	// 停止只注销调度项，不伪造删除已入队的一次性消息（记录既有语义）。
	assert.GreaterOrEqual(t, rec.count(delayType), 1, "已消费的一次性任务计数不应因停止而消失")
}
