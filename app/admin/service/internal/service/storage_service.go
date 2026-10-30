package service

import (
	"context"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport"
	resourcestorage "github.com/zhangzhe-ctrl/ani-resource-service/api/storage/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	storagepb "go-wind-admin/api/gen/go/storage/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	grpcstatus "google.golang.org/grpc/status"
)

// StorageService 是存储页面 BFF。租户 identity 只来自已认证主体。
type StorageService struct {
	admin.UnimplementedStorageServiceServer
	client  *data.StorageClient
	tenants ResourceTenantResolver
}

func NewStorageService(client *data.StorageClient, tenants ResourceTenantResolver) *StorageService {
	return &StorageService{client: client, tenants: tenants}
}

func (s *StorageService) trustedStorageOperator(ctx context.Context) (string, string, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return "", "", errors.Unauthorized("INVALID_LOGIN", "login required")
	}
	if p.TenantID == 0 {
		return "", "", errors.Forbidden("TENANT_REQUIRED", "tenant identity required")
	}
	actor, err := p.Actor()
	if err != nil {
		return "", "", errors.Unauthorized("INVALID_LOGIN", "login required")
	}
	if s.client == nil || s.tenants == nil {
		return "", "", errors.ServiceUnavailable("STORAGE_UNAVAILABLE", "Storage access is not configured")
	}
	tenant, err := s.tenants.ResourceTenantID(ctx, p.TenantID)
	if err != nil {
		return "", "", errors.ServiceUnavailable("TENANT_MAPPING_UNAVAILABLE", "tenant mapping unavailable")
	}
	if !storageUUID(tenant) {
		return "", "", errors.ServiceUnavailable("TENANT_MAPPING_INVALID", "tenant mapping unavailable")
	}
	return tenant, actor, nil
}

func storageUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	return strings.Count(value, "-") == 4
}

func storageBadRequest() error {
	return errors.BadRequest("STORAGE_INVALID_REQUEST", "invalid storage request")
}

func storageNoStore(ctx context.Context) {
	if tr, ok := transport.FromServerContext(ctx); ok {
		tr.ReplyHeader().Set("Cache-Control", "no-store")
		tr.ReplyHeader().Set("Pragma", "no-cache")
	}
}

func storagePage(page, size *uint32) (uint32, uint32, error) {
	current := uint32(1)
	if page != nil {
		if *page == 0 {
			return 0, 0, storageBadRequest()
		}
		current = *page
	}
	limit := uint32(20)
	if size != nil {
		if *size == 0 || *size > 100 {
			return 0, 0, storageBadRequest()
		}
		limit = *size
	}
	return current, limit, nil
}

