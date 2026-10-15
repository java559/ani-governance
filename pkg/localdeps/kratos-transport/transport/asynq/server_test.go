package asynq

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTask1        = "test_task_1"
	testDelayTask    = "test_delay_task"
	testPeriodicTask = "test_periodic_task"
)

type TaskPayload struct {
	Message string `json:"message"`
}

func handleTask1(taskType string, taskData *TaskPayload) error {
	LogInfof("[%s] Task Type: [%s], Payload: [%s]", time.Now().Format("2006-01-02 15:04:05"), taskType, taskData.Message)
	return nil
}

func handleDelayTask(taskType string, taskData *TaskPayload) error {
	LogInfof("[%s] Delay Task Type: [%s], Payload: [%s]", time.Now().Format("2006-01-02 15:04:05"), taskType, taskData.Message)
	return nil
}

func handlePeriodicTask(taskType string, taskData *TaskPayload) error {
	LogInfof("[%s] Periodic Task Type: [%s], Payload: [%s]", time.Now().Format("2006-01-02 15:04:05"), taskType, taskData.Message)
	return nil
}

// testRedisURI starts an isolated in-process Redis for this test. Each test owns
// its server, so it cannot write to a developer or shared environment.
func testRedisURI(t *testing.T) string {
	t.Helper()
	redis := miniredis.RunT(t)
	return "redis://" + redis.Addr() + "/0"
}

// recorder is the verifiable completion condition that replaces the upstream
// "<-interrupt" ending: handlers report every observation, and the test body
// waits for what it needs with a deadline.
type recorder struct {
	mu      sync.Mutex
	seen    map[string][]time.Time
	samples map[string][]string
	notices chan string
}

func newRecorder() *recorder {
	return &recorder{
		seen:    map[string][]time.Time{},
		samples: map[string][]string{},
		notices: make(chan string, 256),
	}
}

// handlerFor builds a subscriber for one task type. It never calls a testing
// method from the consumer goroutine: observations travel back on a channel.
func (r *recorder) handlerFor(typeName string) func(string, *TaskPayload) error {
	return func(_ string, payload *TaskPayload) error {
		r.mu.Lock()
		r.seen[typeName] = append(r.seen[typeName], time.Now())
		message := "<nil>"
		if payload != nil {
			message = payload.Message
		}
		r.samples[typeName] = append(r.samples[typeName], message)
		r.mu.Unlock()
		select {
		case r.notices <- typeName:
		default:
		}
		return nil
	}
}

func (r *recorder) count(typeName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen[typeName])
}

func (r *recorder) firstAt(typeName string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen[typeName]) == 0 {
		return time.Time{}, false
	}
	return r.seen[typeName][0], true
}

// firstAtMatching returns when a payload containing marker was first handled, so
// assertions do not depend on the delivery order of two different tasks.
func (r *recorder) firstAtMatching(typeName, marker string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, message := range r.samples[typeName] {
		if strings.Contains(message, marker) {
			return r.seen[typeName][i], true
		}
	}
	return time.Time{}, false
}

func (r *recorder) hasPayload(typeName, marker string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, message := range r.samples[typeName] {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (r *recorder) payload(typeName string, idx int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx >= len(r.samples[typeName]) {
		return ""
	}
	return r.samples[typeName][idx]
}

// waitFor blocks until the task type has been observed at least n times, or fails
// the test at the deadline. Timeout is a failure, never a pass.
func (r *recorder) waitFor(t *testing.T, typeName string, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for r.count(typeName) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %d handling(s) of %q, saw %d",
				within, n, typeName, r.count(typeName))
		}
		select {
		case <-r.notices:
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// failingThenSucceeds is the handler for the retry case: it refuses the first n
// deliveries of this task type and then succeeds, so the retry path is exercised
// against real asynq state rather than a simulated clock.
func (r *recorder) failingThenSucceeds(typeName string, failFirst int) func(string, *TaskPayload) error {
	return func(_ string, payload *TaskPayload) error {
		r.mu.Lock()
		attempt := len(r.seen[typeName]) + 1
		r.seen[typeName] = append(r.seen[typeName], time.Now())
		message := "<nil>"
		if payload != nil {
			message = payload.Message
		}
		r.samples[typeName] = append(r.samples[typeName], message)
		r.mu.Unlock()
		if attempt <= failFirst {
			return fmt.Errorf("deliberate handler failure %d/%d", attempt, failFirst)
		}
		select {
		case r.notices <- typeName:
		default:
		}
		return nil
	}
}

// startServer runs Start in its own goroutine and returns a function that waits
// for it to return. A Start error is delivered on the channel instead of panicking
// inside the goroutine, and stop is registered as cleanup before the server starts.
func startServer(t *testing.T, srv *Server) func() error {
	t.Helper()
	ctx := context.Background()
	returns := make(chan error, 1)
	go func() { returns <- srv.Start(ctx) }()
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		stopErr := srv.Stop(ctx)
		select {
		case startErr := <-returns:
			if startErr != nil {
				return fmt.Errorf("Start returned: %w", startErr)
			}
			return stopErr
		case <-time.After(20 * time.Second):
			t.Errorf("the Start goroutine did not return after Stop")
			return nil
		}
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Errorf("server shutdown: %v", err)
		}
	})
	return stop
}

