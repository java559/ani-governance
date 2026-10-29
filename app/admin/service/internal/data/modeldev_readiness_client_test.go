package data

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"go-wind-admin/pkg/middleware/requestid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type modelDevReadinessPeer struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	t       *testing.T
	scope   ModelDevResolveScope
	version string
	reply   *modeldevv1.CheckFinetuningReadinessResponse
}

func (p *modelDevReadinessPeer) CheckFinetuningReadiness(ctx context.Context, in *modeldevv1.CheckFinetuningReadinessRequest) (*modeldevv1.CheckFinetuningReadinessResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	for key, want := range map[string]string{"x-ani-tenant-id": p.scope.ResourceTenantID, "x-ani-actor": p.scope.Actor, "x-ani-data-scope": "tenant-all", "x-ani-authorized-method": modeldevv1.ModelDevQueryService_CheckFinetuningReadiness_FullMethodName} {
		require.Equal(p.t, []string{want}, md.Get(key))
	}
	require.Empty(p.t, md.Get("x-secret-injected"))
	require.Equal(p.t, []string{"browser-readiness-1"}, md.Get("x-ani-parent-request-id"))
	require.Equal(p.t, p.version, in.BaseModelVersionId)
	origin, err := contractpb.DecodeComputeOrigin(in.ComputeOrigin)
	require.NoError(p.t, err)
	require.Equal(p.t, in.ComputePresetId, origin.ComputePresetID)
	select {
	case <-time.After(100 * time.Millisecond):
		return proto.Clone(p.reply).(*modeldevv1.CheckFinetuningReadinessResponse), nil
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}

// Only the remote service is a peer. Current Gov mTLS, trusted delegation,
// compute encoding, response verification and remaining deadlines run here.
func TestModelDevReadinessUsesResolutionBudgetAndOriginalScope(t *testing.T) {
	f := modelDevResponseFixture(t)
	capabilities := conformance.FinetuningRuntimeCapabilities()
	wire, err := contractpb.EncodeRuntimeCapabilities(capabilities)
	require.NoError(t, err)
	digest, err := capabilities.Digest()
	require.NoError(t, err)
	plan, err := contractpb.EncodeTemplatePlan(conformance.FinetuningTemplatePlan())
	require.NoError(t, err)
	base := conformance.FinetuningSnapshotV1().Finetuning.BaseModel
	reply := &modeldevv1.CheckFinetuningReadinessResponse{Assessment: modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_ELIGIBLE, RuntimeCapabilities: wire, RuntimeCapabilitiesSha256: digest, TemplateCandidates: wire.RegisteredTemplates, RuntimeValidationRequired: true,
		ModelId: base.ModelID, ModelVersionId: base.VersionID, ModelType: base.Facts.ModelType, ContextLimit: base.Facts.ContextLimit, TemplatePlan: plan,
		Limits:          &modeldevv1.ContentLimits{MaxContentBytes: 9007199254740993},
		StructureLimits: &modeldevv1.StructureLimits{MaxModelFiles: cpup01.MaxModelFiles, MaxOutputFiles: cpup01.MaxOutputFiles, MaxArchiveEntries: cpup01.MaxArchiveEntries, MaxRelativePathBytes: cpup01.MaxRelativePathBytes, MaxStorageKeyBytes: cpup01.MaxStorageKeyBytes, MaxManifestBytes: cpup01.MaxManifestBytes, MaxMaterialBytes: cpup01.MaxMaterialBytes, MaxModelConfigBytes: cpup01.MaxModelConfigBytes, MaxArchiveMetadataBytes: cpup01.MaxArchiveMetadataBytes, MaxRecordBytes: cpup01.MaxRecordBytes, MaxJsonDepth: cpup01.MaxJSONDepth, MaxControlRpcBytes: cpup01.MaxControlRPCBytes, MaxPublicationCandidateBytes: cpup01.MaxPublicationCandidateBytes, MaxPublicationRefBytes: cpup01.MaxPublicationRefBytes}}
	for _, tc := range []struct {
		name   string
		change func(*modeldevv1.CheckFinetuningReadinessResponse)
		code   codes.Code
	}{
		{"eligible after short B", nil, codes.OK},
		{"changed capability digest", func(r *modeldevv1.CheckFinetuningReadinessResponse) {
			r.RuntimeCapabilities.Packages[0].Version = "different"
		}, codes.Unavailable},
		{"wrong source version", func(r *modeldevv1.CheckFinetuningReadinessResponse) { r.ModelVersionId = f.identity.ExecutionID }, codes.Unavailable},
		{"wrong structure guard", func(r *modeldevv1.CheckFinetuningReadinessResponse) { r.StructureLimits.MaxModelFiles++ }, codes.Unavailable},
		{"template required is completed check", func(r *modeldevv1.CheckFinetuningReadinessResponse) {
			r.Assessment = modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_TEMPLATE_REQUIRED
			r.Reason = modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_REQUIRED
			r.TemplatePlan = nil
		}, codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			config.Address, config.Timeout, config.ModelResolutionTimeout = listener.Addr().String(), 30*time.Millisecond, time.Second
			peer := &modelDevReadinessPeer{t: t, scope: f.scope, version: base.VersionID, reply: proto.Clone(reply).(*modeldevv1.CheckFinetuningReadinessResponse)}
			if tc.change != nil {
				tc.change(peer.reply)
			}
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
			modeldevv1.RegisterModelDevQueryServiceServer(server, peer)
			t.Cleanup(server.Stop)
			go func() { _ = server.Serve(listener) }()
			client := newModelDevBoundaryClient(t, config)
			ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-ani-tenant-id", f.identity.ExecutionID, "x-secret-injected", "private"))
			ctx = requestid.NewContext(ctx, "browser-readiness-1")
			out, err := client.CheckFinetuningReadiness(ctx, f.scope, base.VersionID, nil, f.selection)
			require.Equal(t, tc.code, status.Code(err))
			if err == nil {
				require.True(t, proto.Equal(peer.reply, out))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
			defer cancel()
			ctx = requestid.NewContext(ctx, "browser-readiness-1")
			_, err = client.CheckFinetuningReadiness(ctx, f.scope, base.VersionID, nil, f.selection)
			require.Equal(t, codes.DeadlineExceeded, status.Code(err))
		})
	}
}
