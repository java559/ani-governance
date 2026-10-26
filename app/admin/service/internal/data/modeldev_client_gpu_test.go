package data

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// The existing real-TLS response peer and a typed ACC response are external
// boundaries only. These tests exercise the production Gov client; they do not
// replace a ModelDev compiler, original ledger, authorization or GPU runtime.
func modelDevGPUResponseFixture(t *testing.T, mode acc.SupplyMode) (modelDevResponseCase, *acc.ResolvedGpuRequest) {
	t.Helper()
	f := modelDevResponseFixture(t)
	core, code, units := uint32(30), commandcontract.GpuSharedQuotaCode, int64(2048)
	if mode == acc.SupplyMode_WHOLE_EXCLUSIVE {
		core, code, units = 100, commandcontract.GpuPhysicalQuotaCode, 1
	}
	f.selection.ComputeType = "gpu"
	f.selection.Gpu = &compute.GpuSelection{ClusterId: f.candidate.Snapshot.Environment.ClusterID, PoolId: uuid.NewString(), ProfileId: uuid.NewString(), ProfileVersion: 3, SpecDigest: strings.Repeat("a", 64), DevicesPerUnit: 1, Mode: mode, ModelKey: "nvidia-t4", MemoryMibPerDevice: 2048, CoreLimitPercent: core}
	var err error
	f.selection.SelectionDigest, err = compute.SelectionDigest(f.selection)
	require.NoError(t, err)
	raw, err := compute.CanonicalSelection(f.selection)
	require.NoError(t, err)
	f.candidate.Snapshot.ComputeOrigin, err = contractpb.DecodeComputeSelection(raw, f.selection.SelectionDigest)
	require.NoError(t, err)
	f.candidate.GPURequest, err = cpup01.GPURequestFor(f.candidate.Snapshot.ComputeOrigin)
	require.NoError(t, err)
	require.NoError(t, f.candidate.Validate())
	e := &acc.GpuExecution{SchedulerName: "volcano", QueueName: "gpu-task", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": uuid.NewString(), "accelerator.ani.io/model-key": "nvidia-t4", "accelerator.ani.io/baseline-id": uuid.NewString()}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "2048", "volcano.sh/vgpu-cores": "30"}}
	if core == 100 {
		e.Resources["volcano.sh/vgpu-cores"] = "100"
	}
	e.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(e)
	require.NoError(t, err)
	d := &acc.GpuDisplay{ProfileId: f.selection.Gpu.ProfileId, ProfileVersion: f.selection.Gpu.ProfileVersion, ProfileName: "T4 fixture", ModelKey: "nvidia-t4", Mode: mode}
	resolved := &acc.ResolvedGpuRequest{Execution: e, Selection: d, QuotaItems: []*acc.GpuQuotaItem{{QuotaCode: code, Units: units}}}
	execution, display, err := contractpb.EncodeGPUPlanCanonical(e, d)
	require.NoError(t, err)
	final, err := contractpb.DecodeSnapshot(f.response.Snapshot)
	require.NoError(t, err)
	final.ComputeOrigin = f.candidate.Snapshot.ComputeOrigin
	final.GPU, err = contractpb.DecodeGPUPlanCanonical(execution, display)
	require.NoError(t, err)
	f.response.Snapshot, err = contractpb.EncodeSnapshot(final)
	require.NoError(t, err)
	f.response.ExecutionSpecHash, err = final.Digest()
	require.NoError(t, err)
	return f, resolved
}

func TestModelDevGPUPreparedClientPreservesOriginalWholeAndSharedFacts(t *testing.T) {
	for _, mode := range []acc.SupplyMode{acc.SupplyMode_WHOLE_EXCLUSIVE, acc.SupplyMode_SHARED_FIXED} {
		t.Run(mode.String(), func(t *testing.T) {
			f, original := modelDevGPUResponseFixture(t, mode)
			execution, display, err := contractpb.EncodeGPUPlanCanonical(original.Execution, original.Selection)
			require.NoError(t, err)
			candidate, err := f.candidate.Canonical()
			require.NoError(t, err)
			candidateHash, err := f.candidate.Digest()
			require.NoError(t, err)
			finals := make(chan *modeldevv1.FinalizeAdmissionRequest, 1)
			config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, nil, func(_ context.Context, r *modeldevv1.FinalizeAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error) {
				finals <- proto.Clone(r).(*modeldevv1.FinalizeAdmissionRequest)
				return proto.Clone(f.response).(*modeldevv1.FinalizeAdmissionResponse), nil
			})
			config.Timeout = 5 * time.Second
			parentDeadline := time.Now().Add(2 * time.Second)
			ctx, cancel := context.WithDeadline(context.Background(), parentDeadline)
			defer cancel()
			calls := 0
			buffer := proto.Clone(original).(*acc.ResolvedGpuRequest)
			got, err := newModelDevBoundaryClient(t, config).ResolvePreparedWithGPU(ctx, f.scope, f.intent, f.identity, f.selection, f.acceptedAt, func(call context.Context, request *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error) {
				calls++
				require.Equal(t, int32(1), peer.prepareCalls.Load())
				require.Zero(t, peer.finalizeCalls.Load())
				require.Equal(t, f.candidate.GPURequest, request)
				deadline, ok := call.Deadline()
				require.True(t, ok)
				require.True(t, deadline.Equal(parentDeadline))
				request.ContainerName = "callback-cannot-rewrite-original"
				return buffer, nil
			})
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, int32(1), peer.finalizeCalls.Load())
			r := <-finals
			require.Equal(t, candidate, r.CandidateCanonical)
			require.Equal(t, candidateHash, r.CandidateHash)
			require.Equal(t, execution, r.GpuExecutionCanonical)
			require.Equal(t, display, r.GpuDisplayCanonical)
			require.True(t, proto.Equal(original, got.ResolvedGPU))
			require.Equal(t, f.response.ExecutionSpecHash, got.ExecutionSpecHash)
			buffer.QuotaItems[0].Units++
			buffer.Execution.QueueName = "mutated-after-return"
			require.True(t, proto.Equal(original, got.ResolvedGPU), "original ACC full response must not alias callback buffers")
		})
	}
}