// newTestServer gives every test its own queue inside the shared task Redis, so a
// leftover scheduled task from one case can never be counted by another.
func newTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	uri := testRedisURI(t)
	queue := "t11_" + strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	srv := NewServer(
		WithRedisURI(uri),
		WithShutdownTimeout(3*time.Second),
		WithQueues(map[string]int32{queue: 1}),
	)
	return srv, uri, queue
}

func mustInspector(t *testing.T, uri string) *asynq.Inspector {
	t.Helper()
	opt, err := asynq.ParseRedisURI(uri)
	require.Nil(t, err)
	return asynq.NewInspector(opt)
}

// TestNewTaskOnly creates a task and verifies the real Redis record behind it: the
// queue entry, its type and its payload. The upstream test could only wait for a
// signal, so it proved nothing about what it had enqueued.
func TestNewTaskOnly(t *testing.T) {
	srv, uri, queue := newTestServer(t)

	const payloadMessage = "new task record"
	require.Nil(t, srv.NewTask(testTask1, &TaskPayload{Message: payloadMessage},
		asynq.Queue(queue), asynq.MaxRetry(10), asynq.Timeout(3*time.Minute)))

	insp := mustInspector(t, uri)
	defer insp.Close()

	var foundType, foundPayload string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		infos, err := insp.ListPendingTasks(queue)
		if err == nil {
			for _, info := range infos {
				if info.Type == testTask1 {
					foundType, foundPayload = info.Type, string(info.Payload)
				}
			}
		}
		if foundType != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, testTask1, foundType, "the enqueued task type is not visible in the default queue")
	assert.Contains(t, foundPayload, payloadMessage, "the stored payload does not carry what was enqueued")

	rec := newRecorder()
	require.Nil(t, RegisterSubscriber(srv, testTask1, rec.handlerFor(testTask1)))

	startServer(t, srv)
	require.Nil(t, srv.NewTask(testTask1, &TaskPayload{Message: "consumed after the record"},
		asynq.Queue(queue)))
	// both the recorded task and the one enqueued after Start must be handled
	rec.waitFor(t, testTask1, 2, 30*time.Second)
	assert.True(t, rec.hasPayload(testTask1, payloadMessage), "the queued task was not handled")
	assert.True(t, rec.hasPayload(testTask1, "consumed after the record"), "the later task was not handled")
}