// mapStorageError 保留 Res 后端分类，并把业务码映射为页面可直接使用的 HTTP 状态。
func mapStorageError(err error) error {
	st, ok := statusFrom(err)
	if !ok {
		return errors.InternalServer("STORAGE_INTERNAL_ERROR", "storage operation failed")
	}
	switch st.code {
	case "InvalidArgument":
		switch st.message {
		case "CAPACITY_MUST_NOT_DECREASE":
			return errors.BadRequest(st.message, "扩容容量不能小于当前申请容量")
		case "OBJECT_TOO_LARGE":
			return errors.BadRequest(st.message, "文件超过上传大小上限")
		}
		return errors.BadRequest("STORAGE_INVALID_REQUEST", "存储请求参数不合法")
	case "NotFound":
		return errors.NotFound("STORAGE_RESOURCE_NOT_FOUND", "存储资源不存在")
	case "PermissionDenied":
		if st.message == "STORAGE_ACCESS_DENIED" {
			return errors.Forbidden("STORAGE_ACCESS_DENIED", "该租户存储权限未开通，请联系管理员")
		}
		return errors.Forbidden("STORAGE_SCOPE_DENIED", "无权访问该存储资源")
	case "AlreadyExists":
		switch st.message {
		case "CREATE_TOKEN_CONFLICT":
			return errors.Conflict(st.message, "创建令牌已被不同的请求参数使用，请刷新后重新创建")
		case "VOLUME_IDENTITY_CHANGED":
			return errors.Conflict(st.message, "存储卷已被删除并重新创建，请刷新列表")
		case "VOLUME_IN_USE":
			return errors.Conflict(st.message, "存储卷仍被使用，无法删除")
		case "BUCKET_ALREADY_EXISTS":
			return errors.Conflict(st.message, "桶名已存在")
		case "OBJECT_ALREADY_EXISTS":
			return errors.Conflict(st.message, "文件已存在，请确认是否覆盖")
		case "OBJECT_IDENTITY_CHANGED":
			return errors.Conflict(st.message, "文件已被替换，请刷新后重试")
		}
		return errors.Conflict("STORAGE_RESOURCE_CONFLICT", "存储资源冲突")
	case "Aborted":
		return errors.Conflict("STORAGE_RESOURCE_CONFLICT", "存储对象已被其他请求修改，请刷新后重试")
	case "FailedPrecondition":
		switch st.message {
		case "DOWNLOAD_ENDPOINT_UNAVAILABLE":
			return errors.ServiceUnavailable(st.message, "当前环境未配置浏览器下载地址")
		case "UPLOAD_ENDPOINT_UNAVAILABLE":
			return errors.ServiceUnavailable(st.message, "当前环境未配置文件上传地址")
		case "BUCKET_CREATION_DISABLED":
			return errors.Forbidden(st.message, "当前环境未开放建桶")
		case "BUCKET_LIMIT_REACHED":
			return errors.Conflict(st.message, "桶数量已达上限，请先清理")
		case "VOLUME_TERMINATING":
			return errors.Conflict(st.message, "存储卷正在删除中")
		}
		return errors.BadRequest("VOLUME_EXPANSION_UNSUPPORTED", "该存储类型不支持当前操作")
	case "Unavailable":
		if st.message == "VOLUME_USAGE_UNKNOWN" {
			return errors.ServiceUnavailable(st.message, "无法确认存储卷使用情况，已阻止删除")
		}
		return errors.ServiceUnavailable("STORAGE_BACKEND_UNAVAILABLE", "存储后端暂时不可用")
	case "Unauthenticated":
		return errors.Unauthorized("INVALID_TRUSTED_IDENTITY", "trusted identity required")
	default:
		return errors.InternalServer("STORAGE_INTERNAL_ERROR", "storage operation failed")
	}
}

type storageStatus struct {
	code    string
	message string
}

// statusFrom 只解析带 gRPC status 的错误；普通错误保持 Internal。
func statusFrom(err error) (storageStatus, bool) {
	if err == nil {
		return storageStatus{}, false
	}
	value, ok := err.(interface{ GRPCStatus() *grpcstatus.Status })
	if !ok {
		return storageStatus{}, false
	}
	st := value.GRPCStatus()
	if st == nil {
		return storageStatus{}, false
	}
	return storageStatus{code: st.Code().String(), message: st.Message()}, true
}