func TestModelDevGPUPreparedClientKeepsFactor10RuntimePlacementAndMiBLedger(t *testing.T) {
	f, original := modelDevGPUResponseFixture(t, acc.SupplyMode_SHARED_FIXED)
	f.selection.Gpu.MemoryMibPerDevice = 3840
	var err error
	f.selection.SelectionDigest, err = compute.SelectionDigest(f.selection)
	require.NoError(t, err)
	raw, err := compute.CanonicalSelection(f.selection)
	require.NoError(t, err)
	f.candidate.Snapshot.ComputeOrigin, err = contractpb.DecodeComputeSelection(raw, f.selection.SelectionDigest)
	require.NoError(t, err)
	f.candidate.GPURequest, err = cpup01.GPURequestFor(f.candidate.Snapshot.ComputeOrigin)
	require.NoError(t, err)
	original.Execution.Resources["volcano.sh/vgpu-memory"] = "384"
	original.Execution.PodAnnotations["accelerator.ani.io/memory-block-mib"] = "10"
	original.Execution.NodeSelector["kubernetes.io/hostname"] = "node3"
	original.Execution.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(original.Execution)
	require.NoError(t, err)
	original.QuotaItems[0].Units = 3840
	execution, display, err := contractpb.EncodeGPUPlanCanonical(original.Execution, original.Selection)
	require.NoError(t, err)
	final, err := contractpb.DecodeSnapshot(f.response.Snapshot)
	require.NoError(t, err)
	final.ComputeOrigin = f.candidate.Snapshot.ComputeOrigin
	final.GPU, err = contractpb.DecodeGPUPlanCanonical(execution, display)
	require.NoError(t, err)
	f.response.Snapshot, err = contractpb.EncodeSnapshot(final)
	require.NoError(t, err)
	f.response.ExecutionSpecHash, err = final.Digest()
	require.NoError(t, err)
	config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, nil, nil)
	got, err := newModelDevBoundaryClient(t, config).ResolvePreparedWithGPU(context.Background(), f.scope, f.intent, f.identity, f.selection, f.acceptedAt, func(context.Context, *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error) {
		return original, nil
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), peer.finalizeCalls.Load())
	require.True(t, proto.Equal(original, got.ResolvedGPU))
	require.Equal(t, "384", got.Snapshot.GPU.Execution.Resources["volcano.sh/vgpu-memory"])
	require.Equal(t, "10", got.Snapshot.GPU.Execution.PodAnnotations["accelerator.ani.io/memory-block-mib"])
	require.Equal(t, "node3", got.Snapshot.GPU.Execution.NodeSelector["kubernetes.io/hostname"])
	envelope := cpup01.AdmissionEnvelope{TenantID: f.identity.TenantID, Actor: f.identity.Actor, OperationID: f.identity.OperationID, ExecutionID: f.identity.ExecutionID, Intent: f.intent, IntentHash: f.identity.IntentHash, Snapshot: got.Snapshot, SpecHash: got.ExecutionSpecHash, AcceptedAt: f.acceptedAt}
	ledger, err := commandcontract.NewModelDevGPUCanonical(envelope, got.ResolvedGPU)
	require.NoError(t, err)
	require.Equal(t, []commandcontract.GpuQuotaItem{{QuotaCode: commandcontract.GpuSharedQuotaCode, Units: 3840}}, ledger.QuotaItems)
}