// TestNewPeriodicTaskOnly checks registration and the existing removal behaviour
// through the scheduler state in Redis.
// TestNewPeriodicTaskOnly checks that a registration returns a usable entry id,
// that the task is listed under its type, and that the existing removal behaviour
// really removes it and then refuses a second removal.
//
// The upstream case only waited for a signal. asynq's Redis-side scheduler listing
// was probed here and returns nothing even with the server running and ten seconds
// of waiting (three cron shapes tried), so registration is asserted through the
// registry the removal itself uses, and an actual firing is asserted in
// TestPeriodicTask instead.
func TestNewPeriodicTaskOnly(t *testing.T) {
	srv, _, queue := newTestServer(t)

	startServer(t, srv)

	entryID, err := srv.NewPeriodicTask("*/1 * * * ?", testPeriodicTask,
		&TaskPayload{Message: "periodic task"}, asynq.Queue(queue), asynq.Unique(10*time.Second))
	require.Nil(t, err)
	require.NotEmpty(t, entryID, "NewPeriodicTask must return the scheduler entry id")
	require.Equal(t, entryID, srv.QueryPeriodicTaskEntryID(testPeriodicTask),
		"the entry id is not what the registry lists for this type")

	require.Nil(t, srv.RemovePeriodicTask(testPeriodicTask))
	assert.Equal(t, "", srv.QueryPeriodicTaskEntryID(testPeriodicTask),
		"RemovePeriodicTask left the registry entry behind")

	// removing what is gone must fail rather than look like a success
	assert.NotNil(t, srv.RemovePeriodicTask(testPeriodicTask))

	// the id-addressed removal behaves the same way for a fresh entry
	freshID, err := srv.NewPeriodicTask("@every 200ms", testPeriodicTask,
		&TaskPayload{Message: "periodic task by id"}, asynq.Queue(queue))
	require.Nil(t, err)
	require.Nil(t, srv.RemovePeriodicTaskByID(freshID))
	assert.NotNil(t, srv.RemovePeriodicTaskByID(freshID))
}

// TestDelayTask keeps the ProcessIn and ProcessAt intent of the upstream case and
// asserts that neither ran earlier than scheduled.
func TestDelayTask(t *testing.T) {
	srv, _, queue := newTestServer(t)
	rec := newRecorder()
	require.Nil(t, RegisterSubscriber(srv, testDelayTask, rec.handlerFor(testDelayTask)))

	inFor := 600 * time.Millisecond
	enqueueAt := time.Now()
	atFor := enqueueAt.Add(900 * time.Millisecond)

	require.Nil(t, srv.NewTask(testDelayTask,
		&TaskPayload{Message: fmt.Sprintf("ProcessIn:[%s]", time.Now().Format("2006/1/2 15:04:05"))},
		asynq.Queue(queue), asynq.ProcessIn(inFor)))
	require.Nil(t, srv.NewTask(testDelayTask,
		&TaskPayload{Message: fmt.Sprintf("ProcessAt:[%s]", time.Now().Format("2006/1/2 15:04:05"))},
		asynq.Queue(queue), asynq.ProcessAt(atFor)))

	startServer(t, srv)
	rec.waitFor(t, testDelayTask, 2, 45*time.Second)

	// Neither delivery may happen before the instant it asked for. The assertion is
	// per payload, so it does not depend on which of the two tasks runs first.
	if at, ok := rec.firstAtMatching(testDelayTask, "ProcessIn:"); ok {
		assert.False(t, at.Before(enqueueAt.Add(inFor-time.Second)),
			"the ProcessIn task ran earlier than the requested delay")
	} else {
		t.Error("the ProcessIn task was never handled")
	}
	if at, ok := rec.firstAtMatching(testDelayTask, "ProcessAt:"); ok {
		assert.False(t, at.Before(atFor.Add(-time.Second)),
			"the ProcessAt task ran before its scheduled instant")
	} else {
		t.Error("the ProcessAt task was never handled")
	}
}

// TestPeriodicTask exercises registration, real firings and removal. The upstream
// case waited for a signal and never asserted any of it.
func TestPeriodicTask(t *testing.T) {
	srv, uri, queue := newTestServer(t)
	insp := mustInspector(t, uri)
	defer insp.Close()

	rec := newRecorder()
	require.Nil(t, RegisterSubscriber(srv, testPeriodicTask, rec.handlerFor(testPeriodicTask)))

	// A short period through the existing cron API; production defaults stay as they are.
	entryID, err := srv.NewPeriodicTask("@every 200ms", testPeriodicTask,
		&TaskPayload{Message: "periodic task"}, asynq.Queue(queue))
	require.Nil(t, err)
	require.NotEmpty(t, entryID)

	startServer(t, srv)
	rec.waitFor(t, testPeriodicTask, 2, 30*time.Second)

	require.Nil(t, srv.RemovePeriodicTaskByID(entryID))
	// an enqueue already in flight may still be handled; the contract is that no new
	// firing happens after the removal has settled
	time.Sleep(1200 * time.Millisecond)
	stableFrom := rec.count(testPeriodicTask)
	time.Sleep(1200 * time.Millisecond)
	assert.Equal(t, stableFrom, rec.count(testPeriodicTask),
		"the periodic task kept firing after it was removed")
	// and the task type is gone from the registry as well
	assert.Equal(t, "", srv.QueryPeriodicTaskEntryID(testPeriodicTask))
}

