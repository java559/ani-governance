package server

import (
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

// TestVMImageHTTPErrorCarriesContractStatusCodes guards the regression found on the real
// cluster: a plain gRPC NotFound from Resource arrived as a kratos error with an empty
// Reason, so a Reason-only mapping collapsed every 404 into 503 and the browser could not
// tell "no such image" from "image service down".
func TestVMImageHTTPErrorCarriesContractStatusCodes(t *testing.T) {
	withReason, err := status.New(codes.Unknown, "private native detail").WithDetails(&errdetails.ErrorInfo{Reason: "VM_IMAGE_NOT_READY"})
	require.NoError(t, err)
	for _, test := range []struct {
		err    error
		code   int
		reason string
	}{
		{status.Error(codes.NotFound, "VM_IMAGE_NOT_FOUND"), 404, "VM_IMAGE_NOT_FOUND"},
		{status.Error(codes.FailedPrecondition, "VM_IMAGE_NOT_READY"), 409, "VM_IMAGE_NOT_READY"},
		{status.Error(codes.FailedPrecondition, "VM_IMAGE_IN_USE"), 409, "VM_IMAGE_IN_USE"},
		{status.Error(codes.FailedPrecondition, "VM_IMAGE_UPLOAD_DISABLED"), 409, "VM_IMAGE_UPLOAD_DISABLED"},
		{status.Error(codes.AlreadyExists, "VM_IDEMPOTENCY_CONFLICT"), 409, "VM_IDEMPOTENCY_CONFLICT"},
		{status.Error(codes.InvalidArgument, "VM_IMAGE_INVALID_ARGUMENT"), 400, "VM_IMAGE_INVALID_ARGUMENT"},
		{status.Error(codes.PermissionDenied, "VM_SCOPE_DENIED"), 403, "VM_SCOPE_DENIED"},
		{kratoserrors.NotFound("VM_IMAGE_NOT_FOUND", "private ledger details"), 404, "VM_IMAGE_NOT_FOUND"},
		{withReason.Err(), 409, "VM_IMAGE_NOT_READY"},
		// Unpublishable detail stays collapsed, whatever the transport code was.
		{status.Error(codes.Unavailable, "10.16.0.100:443 refused"), 503, "VM_IMAGE_DEPENDENCY_UNAVAILABLE"},
		{status.Error(codes.Internal, "tenant-64c5c35a pod vm-0f1ca6ba failed"), 503, "VM_IMAGE_DEPENDENCY_UNAVAILABLE"},
	} {
		result := vmImageHTTPError(test.err)
		require.Equal(t, test.code, kratoserrors.Code(result), "%v", test.err)
		require.Equal(t, test.reason, kratoserrors.Reason(result), "%v", test.err)
		require.NotContains(t, result.Error(), "private")
		require.NotContains(t, result.Error(), "tenant-")
		require.NotContains(t, result.Error(), "10.16.")
	}
}