func TestModelDevGPUPreparedClientCPUDoesNotResolveOrCreateGPUFacts(t *testing.T) {
	f := modelDevResponseFixture(t)
	config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, nil, nil)
	got, err := newModelDevBoundaryClient(t, config).ResolvePreparedWithGPU(context.Background(), f.scope, f.intent, f.identity, f.selection, f.acceptedAt, func(context.Context, *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error) {
		t.Fatal("CPU reached ACC resolution")
		return nil, nil
	})
	require.NoError(t, err)
	require.Nil(t, got.ResolvedGPU)
	require.Nil(t, got.Snapshot.GPU)
	require.Equal(t, int32(1), peer.prepareCalls.Load())
	require.Equal(t, int32(1), peer.finalizeCalls.Load())
}

func TestModelDevGPUPreparedClientRejectsUnverifiedPrepareBeforeACC(t *testing.T) {
	for _, failure := range []string{"identity", "selection", "gpu-request", "hash"} {
		t.Run(failure, func(t *testing.T) {
			f, resolved := modelDevGPUResponseFixture(t, acc.SupplyMode_SHARED_FIXED)
			prepared := f.candidate
			if failure == "identity" {
				prepared.Snapshot.IdentityRef.OperationID = uuid.NewString()
			}
			if failure == "selection" {
				selection := proto.Clone(f.selection).(*compute.ComputeSelection)
				selection.Gpu.PoolId = uuid.NewString()
				selection.SelectionDigest, _ = compute.SelectionDigest(selection)
				raw, err := compute.CanonicalSelection(selection)
				require.NoError(t, err)
				prepared.Snapshot.ComputeOrigin, err = contractpb.DecodeComputeSelection(raw, selection.SelectionDigest)
				require.NoError(t, err)
				prepared.GPURequest, err = cpup01.GPURequestFor(prepared.Snapshot.ComputeOrigin)
				require.NoError(t, err)
			}
			raw, err := prepared.Canonical()
			require.NoError(t, err)
			if failure == "gpu-request" {
				raw = bytes.Replace(raw, []byte(`"container_name":"node"`), []byte(`"container_name":"workspace"`), 1)
			}
			sum := sha256.Sum256(raw)
			hash := hex.EncodeToString(sum[:])
			if failure == "hash" {
				hash = strings.Repeat("f", 64)
			}
			config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, func(context.Context, *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
				return &modeldevv1.PrepareAdmissionResponse{CandidateCanonical: raw, CandidateHash: hash}, nil
			}, nil)
			calls := 0
			got, err := newModelDevBoundaryClient(t, config).ResolvePreparedWithGPU(context.Background(), f.scope, f.intent, f.identity, f.selection, f.acceptedAt, func(context.Context, *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error) {
				calls++
				return resolved, nil
			})
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Equal(t, ModelDevResolution{}, got)
			require.Zero(t, calls)
			require.Zero(t, peer.finalizeCalls.Load())
		})
	}
}