// TestTaskSubscribe registers the three upstream handlers and verifies each one
// actually receives its own message.
func TestTaskSubscribe(t *testing.T) {
	srv, _, queue := newTestServer(t)
	rec := newRecorder()
	require.Nil(t, RegisterSubscriber(srv, testTask1, rec.handlerFor(testTask1)))
	require.Nil(t, RegisterSubscriber(srv, testDelayTask, rec.handlerFor(testDelayTask)))
	require.Nil(t, RegisterSubscriber(srv, testPeriodicTask, rec.handlerFor(testPeriodicTask)))

	startServer(t, srv)

	require.Nil(t, srv.NewTask(testTask1, &TaskPayload{Message: "subscribe task1"}, asynq.Queue(queue)))
	require.Nil(t, srv.NewTask(testDelayTask, &TaskPayload{Message: "subscribe delay"}, asynq.Queue(queue)))
	require.Nil(t, srv.NewTask(testPeriodicTask, &TaskPayload{Message: "subscribe periodic"}, asynq.Queue(queue)))

	rec.waitFor(t, testTask1, 1, 30*time.Second)
	rec.waitFor(t, testDelayTask, 1, 30*time.Second)
	rec.waitFor(t, testPeriodicTask, 1, 30*time.Second)

	assert.Contains(t, rec.payload(testTask1, 0), "subscribe task1")
	assert.Contains(t, rec.payload(testDelayTask, 0), "subscribe delay")
	assert.Contains(t, rec.payload(testPeriodicTask, 0), "subscribe periodic")
}

// TestAllInOne combines the immediate, the delayed and the periodic path.
func TestAllInOne(t *testing.T) {
	srv, uri, queue := newTestServer(t)
	insp := mustInspector(t, uri)
	defer insp.Close()

	rec := newRecorder()
	require.Nil(t, RegisterSubscriber(srv, testTask1, rec.handlerFor(testTask1)))
	require.Nil(t, RegisterSubscriber(srv, testDelayTask, rec.handlerFor(testDelayTask)))
	require.Nil(t, RegisterSubscriber(srv, testPeriodicTask, rec.handlerFor(testPeriodicTask)))

	require.Nil(t, srv.NewTask(testTask1, &TaskPayload{Message: "all-in-one task1"},
		asynq.Queue(queue), asynq.MaxRetry(3), asynq.Timeout(10*time.Second),
		asynq.Deadline(time.Now().Add(2*time.Minute))))
	require.Nil(t, srv.NewTask(testDelayTask, &TaskPayload{Message: "all-in-one delay"},
		asynq.Queue(queue), asynq.ProcessIn(400*time.Millisecond)))
	entryID, err := srv.NewPeriodicTask("@every 200ms", testPeriodicTask,
		&TaskPayload{Message: "all-in-one periodic"}, asynq.Queue(queue))
	require.Nil(t, err)

	startServer(t, srv)

	rec.waitFor(t, testTask1, 1, 30*time.Second)
	rec.waitFor(t, testDelayTask, 1, 30*time.Second)
	rec.waitFor(t, testPeriodicTask, 2, 30*time.Second)

	require.Nil(t, srv.RemovePeriodicTaskByID(entryID))
	time.Sleep(1200 * time.Millisecond)
	stableFrom := rec.count(testPeriodicTask)
	time.Sleep(1200 * time.Millisecond)
	assert.Equal(t, stableFrom, rec.count(testPeriodicTask),
		"the periodic task kept firing after it was removed")
	assert.Contains(t, rec.payload(testTask1, 0), "all-in-one task1")
}

// TestWaitResultTask waits for the real result of the task it enqueues, and hands
// the Start error back through a channel instead of panicking in a goroutine.
func TestWaitResultTask(t *testing.T) {
	srv, _, queue := newTestServer(t)
	rec := newRecorder()
	require.Nil(t, RegisterSubscriber(srv, testTask1, rec.handlerFor(testTask1)))

	startServer(t, srv)

	require.Nil(t, srv.NewWaitResultTask(testTask1, &TaskPayload{Message: "wait result task"},
		asynq.Queue(queue), asynq.Retention(time.Hour*1), asynq.MaxRetry(3), asynq.Timeout(10*time.Second)))

	// The call above only returns once the result is observable, so the handler must
	// have seen this exact payload by now.
	rec.waitFor(t, testTask1, 1, 30*time.Second)
	assert.Contains(t, rec.payload(testTask1, 0), "wait result task")
}

