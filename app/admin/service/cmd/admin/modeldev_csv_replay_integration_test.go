//go:build modeldev_management_pg

package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Only the remote ModelDev transport is a substitute. The repeated CLI calls
// build distinct production clients after actual JWT/Redis/current PG grants.
type modelDevCSVReplayPeer struct {
	modeldevv1.UnimplementedModelDevManagementServiceServer
	tenant, actor, requestID string
	frozen                   *modeldevv1.ImportCSVRequest
	calls                    int
}

func (p *modelDevCSVReplayPeer) ImportCSV(ctx context.Context, in *modeldevv1.ImportCSVRequest) (*modeldevv1.ImportCSVResponse, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 29*time.Minute || time.Until(deadline) > 30*time.Minute {
		return nil, status.Error(codes.Internal, "independent import deadline missing")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	for key, want := range map[string]string{"x-ani-tenant-id": p.tenant, "x-ani-actor": p.actor, "x-ani-authorized-method": modeldevv1.ModelDevManagementService_ImportCSV_FullMethodName, "x-ani-data-scope": "tenant-all"} {
		if values := md.Get(key); len(values) != 1 || values[0] != want {
			return nil, status.Error(codes.PermissionDenied, "invalid delegation")
		}
	}
	ids := md.Get("x-ani-request-id")
	if len(ids) != 1 {
		return nil, status.Error(codes.InvalidArgument, "request identity missing")
	}
	if _, err := uuid.Parse(ids[0]); err != nil {
		return nil, status.Error(codes.InvalidArgument, "request identity invalid")
	}
	p.calls++
	if p.frozen == nil {
		p.frozen = proto.Clone(in).(*modeldevv1.ImportCSVRequest)
		p.requestID = ids[0]
	} else if p.requestID != ids[0] || !proto.Equal(p.frozen, in) {
		return nil, status.Error(codes.AlreadyExists, "immutable import conflict")
	}
	return &modeldevv1.ImportCSVResponse{InputVersion: &modeldevv1.InputVersionView{InputVersionId: in.InputVersionId, State: modeldevv1.InputState_INPUT_STATE_READY, Format: "CSV", Sha256: in.Object.Sha256, SizeBytes: in.Object.SizeBytes, RowCount: 1024, FeatureCount: 16, CreatedAt: in.RequestedAt}}, nil
}

func runModelDevCSVReplayVerticalCase(t *testing.T, ctx context.Context, directory, tokenFile, tenant, actor string) {
	t.Helper()
	config, security := modelDevBudgetTLS(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	config.Address = listener.Addr().String()
	peer := &modelDevCSVReplayPeer{tenant: tenant, actor: actor}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	modeldevv1.RegisterModelDevManagementServiceServer(server, peer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	for key, value := range map[string]string{"ANI_MODELDEV_ADDR": config.Address, "ANI_MODELDEV_CA": config.CAFile, "ANI_MODELDEV_CERT": config.CertFile, "ANI_MODELDEV_KEY": config.KeyFile, "ANI_MODELDEV_TIMEOUT": "2s"} {
		t.Setenv(key, value)
	}
	in := &modeldevv1.ImportCSVRequest{InputVersionId: uuid.NewString(), ImplementationId: "builtin.mlp", RequestedAt: timestamppb.New(time.Now().UTC().Truncate(time.Microsecond)), Object: &trainingv1.FixedObjectRef{StorageConnectionId: "test-owned", Bucket: "test-input", Key: "inputs/data.csv", Immutability: &trainingv1.FixedObjectRef_VersionId{VersionId: uuid.NewString()}, SizeBytes: 157036, Sha256: strings.Repeat("b", 64)}}
	requestFile := filepath.Join(t.TempDir(), "csv.json")
	write := func() {
		raw, e := protojson.Marshal(in)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(requestFile, raw, 0600))
	}
	write()
	args := []string{"modeldev-import-csv", "--conf", directory, "--token-file", tokenFile, "--request-file", requestFile}
	var output bytes.Buffer
	require.NoError(t, runAdmin(ctx, args, &output))
	first := append([]byte(nil), output.Bytes()...)
	output.Reset()
	require.NoError(t, runAdmin(ctx, args, &output), "same fixed import through a fresh client must preserve the delegated request ID")
	require.JSONEq(t, string(first), output.String())
	require.Equal(t, 2, peer.calls)
	in.Object.Sha256 = strings.Repeat("c", 64)
	write()
	output.Reset()
	err = runAdmin(ctx, args, &output)
	require.ErrorContains(t, err, "AlreadyExists", "confirmed immutable-import conflict must remain distinct from unavailable")
	require.Empty(t, output.String())
	require.Equal(t, 3, peer.calls)
	t.Log("MODELDEV_CSV_REPLAY PASS: fresh production clients preserve fixed import identity after actual current auth, changed body still conflicts")
}
