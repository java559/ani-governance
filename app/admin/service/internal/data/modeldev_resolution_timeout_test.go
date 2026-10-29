package data

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The peer is only the external ModelDev boundary. Gov's real mTLS client,
// candidate verification and phase budgets run unchanged.
func TestModelDevPrepareUsesResolutionBudget(t *testing.T) {
	f := modelDevResponseFixture(t)
	raw, err := f.candidate.Canonical()
	require.NoError(t, err)
	hash, err := f.candidate.Digest()
	require.NoError(t, err)
	config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f,
		func(ctx context.Context, _ *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
			select {
			case <-time.After(200 * time.Millisecond):
				return &modeldevv1.PrepareAdmissionResponse{CandidateCanonical: raw, CandidateHash: hash}, nil
			case <-ctx.Done():
				return nil, status.FromContextError(ctx.Err()).Err()
			}
		}, nil)
	config = modelDevResolutionConfig(t, config)
	_, err = f.resolve(context.Background(), newModelDevBoundaryClient(t, config))
	require.NoError(t, err, "MODELDEV_PREPARE_RESOLUTION_BUDGET: Prepare must be able to finish after B within R")
	require.Equal(t, int32(1), peer.finalizeCalls.Load())
}

func TestModelDevFinalizeKeepsShortBudget(t *testing.T) {
	f := modelDevResponseFixture(t)
	config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, nil,
		func(ctx context.Context, _ *modeldevv1.FinalizeAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error) {
			select {
			case <-time.After(200 * time.Millisecond):
				return f.response, nil
			case <-ctx.Done():
				return nil, status.FromContextError(ctx.Err()).Err()
			}
		})
	config = modelDevResolutionConfig(t, config)
	_, err := f.resolve(context.Background(), newModelDevBoundaryClient(t, config))
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	require.Equal(t, int32(1), peer.finalizeCalls.Load())
}

func TestModelDevPrepareCannotRenewFrozenExecutionDeadline(t *testing.T) {
	f := modelDevResponseFixture(t)
	snapshot, err := contractpb.DecodeSnapshot(f.response.Snapshot)
	require.NoError(t, err)
	snapshot.Timing.AcceptedAt = time.Now().UTC().Truncate(time.Microsecond).Add(-2 * time.Second)
	snapshot.Timing.ExecutionTimeoutSeconds = 1
	snapshot.Timing.ExecutionDeadlineAt = snapshot.Timing.AcceptedAt.Add(time.Second)
	snapshot.DeadlineAt = snapshot.Timing.ExecutionDeadlineAt
	f.acceptedAt = snapshot.Timing.AcceptedAt
	f.candidate.Snapshot.Timing = snapshot.Timing
	f.candidate.Snapshot.DeadlineAt = snapshot.DeadlineAt
	f.response.Snapshot, err = contractpb.EncodeSnapshot(snapshot)
	require.NoError(t, err)
	f.response.ExecutionSpecHash, err = snapshot.Digest()
	require.NoError(t, err)
	config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, nil, nil)
	_, err = f.resolve(context.Background(), newModelDevBoundaryClient(t, config))
	require.Equal(t, codes.DeadlineExceeded, status.Code(err), "MODELDEV_FROZEN_DEADLINE: Prepare cannot grant a fresh execution lifetime")
	require.Zero(t, peer.finalizeCalls.Load())
}

func TestModelDevResolutionKeepsEarlierCallerCancellation(t *testing.T) {
	f := modelDevResponseFixture(t)
	config, _ := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f,
		func(ctx context.Context, _ *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
			<-ctx.Done()
			return nil, status.FromContextError(ctx.Err()).Err()
		}, nil)
	config = modelDevResolutionConfig(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := f.resolve(ctx, newModelDevBoundaryClient(t, config))
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
}

func modelDevResolutionConfig(t *testing.T, config ModelDevClientConfig) ModelDevClientConfig {
	t.Helper()
	clearModelDevConfigEnvironment(t)
	t.Setenv("ANI_MODELDEV_ADDR", config.Address)
	t.Setenv("ANI_MODELDEV_CA", config.CAFile)
	t.Setenv("ANI_MODELDEV_CERT", config.CertFile)
	t.Setenv("ANI_MODELDEV_KEY", config.KeyFile)
	t.Setenv("ANI_MODELDEV_TIMEOUT", "50ms")
	t.Setenv("ANI_MODELDEV_MODEL_RESOLUTION_TIMEOUT_SECONDS", "2")
	loaded, err := ModelDevConfigFromEnv()
	require.NoError(t, err)
	return loaded
}