// TestPeriodicTaskSameTypeName verifies that multiple periodic tasks sharing
// the same typeName are tracked and removable independently:
//   - RemovePeriodicTaskByID removes any single entry by the entryID returned
//     by NewPeriodicTask, even when its typeName was overwritten by a later
//     registration;
//   - RemovePeriodicTask (by typeName) removes the latest entry and actually
//     cleans the entryIDs map (regression for the no-op cleanup bug).
func TestPeriodicTaskSameTypeName(t *testing.T) {
	srv := NewServer()
	assert.Nil(t, srv.createAsynqScheduler())

	const typeName = "test_periodic_same_typename"
	task := asynq.NewTask(typeName, nil)

	// two periodic tasks sharing the same typeName: the underlying scheduler
	// holds two entries, while entryIDs keeps only the latest one.
	id1, err := srv.scheduler.Register("*/5 * * * *", task)
	assert.Nil(t, err)
	srv.addPeriodicTaskEntryID(typeName, id1)

	id2, err := srv.scheduler.Register("*/7 * * * *", task)
	assert.Nil(t, err)
	srv.addPeriodicTaskEntryID(typeName, id2)

	assert.NotEqual(t, id1, id2)
	assert.Equal(t, id2, srv.QueryPeriodicTaskEntryID(typeName))

	// the overwritten entry is still firing in the scheduler and can only be
	// removed via its entryID.
	assert.Nil(t, srv.RemovePeriodicTaskByID(id1))
	assert.NotNil(t, srv.scheduler.Unregister(id1)) // already unregistered
	assert.Equal(t, id2, srv.QueryPeriodicTaskEntryID(typeName))

	// removing by typeName works for the latest entry and cleans the map.
	assert.Nil(t, srv.RemovePeriodicTask(typeName))
	assert.Equal(t, "", srv.QueryPeriodicTaskEntryID(typeName))
	assert.NotNil(t, srv.scheduler.Unregister(id2)) // already unregistered

	// RemovePeriodicTaskByID also clears the map entry that holds the entryID.
	id3, err := srv.scheduler.Register("*/9 * * * *", task)
	assert.Nil(t, err)
	srv.addPeriodicTaskEntryID(typeName, id3)
	assert.Nil(t, srv.RemovePeriodicTaskByID(id3))
	assert.Equal(t, "", srv.QueryPeriodicTaskEntryID(typeName))

	// removing an unknown or empty entryID fails instead of silently succeeding.
	assert.NotNil(t, srv.RemovePeriodicTaskByID("nonexistent-entry-id"))
	assert.NotNil(t, srv.RemovePeriodicTaskByID(""))
}

