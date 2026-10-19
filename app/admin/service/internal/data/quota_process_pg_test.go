//go:build quota_pg

package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

// Only the test connection installs this tracer. Production Ent transactions
// run unchanged. Start blocks before COMMIT reaches PG; End blocks only after PG
// acknowledges successful COMMIT and before the repository returns its response.
type quotaCommitTrace struct{ point, marker string }
type quotaCommitContextKey struct{}

func (p *quotaCommitTrace) pause() {
	if err := os.WriteFile(p.marker, []byte(p.point), 0600); err != nil {
		panic(err)
	}
	select {}
}
func (p *quotaCommitTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	commit := strings.EqualFold(strings.TrimSpace(d.SQL), "commit")
	if commit && p.point == "before" {
		p.pause()
	}
	return context.WithValue(ctx, quotaCommitContextKey{}, commit)
}
func (p *quotaCommitTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	if ctx.Value(quotaCommitContextKey{}) == true && d.Err == nil && p.point == "after" {
		p.pause()
	}
}

type quotaFaultSpec struct {
	Action, Point, Marker, Ready, Go, Result string
	Input                                    *QuotaOccupyInput
	Delete                                   *QuotaDeleteInput
	Release                                  *QuotaReleaseInput
	OperationID                              string
	Revision, Generation                     int64
}
type quotaFaultResult struct {
	Occupy  *QuotaOccupyResult
	Delete  *QuotaDeleteResult
	Release []QuotaReleaseResult
	Claims  []ClaimedOperation
	Ack     bool
}

func TestQuotaProcessChild(t *testing.T) {
	file := os.Getenv("QUOTA_PROCESS_SPEC")
	if file == "" {
		t.Skip("OS subprocess helper")
	}
	var s quotaFaultSpec
	b, e := os.ReadFile(file)
	require.NoError(t, e)
	require.NoError(t, json.Unmarshal(b, &s))
	cfg, e := pgx.ParseConfig(os.Getenv("ANI_TEST_DATABASE_DSN"))
	require.NoError(t, e)
	cfg.Tracer = &quotaCommitTrace{s.Point, s.Marker}
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	drv := entsql.OpenDB("postgres", db)
	client := ent.NewClient(ent.Driver(drv))
	repo := newTestRepo(entCrud.NewEntClient(client, drv))
	if s.Ready != "" {
		require.NoError(t, os.WriteFile(s.Ready, []byte("ready"), 0600))
		quotaWaitFile(t, s.Go)
	}
	var out quotaFaultResult
	ctx := context.Background()
	switch s.Action {
	case "occupy":
		out.Occupy, e = repo.Occupy(ctx, s.Input)
	case "cancel", "delete":
		out.Delete, e = repo.CreateDeleteOperation(ctx, s.Delete)
	case "release":
		out.Release, e = repo.Release(ctx, s.Release)
	case "sync":
		out.Ack, e = repo.AckGpuUsageSync(ctx, s.Input.TenantID, s.OperationID, s.Revision, s.Generation)
	case "claim":
		out.Claims, e = repo.ClaimDispatchable(ctx, "os-worker-"+uuid.NewString(), time.Minute, 10)
	default:
		t.Fatal("unknown fault action", s.Action)
	}
	require.NoError(t, e)
	b, e = json.Marshal(out)
	require.NoError(t, e)
	require.NoError(t, os.WriteFile(s.Result, b, 0600))
}

func quotaWaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, e := os.Stat(path); e == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("process barrier timeout", path)
}
func quotaFaultStart(t *testing.T, s quotaFaultSpec) (*exec.Cmd, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.json")
	s.Result = filepath.Join(dir, "result.json")
	b, e := json.Marshal(s)
	require.NoError(t, e)
	require.NoError(t, os.WriteFile(path, b, 0600))
	executable, e := os.Executable()
	require.NoError(t, e)
	cmd := exec.Command(executable, "-test.run=^TestQuotaProcessChild$", "-test.v")
	cmd.Env = append(os.Environ(), "QUOTA_PROCESS_SPEC="+path)
	log, e := os.Create(filepath.Join(dir, "child.log"))
	require.NoError(t, e)
	cmd.Stdout = log
	cmd.Stderr = log
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = log.Close() })
	return cmd, s.Result
}
func quotaFaultRead(t *testing.T, path string) quotaFaultResult {
	t.Helper()
	b, e := os.ReadFile(path)
	require.NoError(t, e)
	var out quotaFaultResult
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}
func quotaFaultRun(t *testing.T, s quotaFaultSpec) quotaFaultResult {
	t.Helper()
	s.Point = ""
	cmd, result := quotaFaultStart(t, s)
	require.NoError(t, cmd.Wait())
	return quotaFaultRead(t, result)
}