func TestModelDevGPUPreparedClientRejectsInvalidACCBeforeFinalize(t *testing.T) {
	for _, failure := range []string{"nil", "quota", "unknown", "digest", "profile", "origin-mode"} {
		t.Run(failure, func(t *testing.T) {
			f, resolved := modelDevGPUResponseFixture(t, acc.SupplyMode_SHARED_FIXED)
			switch failure {
			case "nil":
				resolved = nil
			case "quota":
				resolved.QuotaItems[0].Units++
			case "unknown":
				resolved.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
			case "digest":
				resolved.Execution.ExecutionDigest = strings.Repeat("f", 64)
			case "profile":
				resolved.Selection.ProfileId = uuid.NewString()
			case "origin-mode":
				resolved.Selection.Mode = acc.SupplyMode_WHOLE_EXCLUSIVE
				resolved.Execution.Resources["volcano.sh/vgpu-cores"] = "100"
				resolved.Execution.ExecutionDigest, _ = gpuprotocol.GpuExecutionDigest(resolved.Execution)
				resolved.QuotaItems[0] = &acc.GpuQuotaItem{QuotaCode: commandcontract.GpuPhysicalQuotaCode, Units: 1}
			}
			config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, nil, nil)
			got, err := newModelDevBoundaryClient(t, config).ResolvePreparedWithGPU(context.Background(), f.scope, f.intent, f.identity, f.selection, f.acceptedAt, func(context.Context, *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error) { return resolved, nil })
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Equal(t, ModelDevResolution{}, got)
			require.Zero(t, peer.finalizeCalls.Load())
		})
	}
}

func TestModelDevGPUPreparedClientRejectsValidButDifferentFinalGPU(t *testing.T) {
	f, resolved := modelDevGPUResponseFixture(t, acc.SupplyMode_SHARED_FIXED)
	changed := proto.Clone(resolved).(*acc.ResolvedGpuRequest)
	changed.Execution.QueueName = "another-valid-queue"
	changed.Execution.ExecutionDigest, _ = gpuprotocol.GpuExecutionDigest(changed.Execution)
	execution, display, err := contractpb.EncodeGPUPlanCanonical(changed.Execution, changed.Selection)
	require.NoError(t, err)
	final, err := contractpb.DecodeSnapshot(f.response.Snapshot)
	require.NoError(t, err)
	final.GPU, err = contractpb.DecodeGPUPlanCanonical(execution, display)
	require.NoError(t, err)
	f.response.Snapshot, err = contractpb.EncodeSnapshot(final)
	require.NoError(t, err)
	f.response.ExecutionSpecHash, err = final.Digest()
	require.NoError(t, err)
	config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, nil, nil)
	got, err := newModelDevBoundaryClient(t, config).ResolvePreparedWithGPU(context.Background(), f.scope, f.intent, f.identity, f.selection, f.acceptedAt, func(context.Context, *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error) { return resolved, nil })
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Equal(t, ModelDevResolution{}, got)
	require.Equal(t, int32(1), peer.finalizeCalls.Load())
}

func TestModelDevGPUPreparedClientBoundsACCAndNeverFinalizesAfterTimeout(t *testing.T) {
	f, _ := modelDevGPUResponseFixture(t, acc.SupplyMode_SHARED_FIXED)
	config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, nil, nil)
	config.Timeout = 150 * time.Millisecond
	got, err := newModelDevBoundaryClient(t, config).ResolvePreparedWithGPU(context.Background(), f.scope, f.intent, f.identity, f.selection, f.acceptedAt, func(ctx context.Context, _ *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error) {
		_, ok := ctx.Deadline()
		require.True(t, ok)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	require.Equal(t, ModelDevResolution{}, got)
	require.Zero(t, peer.finalizeCalls.Load())
}
