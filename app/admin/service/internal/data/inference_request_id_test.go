package data

import (
	"context"
	"testing"
	"time"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	"go-wind-admin/pkg/middleware/requestid"
	"google.golang.org/grpc/metadata"
)

func TestInferenceTrustedContextCorrelatesHTTPWithoutForwardingHeaders(t *testing.T) {
	client := &InferenceClient{client: runtimev1.NewInferenceServiceManagerClient(nil), timeout: time.Second}
	ctx := requestid.NewContext(context.Background(), "frontend-123")
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "do-not-forward"))
	call, cancel, err := client.trustedContext(ctx, "64c5c35a-f554-432c-b4bc-7b4f36f08420", "governance:user:1")
	require.NoError(t, err)
	defer cancel()
	md, ok := metadata.FromOutgoingContext(call)
	require.True(t, ok)
	require.Equal(t, []string{"frontend-123"}, md.Get("x-ani-origin-request-id"))
	require.Len(t, md.Get("x-ani-request-id"), 1)
	require.Empty(t, md.Get("authorization"))
}