// DB-03: each repository mutation is executed in a separate OS process using the
// restricted production connection. SIGKILL precedes any result-file response.
// Release is the actual persistence transaction called by ReportQuotaRelease;
// transport certificate and callback coverage is provided by the joint suite.
func TestQuotaProcessCommitRecovery(t *testing.T) {
	c := newLedgerPGClient(t)
	ctx := context.Background()
	sys := appViewer.NewSystemViewerContext(ctx)
	var restricted bool
	require.NoError(t, c.DB().QueryRowContext(ctx, gpuRoleAndRLS).Scan(&restricted))
	require.True(t, restricted)
	for _, action := range []string{"occupy", "cancel", "delete", "release", "sync"} {
		for _, point := range []string{"before", "after"} {
			t.Run(action+"/"+point, func(t *testing.T) {
				cleanLedger(t, c)
				repo := newTestRepo(c)
				tid, pid := seedTenantPlan(t, c, "process-"+action+"-"+point, 20)
				in := gpuLedgerInput(t, c.Client(), tid, pid)
				s := quotaFaultSpec{Action: action, Point: point, Input: in, Marker: filepath.Join(t.TempDir(), "commit")}
				if action != "occupy" {
					original, e := repo.Occupy(ctx, in)
					require.NoError(t, e)
					s.OperationID = original.OperationID
					s.Delete = gpuDelete(t, in)
					if action == "delete" || action == "release" {
						rows, e := repo.ClaimDispatchable(ctx, "setup", time.Minute, 1)
						require.NoError(t, e)
						require.Len(t, rows, 1)
					}
					if action == "release" {
						_, e = repo.CreateDeleteOperation(ctx, s.Delete)
						require.NoError(t, e)
						s.Release = &QuotaReleaseInput{OwnerService: in.OwnerService, OperationID: s.OperationID, ResourceID: in.ResourceID, Complete: true, ReleaseEventID: uuid.NewString(), Reason: "RESOURCE_RELEASED", PayloadHash: "stable-release", PayloadJSON: `{"source":"controlled process test"}`}

					}
					if action == "sync" {
						require.NoError(t, repo.UpsertGpuUsageProjection(ctx, tid, s.OperationID, 1, "DECLARED", gpuProjectionPayload(in, s.OperationID, 1, "stable"), "stable"))
						rows, e := repo.ClaimGpuUsageSync(ctx, "setup", time.Minute, 1)
						require.NoError(t, e)
						require.Len(t, rows, 1)
						s.Revision = 1
						s.Generation = rows[0].LeaseGeneration
					}
				}
				cmd, result := quotaFaultStart(t, s)
				quotaWaitFile(t, s.Marker)
				require.NoError(t, cmd.Process.Kill())
				require.Error(t, cmd.Wait())
				waitStatus, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				require.True(t, ok)
				require.True(t, waitStatus.Signaled())
				require.Equal(t, syscall.SIGKILL, waitStatus.Signal())
				_, e := os.Stat(result)
				require.True(t, os.IsNotExist(e), "killed process must not return response")
				if action == "occupy" {
					_, e := repo.FindIdempotentOperation(ctx, tid, in.ActorType, in.ActorID, in.Action, in.IdempotencyKey)
					if point == "before" {
						require.True(t, ent.IsNotFound(e))
					} else {
						require.NoError(t, e)
					}
				} else if action == "cancel" || action == "delete" {
					_, e := repo.FindIdempotentOperation(ctx, tid, s.Delete.ActorType, s.Delete.ActorID, s.Delete.Action, s.Delete.IdempotencyKey)
					if point == "before" {
						require.True(t, ent.IsNotFound(e))
					} else {
						require.NoError(t, e)
					}
				} else if action == "release" {
					n, e := c.Client().QuotaReleaseReceipt.Query().Count(sys)
					require.NoError(t, e)
					if point == "before" {
						require.Zero(t, n)
					} else {
						require.Equal(t, 1, n)
					}
				} else {
					row, e := c.Client().GpuUsageSync.Query().Only(sys)
					require.NoError(t, e)
					if point == "before" {
						require.Zero(t, row.AckedRevision)
					} else {
						require.EqualValues(t, 1, row.AckedRevision)
					}
				}
				// Inspect the crash state before recovery can repair anything.
				wantOperations := 1
				if action == "occupy" && point == "before" {
					wantOperations = 0
				}
				if action == "release" || (action == "cancel" || action == "delete") && point == "after" {
					wantOperations = 2
				}
				n, e := c.Client().QuotaOperation.Query().Count(sys)
				require.NoError(t, e)
				require.Equal(t, wantOperations, n)
				n, e = c.Client().QuotaCharge.Query().Count(sys)
				require.NoError(t, e)
				if action == "occupy" && point == "before" {
					require.Zero(t, n)
				} else {
					require.Equal(t, 2, n)
				}
				crashBalances, e := repo.RecomputeInvariants(ctx, tid)
				require.NoError(t, e)
				for _, row := range crashBalances {
					require.True(t, row.Balanced)
					want := int64(10)
					if isGPUCode(row.QuotaCode) {
						want = 2
					}
					if action == "occupy" && point == "before" || point == "after" && (action == "cancel" || action == "release") {
						want = 0
					}
					require.Equal(t, want, row.OccupiedUnits)
				}
				replay := quotaFaultRun(t, s)
				switch action {
				case "occupy":
					require.Equal(t, point == "after", replay.Occupy.Replayed)
					s.OperationID = replay.Occupy.OperationID
				case "cancel", "delete":
					require.Equal(t, point == "after", replay.Delete.Replayed)
					require.Equal(t, action == "cancel", replay.Delete.LocalCanceled)
				case "release":
					require.Len(t, replay.Release, 2)
				case "sync": // A previously committed ACK is durable even if same-generation replay is rejected.
					row, e := c.Client().GpuUsageSync.Query().Only(sys)
					require.NoError(t, e)
					require.EqualValues(t, 1, row.AckedRevision)
				}
				again := quotaFaultRun(t, s)
				if action == "occupy" {
					require.True(t, again.Occupy.Replayed)
					require.Equal(t, s.OperationID, again.Occupy.OperationID)
				}
				if action == "cancel" || action == "delete" {
					require.True(t, again.Delete.Replayed)
					require.Equal(t, replay.Delete.OperationID, again.Delete.OperationID)
				}
				balances, e := repo.RecomputeInvariants(ctx, tid)
				require.NoError(t, e)
				require.Len(t, balances, 2)
				for _, row := range balances {
					require.True(t, row.Balanced)
					want := int64(10)
					if isGPUCode(row.QuotaCode) {
						want = 2
					}
					if action == "cancel" || action == "release" {
						want = 0
					}
					require.Equal(t, want, row.OccupiedUnits)
				}
				if action == "release" {
					n, e := c.Client().QuotaReleaseReceipt.Query().Count(sys)
					require.NoError(t, e)
					require.Equal(t, 1, n)
				}
				t.Logf("DB-03 %s %s COMMIT: OS pid=%d SIGKILL, response absent, durable state and two fresh-process replays verified", action, point, cmd.Process.Pid)
			})
		}
	}
}

