package service

import (
	"context"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	storagepb "go-wind-admin/api/gen/go/storage/service/v1"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func uint32Pointer(value uint32) *uint32 { return &value }

func TestStorageObjectListRejectsPagePagination(t *testing.T) {
	svc := NewStorageService(nil, nil)
	cases := []*storagepb.ListObjectsRequest{
		{BucketId: "files", Page: uint32Pointer(1)},
		{BucketId: "files", PageSize: uint32Pointer(20)},
		{BucketId: "files", Offset: "10"},
		{BucketId: "files", Token: "legacy"},
	}
	for _, request := range cases {
		_, err := svc.ListObjects(context.Background(), request)
		if err == nil {
			t.Fatal("page/pageSize/offset/token must be rejected")
		}
		kratosErr, ok := err.(*kratoserrors.Error)
		if !ok || kratosErr.Code != 400 || kratosErr.Reason != "STORAGE_UNSUPPORTED_PAGINATION" {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestStoragePageDefaultsAndBounds(t *testing.T) {
	page, size, err := storagePage(nil, nil)
	if err != nil || page != 1 || size != 20 {
		t.Fatalf("defaults = %d/%d, %v", page, size, err)
	}
	if _, _, err := storagePage(uint32Pointer(0), nil); err == nil {
		t.Fatal("explicit page 0 must fail")
	}
	if _, _, err := storagePage(nil, uint32Pointer(0)); err == nil {
		t.Fatal("explicit pageSize 0 must fail")
	}
	if _, _, err := storagePage(nil, uint32Pointer(101)); err == nil {
		t.Fatal("pageSize above 100 must fail")
	}
}

func TestStorageErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		code   int32
		reason string
	}{
		{grpcstatus.Error(grpccodes.PermissionDenied, "STORAGE_ACCESS_DENIED"), 403, "STORAGE_ACCESS_DENIED"},
		{grpcstatus.Error(grpccodes.AlreadyExists, "CREATE_TOKEN_CONFLICT"), 409, "CREATE_TOKEN_CONFLICT"},
		{grpcstatus.Error(grpccodes.AlreadyExists, "VOLUME_IDENTITY_CHANGED"), 409, "VOLUME_IDENTITY_CHANGED"},
		{grpcstatus.Error(grpccodes.AlreadyExists, "VOLUME_IN_USE"), 409, "VOLUME_IN_USE"},
		{grpcstatus.Error(grpccodes.InvalidArgument, "CAPACITY_MUST_NOT_DECREASE"), 400, "CAPACITY_MUST_NOT_DECREASE"},
		{grpcstatus.Error(grpccodes.FailedPrecondition, "DOWNLOAD_ENDPOINT_UNAVAILABLE"), 503, "DOWNLOAD_ENDPOINT_UNAVAILABLE"},
		{grpcstatus.Error(grpccodes.Unavailable, "VOLUME_USAGE_UNKNOWN"), 503, "VOLUME_USAGE_UNKNOWN"},
		{grpcstatus.Error(grpccodes.Aborted, "STORAGE_RESOURCE_CONFLICT"), 409, "STORAGE_RESOURCE_CONFLICT"},
		{grpcstatus.Error(grpccodes.NotFound, "STORAGE_RESOURCE_NOT_FOUND"), 404, "STORAGE_RESOURCE_NOT_FOUND"},
		{grpcstatus.Error(grpccodes.AlreadyExists, "BUCKET_ALREADY_EXISTS"), 409, "BUCKET_ALREADY_EXISTS"},
		{grpcstatus.Error(grpccodes.AlreadyExists, "OBJECT_ALREADY_EXISTS"), 409, "OBJECT_ALREADY_EXISTS"},
		{grpcstatus.Error(grpccodes.AlreadyExists, "OBJECT_IDENTITY_CHANGED"), 409, "OBJECT_IDENTITY_CHANGED"},
		{grpcstatus.Error(grpccodes.InvalidArgument, "OBJECT_TOO_LARGE"), 400, "OBJECT_TOO_LARGE"},
		{grpcstatus.Error(grpccodes.FailedPrecondition, "UPLOAD_ENDPOINT_UNAVAILABLE"), 503, "UPLOAD_ENDPOINT_UNAVAILABLE"},
		{grpcstatus.Error(grpccodes.FailedPrecondition, "BUCKET_CREATION_DISABLED"), 403, "BUCKET_CREATION_DISABLED"},
		{grpcstatus.Error(grpccodes.FailedPrecondition, "BUCKET_LIMIT_REACHED"), 409, "BUCKET_LIMIT_REACHED"},
	}
	for _, tc := range cases {
		mapped, ok := mapStorageError(tc.err).(*kratoserrors.Error)
		if !ok || mapped.Code != tc.code || mapped.Reason != tc.reason {
			t.Fatalf("%v mapped to %+v", tc.err, mapped)
		}
	}
}

func TestStorageAccessDeniedMessage(t *testing.T) {
	mapped, ok := mapStorageError(grpcstatus.Error(grpccodes.PermissionDenied, "STORAGE_ACCESS_DENIED")).(*kratoserrors.Error)
	if !ok || mapped.Message != "该租户存储权限未开通，请联系管理员" {
		t.Fatalf("unexpected message: %+v", mapped)
	}
}

func TestStorageWriteHandlersValidateData(t *testing.T) {
	svc := NewStorageService(nil, nil)
	cases := []struct {
		name string
		call func() error
	}{
		{"create bucket nil data", func() error {
			_, err := svc.CreateBucket(context.Background(), &storagepb.CreateBucketRequest{})
			return err
		}},
		{"create bucket empty name", func() error {
			_, err := svc.CreateBucket(context.Background(), &storagepb.CreateBucketRequest{Data: &storagepb.CreateBucketData{Name: "  "}})
			return err
		}},
		{"upload missing key", func() error {
			_, err := svc.GetUploadURL(context.Background(), &storagepb.GetUploadURLRequest{Data: &storagepb.GetUploadURLData{BucketId: "files", Size: 1}})
			return err
		}},
		{"upload zero size", func() error {
			_, err := svc.GetUploadURL(context.Background(), &storagepb.GetUploadURLRequest{Data: &storagepb.GetUploadURLData{BucketId: "files", Key: "a.txt"}})
			return err
		}},
		{"delete empty key", func() error {
			_, err := svc.DeleteObject(context.Background(), &storagepb.DeleteObjectRequest{Data: &storagepb.DeleteObjectData{BucketId: "files"}})
			return err
		}},
	}
	for _, tc := range cases {
		err := tc.call()
		kratosErr, ok := err.(*kratoserrors.Error)
		if !ok || kratosErr.Code != 400 || kratosErr.Reason != "STORAGE_INVALID_REQUEST" {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
	}
}

func TestStorageRoutesRegistered(t *testing.T) {
	operations := []string{
		adminV1.OperationStorageServiceListStorageClasses,
		adminV1.OperationStorageServiceCreateVolume,
		adminV1.OperationStorageServiceListVolumes,
		adminV1.OperationStorageServiceGetVolume,
		adminV1.OperationStorageServiceUpdateVolume,
		adminV1.OperationStorageServiceDeleteVolume,
		adminV1.OperationStorageServiceListBuckets,
		adminV1.OperationStorageServiceListObjects,
		adminV1.OperationStorageServiceHeadObject,
		adminV1.OperationStorageServiceGetDownloadURL,
		adminV1.OperationStorageServiceCreateBucket,
		adminV1.OperationStorageServiceGetUploadURL,
		adminV1.OperationStorageServiceDeleteObject,
	}
	seen := map[string]bool{}
	for _, operation := range operations {
		if operation == "" || seen[operation] {
			t.Fatalf("operation %q is missing or duplicated", operation)
		}
		seen[operation] = true
	}
}