func (s *StorageService) ListStorageClasses(ctx context.Context, r *storagepb.ListStorageClassesRequest) (*storagepb.ListStorageClassesResponse, error) {
	if r == nil {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	page, limit, err := storagePage(r.Page, r.PageSize)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListStorageClasses(ctx, tenant, actor, &resourcestorage.ListStorageClassesRequest{})
	if err != nil {
		return nil, mapStorageError(err)
	}
	total := int64(len(reply.GetItems()))
	items := paginateStorageClasses(reply.GetItems(), page, limit)
	out := &storagepb.ListStorageClassesResponse{Total: total, Items: make([]*storagepb.StorageClass, 0, len(items))}
	for _, item := range items {
		out.Items = append(out.Items, &storagepb.StorageClass{
			Name: item.GetName(), Provisioner: item.GetProvisioner(), IsDefault: item.GetIsDefault(),
			AllowVolumeExpansion: item.GetAllowVolumeExpansion(), VolumeBindingMode: item.GetVolumeBindingMode(),
			ReclaimPolicy: item.GetReclaimPolicy(),
		})
	}
	return out, nil
}

func paginateStorageClasses(items []*resourcestorage.StorageClass, page, limit uint32) []*resourcestorage.StorageClass {
	offset := int64(page-1) * int64(limit)
	if offset >= int64(len(items)) {
		return nil
	}
	end := offset + int64(limit)
	if end > int64(len(items)) {
		end = int64(len(items))
	}
	return items[offset:end]
}

func (s *StorageService) CreateVolume(ctx context.Context, r *storagepb.CreateVolumeRequest) (*storagepb.CreateVolumeResponse, error) {
	if r == nil || r.GetData() == nil {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	data := r.GetData()
	reply, err := s.client.CreateVolume(ctx, tenant, actor, &resourcestorage.CreateVolumeRequest{
		CreateToken: data.GetCreateToken(), DisplayName: data.GetDisplayName(), CapacityGb: data.GetCapacityGb(),
		StorageClassName: data.GetStorageClassName(), VolumeMode: data.GetVolumeMode(), AccessModes: data.GetAccessModes(),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	return &storagepb.CreateVolumeResponse{Volume: wireStorageVolume(reply.GetVolume())}, nil
}

func (s *StorageService) ListVolumes(ctx context.Context, r *storagepb.ListVolumesRequest) (*storagepb.ListVolumesResponse, error) {
	if r == nil {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	page, limit, err := storagePage(r.Page, r.PageSize)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListVolumes(ctx, tenant, actor, &resourcestorage.ListVolumesRequest{
		Page: page, PageSize: limit,
		Name: strings.TrimSpace(r.GetName()), Id: strings.TrimSpace(r.GetId()), State: r.GetState(),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	out := &storagepb.ListVolumesResponse{Total: reply.GetTotal(), Items: make([]*storagepb.Volume, 0, len(reply.GetItems()))}
	for _, item := range reply.GetItems() {
		out.Items = append(out.Items, wireStorageVolume(item))
	}
	return out, nil
}

func (s *StorageService) GetVolume(ctx context.Context, r *storagepb.GetVolumeRequest) (*storagepb.GetVolumeResponse, error) {
	if r == nil || r.GetPvcName() == "" {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetVolume(ctx, tenant, actor, &resourcestorage.GetVolumeRequest{PvcName: r.GetPvcName()})
	if err != nil {
		return nil, mapStorageError(err)
	}
	return &storagepb.GetVolumeResponse{Volume: wireStorageDetail(reply.GetDetail())}, nil
}

func (s *StorageService) UpdateVolume(ctx context.Context, r *storagepb.UpdateVolumeRequest) (*storagepb.UpdateVolumeResponse, error) {
	if r == nil || r.GetData() == nil {
		return nil, storageBadRequest()
	}
	if mask := r.GetUpdateMask(); mask != nil {
		for _, path := range mask.GetPaths() {
			if path != "capacity_gb" && path != "data.capacity_gb" {
				return nil, errors.BadRequest("STORAGE_UNSUPPORTED_UPDATE", "只支持修改 capacity_gb")
			}
		}
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	data := r.GetData()
	reply, err := s.client.UpdateVolume(ctx, tenant, actor, &resourcestorage.UpdateVolumeRequest{
		PvcName: data.GetPvcName(), ExpectedUid: data.GetExpectedUid(), CapacityGb: data.GetCapacityGb(),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	return &storagepb.UpdateVolumeResponse{Volume: wireStorageVolume(reply.GetVolume())}, nil
}

func (s *StorageService) DeleteVolume(ctx context.Context, r *storagepb.DeleteVolumeRequest) (*storagepb.DeleteVolumeResponse, error) {
	if r == nil || r.GetData() == nil {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	data := r.GetData()
	reply, err := s.client.DeleteVolume(ctx, tenant, actor, &resourcestorage.DeleteVolumeRequest{
		PvcName: data.GetPvcName(), ExpectedUid: data.GetExpectedUid(),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	out := &storagepb.DeleteVolumeResponse{State: reply.GetState()}
	if reply.GetVolume() != nil {
		out.Volume = wireStorageVolume(reply.GetVolume())
	}
	return out, nil
}

func (s *StorageService) ListBuckets(ctx context.Context, r *storagepb.ListBucketsRequest) (*storagepb.ListBucketsResponse, error) {
	if r == nil {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	page, limit, err := storagePage(r.Page, r.PageSize)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListBuckets(ctx, tenant, actor, &resourcestorage.ListBucketsRequest{
		Name: strings.TrimSpace(r.GetName()), Id: strings.TrimSpace(r.GetId()),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	total := int64(len(reply.GetItems()))
	out := &storagepb.ListBucketsResponse{Total: total, Items: make([]*storagepb.Bucket, 0, limit)}
	offset := int64(page-1) * int64(limit)
	for index, item := range reply.GetItems() {
		if int64(index) < offset || int64(index) >= offset+int64(limit) {
			continue
		}
		out.Items = append(out.Items, wireStorageBucket(item))
	}
	return out, nil
}

func (s *StorageService) ListObjects(ctx context.Context, r *storagepb.ListObjectsRequest) (*storagepb.ListObjectsResponse, error) {
	if r == nil || r.GetBucketId() == "" {
		return nil, storageBadRequest()
	}
	// 对象列表是分页合同的唯一例外：任何页码/偏移/旧游标参数都按未声明参数拒绝。
	if r.Page != nil || r.PageSize != nil || r.Offset != "" || r.Token != "" {
		return nil, errors.BadRequest("STORAGE_UNSUPPORTED_PAGINATION", "对象列表只接受 limit 与 cursor")
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListObjects(ctx, tenant, actor, &resourcestorage.ListObjectsRequest{
		BucketId: r.GetBucketId(), Prefix: r.GetPrefix(), Limit: r.Limit, Cursor: r.GetCursor(),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	out := &storagepb.ListObjectsResponse{
		CommonPrefixes: reply.GetCommonPrefixes(), NextCursor: reply.GetNextCursor(), IsTruncated: reply.GetIsTruncated(),
		Items: make([]*storagepb.ObjectItem, 0, len(reply.GetItems())),
	}
	for _, item := range reply.GetItems() {
		out.Items = append(out.Items, &storagepb.ObjectItem{
			Key: item.GetKey(), Size: item.GetSize(), Etag: item.GetEtag(), LastModified: item.GetLastModified(),
		})
	}
	return out, nil
}

func (s *StorageService) HeadObject(ctx context.Context, r *storagepb.HeadObjectRequest) (*storagepb.HeadObjectResponse, error) {
	if r == nil || r.GetData() == nil || r.GetData().GetKey() == "" {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	data := r.GetData()
	reply, err := s.client.HeadObject(ctx, tenant, actor, &resourcestorage.HeadObjectRequest{
		BucketId: data.GetBucketId(), Key: data.GetKey(),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	meta := reply.GetObject()
	return &storagepb.HeadObjectResponse{Object: &storagepb.ObjectMeta{
		Key: meta.GetKey(), Size: meta.GetSize(), Etag: meta.GetEtag(), ContentType: meta.GetContentType(), LastModified: meta.GetLastModified(),
	}}, nil
}

func (s *StorageService) GetDownloadURL(ctx context.Context, r *storagepb.GetDownloadURLRequest) (*storagepb.GetDownloadURLResponse, error) {
	storageNoStore(ctx)
	if r == nil || r.GetData() == nil || r.GetData().GetKey() == "" {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	data := r.GetData()
	reply, err := s.client.GetDownloadURL(ctx, tenant, actor, &resourcestorage.GetDownloadURLRequest{
		BucketId: data.GetBucketId(), Key: data.GetKey(),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	return &storagepb.GetDownloadURLResponse{Url: reply.GetUrl(), ExpiresAt: reply.GetExpiresAt()}, nil
}

func (s *StorageService) CreateBucket(ctx context.Context, r *storagepb.CreateBucketRequest) (*storagepb.CreateBucketResponse, error) {
	if r == nil || r.GetData() == nil || strings.TrimSpace(r.GetData().GetName()) == "" {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.CreateBucket(ctx, tenant, actor, &resourcestorage.CreateBucketRequest{
		Name: strings.TrimSpace(r.GetData().GetName()),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	return &storagepb.CreateBucketResponse{Bucket: wireStorageBucket(reply.GetBucket())}, nil
}

func wireStorageBucket(bucket *resourcestorage.Bucket) *storagepb.Bucket {
	if bucket == nil {
		return nil
	}
	return &storagepb.Bucket{
		Id: bucket.GetId(), Name: bucket.GetName(), CreatedAt: bucket.GetCreatedAt(),
		Region: bucket.GetRegion(), StorageClass: bucket.GetStorageClass(), Acl: bucket.GetAcl(),
	}
}

func (s *StorageService) GetBucketStats(ctx context.Context, r *storagepb.GetBucketStatsRequest) (*storagepb.GetBucketStatsResponse, error) {
	if r == nil || r.GetBucketId() == "" {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetBucketStats(ctx, tenant, actor, &resourcestorage.GetBucketStatsRequest{BucketId: r.GetBucketId()})
	if err != nil {
		return nil, mapStorageError(err)
	}
	return &storagepb.GetBucketStatsResponse{
		ObjectCount: reply.GetObjectCount(), TotalSize: reply.GetTotalSize(),
		Truncated: reply.GetTruncated(), Scanned: reply.GetScanned(),
	}, nil
}

func (s *StorageService) GetUploadURL(ctx context.Context, r *storagepb.GetUploadURLRequest) (*storagepb.GetUploadURLResponse, error) {
	storageNoStore(ctx)
	if r == nil || r.GetData() == nil || r.GetData().GetBucketId() == "" || r.GetData().GetKey() == "" || r.GetData().GetSize() <= 0 {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	data := r.GetData()
	reply, err := s.client.GetUploadURL(ctx, tenant, actor, &resourcestorage.GetUploadURLRequest{
		BucketId: data.GetBucketId(), Key: data.GetKey(), ContentType: data.GetContentType(),
		Size: data.GetSize(), Overwrite: data.GetOverwrite(),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	return &storagepb.GetUploadURLResponse{
		Url: reply.GetUrl(), Headers: reply.GetHeaders(), ExpiresAt: reply.GetExpiresAt(), Key: reply.GetKey(),
	}, nil
}

func (s *StorageService) DeleteObject(ctx context.Context, r *storagepb.DeleteObjectRequest) (*storagepb.DeleteObjectResponse, error) {
	if r == nil || r.GetData() == nil || r.GetData().GetBucketId() == "" || r.GetData().GetKey() == "" {
		return nil, storageBadRequest()
	}
	tenant, actor, err := s.trustedStorageOperator(ctx)
	if err != nil {
		return nil, err
	}
	data := r.GetData()
	reply, err := s.client.DeleteObject(ctx, tenant, actor, &resourcestorage.DeleteObjectRequest{
		BucketId: data.GetBucketId(), Key: data.GetKey(), ExpectedEtag: data.GetExpectedEtag(),
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	return &storagepb.DeleteObjectResponse{State: reply.GetState()}, nil
}

func wireStorageVolume(volume *resourcestorage.Volume) *storagepb.Volume {
	if volume == nil {
		return nil
	}
	return &storagepb.Volume{
		PvcName: volume.GetPvcName(), Uid: volume.GetUid(), DisplayName: volume.GetDisplayName(),
		StorageClassName: volume.GetStorageClassName(), VolumeMode: volume.GetVolumeMode(),
		AccessModes: volume.GetAccessModes(), RequestedCapacityGb: volume.GetRequestedCapacityGb(),
		ObservedCapacityBytes: volume.GetObservedCapacityBytes(), ObservedCapacityKnown: volume.GetObservedCapacityKnown(),
		ObservedCapacityQuantity: volume.GetObservedCapacityQuantity(), Phase: volume.GetPhase(),
		Deleting: volume.GetDeleting(), ReclaimPolicy: volume.GetReclaimPolicy(), ReclaimPolicyKnown: volume.GetReclaimPolicyKnown(),
	}
}

func wireStorageDetail(detail *resourcestorage.VolumeDetail) *storagepb.VolumeDetail {
	if detail == nil {
		return nil
	}
	out := &storagepb.VolumeDetail{Volume: wireStorageVolume(detail.GetVolume())}
	for _, condition := range detail.GetConditions() {
		out.Conditions = append(out.Conditions, &storagepb.VolumeCondition{
			Type: condition.GetType(), Status: condition.GetStatus(), Reason: condition.GetReason(),
			Message: condition.GetMessage(), LastTransitionTime: condition.GetLastTransitionTime(),
		})
	}
	for _, event := range detail.GetEvents() {
		out.Events = append(out.Events, &storagepb.VolumeEvent{
			Type: event.GetType(), Reason: event.GetReason(), Message: event.GetMessage(),
			Count: event.GetCount(), LastTimestamp: event.GetLastTimestamp(),
		})
	}
	for _, consumer := range detail.GetConsumers() {
		out.Consumers = append(out.Consumers, &storagepb.VolumeConsumer{
			Kind: consumer.GetKind(), Namespace: consumer.GetNamespace(), Name: consumer.GetName(),
		})
	}
	return out
}