func TestQuotaProcessDeleteClaimRace(t *testing.T) {
	c := newLedgerPGClient(t)
	ctx := context.Background()
	var restricted bool
	require.NoError(t, c.DB().QueryRowContext(ctx, gpuRoleAndRLS).Scan(&restricted))
	require.True(t, restricted)
	for i := 0; i < 12; i++ {
		cleanLedger(t, c)
		repo := newTestRepo(c)
		tid, pid := seedTenantPlan(t, c, fmt.Sprintf("process-race-%d", i), 20)
		in := gpuLedgerInput(t, c.Client(), tid, pid)
		original, e := repo.Occupy(ctx, in)
		require.NoError(t, e)
		dir := t.TempDir()
		gate := filepath.Join(dir, "go")
		del := gpuDelete(t, in)
		a := quotaFaultSpec{Action: "cancel", Input: in, Delete: del, Ready: filepath.Join(dir, "delete-ready"), Go: gate}
		b := quotaFaultSpec{Action: "claim", Input: in, Ready: filepath.Join(dir, "claim-ready"), Go: gate}
		ca, ra := quotaFaultStart(t, a)
		cb, rb := quotaFaultStart(t, b)
		quotaWaitFile(t, a.Ready)
		quotaWaitFile(t, b.Ready)
		require.NoError(t, os.WriteFile(gate, []byte("go"), 0600))
		require.NoError(t, ca.Wait())
		require.NoError(t, cb.Wait())
		deleted := quotaFaultRead(t, ra)
		claimed := quotaFaultRead(t, rb)
		op, e := repo.GetOperationForUser(ctx, tid, original.OperationID)
		require.NoError(t, e)
		originalClaims := 0
		for _, v := range claimed.Claims {
			if v.OperationID == original.OperationID {
				originalClaims++
			}
		}
		if deleted.Delete.LocalCanceled {
			require.Zero(t, originalClaims)
			require.Equal(t, quotaoperation.DispatchStateCanceledUnsent, op.DispatchState)
			require.Zero(t, op.AttemptCount)
		} else {
			require.Equal(t, 1, originalClaims)
			require.Equal(t, 1, op.AttemptCount)
		}
		balances, e := repo.RecomputeInvariants(ctx, tid)
		require.NoError(t, e)
		for _, v := range balances {
			require.True(t, v.Balanced)
			if deleted.Delete.LocalCanceled {
				require.Zero(t, v.OccupiedUnits)
			} else {
				require.Positive(t, v.OccupiedUnits)
			}
		}
		replay := quotaFaultRun(t, a)
		require.True(t, replay.Delete.Replayed)
		require.Equal(t, deleted.Delete.OperationID, replay.Delete.OperationID)
		t.Logf("DELETE-02 iteration=%d independent OS pids=%d,%d local_canceled=%t original_claims=%d attempts=%d balances consistent", i, ca.Process.Pid, cb.Process.Pid, deleted.Delete.LocalCanceled, originalClaims, op.AttemptCount)
	}
}
