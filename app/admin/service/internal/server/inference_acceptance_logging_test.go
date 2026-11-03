package server

import (
	"bytes"
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/pkg/middleware/requestid"
)

func TestInferenceAcceptanceLoggingLinksRequestAndOperation(t *testing.T) {
	var output bytes.Buffer
	logger := log.With(log.NewStdLogger(&output), "request_id", log.Valuer(func(ctx context.Context) any {
		id, _ := requestid.FromContext(ctx)
		return id
	}))
	ctx := requestid.NewContext(context.Background(), "web-request")
	reply, err := inferenceAcceptanceLogging(logger)(func(context.Context, any) (any, error) {
		return &view.InferenceAcceptance{OperationId: "operation-123", ResourceId: "resource-123"}, nil
	})(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, reply)
	require.Contains(t, output.String(), "request_id=web-request")
	require.Contains(t, output.String(), "operation_id=operation-123")
}