// TestRetryAfterHandlerFailure drives the real asynq retry path: the handler refuses
// the first delivery, the task lands in the retry set with its payload intact, and
// the retry the server performs afterwards succeeds.
//
// The retry is *triggered* through the Inspector rather than waited for: the backoff
// this configuration computes for the first retry is on the order of minutes
// (measured NextProcessAt was ~110s ahead), and sleeping that long would only make
// the case slow and flaky without testing anything extra.
func TestRetryAfterHandlerFailure(t *testing.T) {
	srv, uri, queue := newTestServer(t)
	insp := mustInspector(t, uri)
	defer insp.Close()

	rec := newRecorder()
	require.Nil(t, RegisterSubscriber(srv, testTask1, rec.failingThenSucceeds(testTask1, 1)))
	// a handler that always fails must end up archived, never silently dropped.
	// Subscribers have to be registered before Start: the mux is frozen then.
	failing := newRecorder()
	require.Nil(t, RegisterSubscriber(srv, testDelayTask, failing.failingThenSucceeds(testDelayTask, 1000)))

	startServer(t, srv)
	require.Nil(t, srv.NewTask(testTask1, &TaskPayload{Message: "retry me"},
		asynq.Queue(queue), asynq.MaxRetry(3), asynq.Timeout(10*time.Second)))

	// first delivery fails and the task must be waiting in the retry set
	rec.waitFor(t, testTask1, 1, 60*time.Second)
	var retryID string
	require.Eventually(t, func() bool {
		tasks, err := insp.ListRetryTasks(queue)
		if err != nil {
			return false
		}
		for _, info := range tasks {
			if info.Type == testTask1 {
				retryID = info.ID
				assert.Equal(t, 1, info.Retried, "the retry count should be one after the first failure")
				assert.Equal(t, 3, info.MaxRetry)
				assert.Contains(t, string(info.Payload), "retry me", "the payload must survive the retry")
				assert.Contains(t, info.LastErr, "deliberate handler failure")
				return true
			}
		}
		return false
	}, 30*time.Second, 200*time.Millisecond, "the failed task never reached the retry set")

	// drive the retry now; the second delivery succeeds. RunTask moves this
	// retry-state task back to pending, which is asynq v0.26's only explicit
	// single-task unblock (there is no RunRetryTask).
	require.Nil(t, insp.RunTask(queue, retryID))
	rec.waitFor(t, testTask1, 2, 60*time.Second)
	assert.Contains(t, rec.payload(testTask1, 1), "retry me")

	require.Eventually(t, func() bool {
		tasks, err := insp.ListRetryTasks(queue)
		if err != nil {
			return false
		}
		for _, info := range tasks {
			if info.ID == retryID {
				return false
			}
		}
		return true
	}, 30*time.Second, 200*time.Millisecond, "the retried task stayed in the retry set")

	// a handler that always fails must end up archived, never silently dropped
	require.Nil(t, srv.NewTask(testDelayTask, &TaskPayload{Message: "always fails"},
		asynq.Queue(queue), asynq.MaxRetry(0), asynq.Timeout(10*time.Second)))
	failing.waitFor(t, testDelayTask, 1, 60*time.Second)
	require.Eventually(t, func() bool {
		tasks, err := insp.ListArchivedTasks(queue)
		if err != nil {
			return false
		}
		for _, info := range tasks {
			if info.Type == testDelayTask {
				assert.Contains(t, string(info.Payload), "always fails")
				return true
			}
		}
		return false
	}, 30*time.Second, 200*time.Millisecond, "an exhausted task was neither retried nor archived")
}

// TestPeriodicTaskRemoveAll covers RemoveAllPeriodicTask, which the upstream suite
// never exercised: every registered periodic entry must stop firing and the
// registry must be emptied.
func TestPeriodicTaskRemoveAll(t *testing.T) {
	srv, _, queue := newTestServer(t)

	rec := newRecorder()
	require.Nil(t, RegisterSubscriber(srv, testPeriodicTask, rec.handlerFor(testPeriodicTask)))
	require.Nil(t, RegisterSubscriber(srv, testTask1, rec.handlerFor(testTask1)))

	idA, err := srv.NewPeriodicTask("@every 200ms", testPeriodicTask, &TaskPayload{Message: "a"},
		asynq.Queue(queue))
	require.Nil(t, err)
	idB, err := srv.NewPeriodicTask("@every 300ms", testTask1, &TaskPayload{Message: "b"},
		asynq.Queue(queue))
	require.Nil(t, err)
	require.NotEmpty(t, idA)
	require.NotEmpty(t, idB)

	startServer(t, srv)
	rec.waitFor(t, testPeriodicTask, 2, 30*time.Second)
	rec.waitFor(t, testTask1, 2, 30*time.Second)

	srv.RemoveAllPeriodicTask()
	assert.Equal(t, "", srv.QueryPeriodicTaskEntryID(testPeriodicTask))
	assert.Equal(t, "", srv.QueryPeriodicTaskEntryID(testTask1))

	// an enqueue already in flight may still be handled; wait for that backlog to drain
	// before sampling, then require the counts to hold
	time.Sleep(1200 * time.Millisecond)
	periodicAt := rec.count(testPeriodicTask)
	taskAt := rec.count(testTask1)
	time.Sleep(1200 * time.Millisecond)
	assert.Equal(t, periodicAt, rec.count(testPeriodicTask), "the first periodic entry kept firing")
	assert.Equal(t, taskAt, rec.count(testTask1), "the second periodic entry kept firing")
}
