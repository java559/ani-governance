package data

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	storagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/storage/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// StorageClientConfig 复用 Resource 监听地址与 mTLS 凭据；ANI_STORAGE_* 优先，
// 缺省回落到现有 ANI_INSTANCE_*。
type StorageClientConfig struct {
	Address, CAFile, CertFile, KeyFile string
	Timeout                            time.Duration
}

type StorageClient struct {
	client  storagev1.StorageServiceClient
	timeout time.Duration
}

func StorageConfigFromEnv() (StorageClientConfig, error) {
	timeout := 30 * time.Second
	if raw := os.Getenv("ANI_STORAGE_TIMEOUT"); raw != "" {
		var err error
		timeout, err = time.ParseDuration(raw)
		if err != nil || timeout <= 0 || timeout > 45*time.Second {
			return StorageClientConfig{}, fmt.Errorf("invalid Storage client timeout")
		}
	}
	address := os.Getenv("ANI_STORAGE_ADDR")
	if address == "" {
		address = os.Getenv("ANI_INSTANCE_ADDR")
	}
	env := func(storage, shared string) string {
		if value := os.Getenv(storage); value != "" {
			return value
		}
		return os.Getenv(shared)
	}
	return StorageClientConfig{
		Address:  address,
		CAFile:   env("ANI_STORAGE_CA", "ANI_INSTANCE_CA"),
		CertFile: env("ANI_STORAGE_CERT", "ANI_INSTANCE_CERT"),
		KeyFile:  env("ANI_STORAGE_KEY", "ANI_INSTANCE_KEY"),
		Timeout:  timeout,
	}, nil
}

func NewStorageClient(c StorageClientConfig) (*StorageClient, func(), error) {
	invalid := func() (*StorageClient, func(), error) {
		return nil, nil, fmt.Errorf("invalid Storage mTLS client configuration")
	}
	if c.Address == "" || c.Timeout <= 0 || c.Timeout > 45*time.Second {
		return invalid()
	}
	security, err := resourceClientTLS(c.CAFile, c.CertFile, c.KeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("Storage mTLS configuration: %w", err)
	}
	conn, err := grpc.NewClient(c.Address, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(security)))
	if err != nil {
		return nil, nil, fmt.Errorf("create Storage client: %w", err)
	}
	return &StorageClient{client: storagev1.NewStorageServiceClient(conn), timeout: c.Timeout}, func() { _ = conn.Close() }, nil
}

func trustedStorageCall[T any](c *StorageClient, ctx context.Context, tenant, actor string, call func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.client == nil || c.timeout <= 0 {
		return zero, status.Error(codes.Unavailable, "Storage client unavailable")
	}
	if !validResourceIdentity(tenant, actor) {
		return zero, status.Error(codes.Unauthenticated, "invalid trusted Storage identity")
	}
	if err := ctx.Err(); err != nil {
		return zero, status.FromContextError(err).Err()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return call(ctx)
}

func storageRequestRequired() error {
	return status.Error(codes.InvalidArgument, "Storage request required")
}

func (c *StorageClient) ListStorageClasses(ctx context.Context, tenant, actor string, r *storagev1.ListStorageClassesRequest) (*storagev1.ListStorageClassesResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.ListStorageClassesRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.ListStorageClassesResponse, error) {
		return c.client.ListStorageClasses(ctx, req)
	})
}

func (c *StorageClient) CreateVolume(ctx context.Context, tenant, actor string, r *storagev1.CreateVolumeRequest) (*storagev1.CreateVolumeResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.CreateVolumeRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.CreateVolumeResponse, error) {
		return c.client.CreateVolume(ctx, req)
	})
}

func (c *StorageClient) ListVolumes(ctx context.Context, tenant, actor string, r *storagev1.ListVolumesRequest) (*storagev1.ListVolumesResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.ListVolumesRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.ListVolumesResponse, error) {
		return c.client.ListVolumes(ctx, req)
	})
}

func (c *StorageClient) GetVolume(ctx context.Context, tenant, actor string, r *storagev1.GetVolumeRequest) (*storagev1.GetVolumeResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.GetVolumeRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.GetVolumeResponse, error) {
		return c.client.GetVolume(ctx, req)
	})
}

func (c *StorageClient) UpdateVolume(ctx context.Context, tenant, actor string, r *storagev1.UpdateVolumeRequest) (*storagev1.UpdateVolumeResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.UpdateVolumeRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.UpdateVolumeResponse, error) {
		return c.client.UpdateVolume(ctx, req)
	})
}

func (c *StorageClient) DeleteVolume(ctx context.Context, tenant, actor string, r *storagev1.DeleteVolumeRequest) (*storagev1.DeleteVolumeResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.DeleteVolumeRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.DeleteVolumeResponse, error) {
		return c.client.DeleteVolume(ctx, req)
	})
}

func (c *StorageClient) ListBuckets(ctx context.Context, tenant, actor string, r *storagev1.ListBucketsRequest) (*storagev1.ListBucketsResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.ListBucketsRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.ListBucketsResponse, error) {
		return c.client.ListBuckets(ctx, req)
	})
}

func (c *StorageClient) ListObjects(ctx context.Context, tenant, actor string, r *storagev1.ListObjectsRequest) (*storagev1.ListObjectsResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.ListObjectsRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.ListObjectsResponse, error) {
		return c.client.ListObjects(ctx, req)
	})
}

func (c *StorageClient) HeadObject(ctx context.Context, tenant, actor string, r *storagev1.HeadObjectRequest) (*storagev1.HeadObjectResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.HeadObjectRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.HeadObjectResponse, error) {
		return c.client.HeadObject(ctx, req)
	})
}

func (c *StorageClient) GetDownloadURL(ctx context.Context, tenant, actor string, r *storagev1.GetDownloadURLRequest) (*storagev1.GetDownloadURLResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.GetDownloadURLRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.GetDownloadURLResponse, error) {
		return c.client.GetDownloadURL(ctx, req)
	})
}

func (c *StorageClient) CreateBucket(ctx context.Context, tenant, actor string, r *storagev1.CreateBucketRequest) (*storagev1.CreateBucketResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.CreateBucketRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.CreateBucketResponse, error) {
		return c.client.CreateBucket(ctx, req)
	})
}

func (c *StorageClient) GetUploadURL(ctx context.Context, tenant, actor string, r *storagev1.GetUploadURLRequest) (*storagev1.GetUploadURLResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.GetUploadURLRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.GetUploadURLResponse, error) {
		return c.client.GetUploadURL(ctx, req)
	})
}

func (c *StorageClient) DeleteObject(ctx context.Context, tenant, actor string, r *storagev1.DeleteObjectRequest) (*storagev1.DeleteObjectResponse, error) {
	if r == nil {
		return nil, storageRequestRequired()
	}
	req := proto.Clone(r).(*storagev1.DeleteObjectRequest)
	req.TenantId = tenant
	return trustedStorageCall(c, ctx, tenant, actor, func(ctx context.Context) (*storagev1.DeleteObjectResponse, error) {
		return c.client.DeleteObject(ctx, req)
	})
}
