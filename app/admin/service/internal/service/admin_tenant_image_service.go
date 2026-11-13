package service

import (
	"context"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
)

// ImageTenantModuleGate 是代管目标租户的套餐与存在性只读检查。
// 由 data.PlanModuleRepo 实现，测试可替换。
type ImageTenantModuleGate interface {
	TenantExists(context.Context, uint32) (bool, error)
	TenantModuleEnabled(context.Context, uint32, identityV1.Module) (bool, error)
}

// AdminTenantImageService 是平台代管入口。平台操作者身份保持不变，目标租户只
// 来自路径；用于“开通/扩张”的操作额外要求目标租户套餐包含 IMAGE 模块，而查看、
// 停用、注销允许已停用或套餐已变更的目标租户，便于回收。
type AdminTenantImageService struct {
	admin.UnimplementedAdminTenantImageServiceServer
	client     ImageManagementClient
	resolver   ResourceTenantResolver
	moduleGate ImageTenantModuleGate
}

func NewAdminTenantImageService(client ImageManagementClient, resolver ResourceTenantResolver, gate ImageTenantModuleGate) *AdminTenantImageService {
	return &AdminTenantImageService{client: client, resolver: resolver, moduleGate: gate}
}

// resolveManagedImageTenant 解析代管目标：target 必须非 0；租户必须存在且未删除；
// 套餐检查只在 expand 为 true（开通/扩张类操作）时执行。返回 Res 绑定的规范 UUID。
func (s *AdminTenantImageService) resolveManagedImageTenant(ctx context.Context, target uint32, action string, expand bool) (string, string, error) {
	actor, _, err := trustedPlatformImageOperator(ctx, action)
	if err != nil {
		return "", "", err
	}
	if target == 0 {
		return "", "", imageBadRequest()
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return "", "", err
	}
	if s.resolver == nil || s.moduleGate == nil {
		return "", "", imagePlatformUnavailable()
	}
	ok, err := s.moduleGate.TenantExists(ctx, target)
	if err != nil {
		return "", "", errors.ServiceUnavailable("IMAGE_MANAGEMENT_UNAVAILABLE", "tenant lookup unavailable")
	}
	if !ok {
		return "", "", errors.NotFound("IMAGE_TENANT_NOT_FOUND", "target tenant not found")
	}
	if expand {
		allowed, e := s.moduleGate.TenantModuleEnabled(ctx, target, identityV1.Module_IMAGE)
		if e != nil {
			return "", "", errors.ServiceUnavailable("IMAGE_MANAGEMENT_UNAVAILABLE", "tenant module lookup unavailable")
		}
		if !allowed {
			return "", "", errors.Forbidden("IMAGE_MODULE_REQUIRED", "target tenant has no IMAGE subscription")
		}
	}
	tenant, err := s.resolver.ResourceTenantID(ctx, target)
	if err != nil {
		return "", "", errors.ServiceUnavailable("TENANT_MAPPING_UNAVAILABLE", "tenant mapping unavailable")
	}
	if !imageUUID(tenant) {
		return "", "", errors.ServiceUnavailable("TENANT_MAPPING_INVALID", "tenant mapping unavailable")
	}
	return tenant, actor, nil
}

// validateManagedSpace 校验代管空间回包：tenant 必须等于已映射的 UUID。
func validateManagedSpace(v *imagev1.ImageSpace, tenant string) error {
	return validateImageSpace(v, tenant)
}

// validateManagedCredential 校验代管凭据回包：tenant 必须等于已映射的 UUID。
func validateManagedCredential(v *imagev1.PublisherCredential, tenant string) error {
	return validateImageCredential(v, tenant)
}

// validateManagedImage 校验代管镜像回包：scope=TENANT 且 tenant 必须是目标租户。
func validateManagedImage(v *imagev1.ImageRegistration, tenant, id string) error {
	return validateImageReply(v, tenant, imagev1.ImageScope_IMAGE_SCOPE_TENANT, id)
}

func (s *AdminTenantImageService) GetTenantImageSpace(ctx context.Context, r *admin.GetTenantImageSpaceRequest) (*admin.GetTenantImageSpaceResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-space-get", false)
	if err != nil {
		return nil, err
	}
	v, err := s.client.GetTenantImageSpace(ctx, tenant, actor, &imagev1.GetTenantImageSpaceRequest{TargetTenantId: tenant})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateManagedSpace(v.GetSpace(), tenant); err != nil {
		return nil, err
	}
	return &admin.GetTenantImageSpaceResponse{Space: wireImageSpace(v.GetSpace())}, nil
}

func (s *AdminTenantImageService) EnsureTenantImageSpace(ctx context.Context, r *admin.EnsureTenantImageSpaceRequest) (*admin.EnsureTenantImageSpaceResponse, error) {
	if r == nil || r.GetData() == nil || !imageIdempotencyKey(r.GetData().GetIdempotencyKey()) {
		return nil, imageBadRequest()
	}
	// slug 上限与 Res 的 ParseSlug 一致（3–40 字符）：Gov 提前拒绝会让 Res 本可
	// 开通的空间在入口就被挡下。
	if slug := strings.TrimSpace(r.GetData().GetSlug()); slug == "" || len(slug) > 40 || strings.ToLower(slug) != slug {
		return nil, imageBadRequest()
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-space-enable", true)
	if err != nil {
		return nil, err
	}
	v, err := s.client.EnsureTenantImageSpace(ctx, tenant, actor, &imagev1.EnsureTenantImageSpaceRequest{TargetTenantId: tenant, Slug: r.GetData().GetSlug(), IdempotencyKey: r.GetData().GetIdempotencyKey()})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateManagedSpace(v.GetSpace(), tenant); err != nil {
		return nil, err
	}
	return &admin.EnsureTenantImageSpaceResponse{Space: wireImageSpace(v.GetSpace())}, nil
}

func (s *AdminTenantImageService) GetTenantPublisherCredential(ctx context.Context, r *admin.GetTenantPublisherCredentialRequest) (*admin.GetTenantPublisherCredentialResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-credential-get", false)
	if err != nil {
		return nil, err
	}
	v, err := s.client.GetTenantPublisherCredential(ctx, tenant, actor, &imagev1.GetTenantPublisherCredentialRequest{TargetTenantId: tenant})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateManagedCredential(v.GetCredential(), tenant); err != nil {
		return nil, err
	}
	return &admin.GetTenantPublisherCredentialResponse{Credential: wireImageCredential(v.GetCredential())}, nil
}

func (s *AdminTenantImageService) IssueTenantPublisherCredential(ctx context.Context, r *admin.IssueTenantPublisherCredentialRequest) (*admin.IssueTenantPublisherCredentialResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	expected, key, err := imageCredentialData(r.GetData())
	if err != nil {
		return nil, err
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-credential-issue", true)
	if err != nil {
		return nil, err
	}
	v, err := s.client.IssueTenantPublisher(ctx, tenant, actor, &imagev1.IssueTenantPublisherCredentialRequest{TargetTenantId: tenant, IdempotencyKey: key, ExpectedVersion: &expected})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateImageDelivery(v.GetCredential(), tenant, v.GetSecret(), v.GetReplayUntil()); err != nil {
		return nil, err
	}
	return &admin.IssueTenantPublisherCredentialResponse{Credential: wireImageCredential(v.GetCredential()), Secret: v.GetSecret(), ReplayUntil: v.GetReplayUntil()}, nil
}

func (s *AdminTenantImageService) ResetTenantPublisherCredential(ctx context.Context, r *admin.ResetTenantPublisherCredentialRequest) (*admin.ResetTenantPublisherCredentialResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	expected, key, err := imageCredentialData(r.GetData())
	if err != nil {
		return nil, err
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-credential-reset", true)
	if err != nil {
		return nil, err
	}
	v, err := s.client.ResetTenantPublisher(ctx, tenant, actor, &imagev1.ResetTenantPublisherCredentialRequest{TargetTenantId: tenant, IdempotencyKey: key, ExpectedVersion: &expected})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateImageDelivery(v.GetCredential(), tenant, v.GetSecret(), v.GetReplayUntil()); err != nil {
		return nil, err
	}
	return &admin.ResetTenantPublisherCredentialResponse{Credential: wireImageCredential(v.GetCredential()), Secret: v.GetSecret(), ReplayUntil: v.GetReplayUntil()}, nil
}

func (s *AdminTenantImageService) DisableTenantPublisherCredential(ctx context.Context, r *admin.DisableTenantPublisherCredentialRequest) (*admin.DisableTenantPublisherCredentialResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	expected, key, err := imageCredentialData(r.GetData())
	if err != nil {
		return nil, err
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-credential-disable", false)
	if err != nil {
		return nil, err
	}
	v, err := s.client.DisableTenantPublisher(ctx, tenant, actor, &imagev1.DisableTenantPublisherCredentialRequest{TargetTenantId: tenant, IdempotencyKey: key, ExpectedVersion: &expected})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateManagedCredential(v.GetCredential(), tenant); err != nil {
		return nil, err
	}
	if v.GetCredential().GetState() != "disabled" {
		return nil, imageInvalidReply()
	}
	return &admin.DisableTenantPublisherCredentialResponse{Credential: wireImageCredential(v.GetCredential())}, nil
}

func (s *AdminTenantImageService) ListTenantImages(ctx context.Context, r *admin.ListTenantImagesRequest) (*admin.ListTenantImagesResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-registration-list", false)
	if err != nil {
		return nil, err
	}
	page, size, err := resolveImagePage(r.Page, r.PageSize)
	if err != nil {
		return nil, err
	}
	purposes, accelerator, err := imageManagementDeclarations(r.GetPurposes(), r.GetAccelerator())
	if err != nil {
		return nil, err
	}
	v, err := s.client.ListTenantImages(ctx, tenant, actor, &imagev1.ListTenantImagesRequest{TargetTenantId: tenant, Search: r.GetSearch(), Purposes: purposes, Accelerator: accelerator, Page: &page, PageSize: &size})
	if err != nil {
		return nil, mapImageError(err)
	}
	result, err := imageManagementPage(v.GetPage(), size, func(item *imagev1.ImageRegistration) error {
		return validateManagedImage(item, tenant, "")
	})
	if err != nil {
		return nil, err
	}
	return &admin.ListTenantImagesResponse{Items: result.GetItems(), Total: result.GetTotal()}, nil
}

func (s *AdminTenantImageService) GetTenantImage(ctx context.Context, r *admin.GetTenantImageRequest) (*admin.GetTenantImageResponse, error) {
	if r == nil || !imageIDPattern.MatchString(r.GetImageId()) {
		return nil, imageBadRequest()
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-registration-get", false)
	if err != nil {
		return nil, err
	}
	v, err := s.client.GetTenantImage(ctx, tenant, actor, &imagev1.GetTenantImageRequest{TargetTenantId: tenant, ImageId: r.GetImageId()})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateManagedImage(v.GetImage(), tenant, r.GetImageId()); err != nil {
		return nil, err
	}
	return &admin.GetTenantImageResponse{Image: wireImageRegistration(v.GetImage())}, nil
}

func (s *AdminTenantImageService) RegisterTenantImage(ctx context.Context, r *admin.RegisterTenantImageRequest) (*admin.RegisterTenantImageResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	request, err := imageRegisterData(r.GetData())
	if err != nil {
		return nil, err
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-registration-create", true)
	if err != nil {
		return nil, err
	}
	v, err := s.client.RegisterTenantImage(ctx, tenant, actor, &imagev1.RegisterTenantImageRequest{TargetTenantId: tenant, ImageReference: request.GetImageReference(), DisplayName: request.GetDisplayName(), Description: request.GetDescription(), Purposes: request.GetPurposes(), Accelerator: request.GetAccelerator(), IdempotencyKey: request.GetIdempotencyKey()})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateManagedImage(v.GetImage(), tenant, ""); err != nil {
		return nil, err
	}
	return &admin.RegisterTenantImageResponse{Image: wireImageRegistration(v.GetImage())}, nil
}

func (s *AdminTenantImageService) UpdateTenantImage(ctx context.Context, r *admin.UpdateTenantImageRequest) (*admin.UpdateTenantImageResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	request, err := imagePatchData(r.GetImageId(), r.GetData(), r.GetUpdateMask())
	if err != nil {
		return nil, err
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-registration-update", true)
	if err != nil {
		return nil, err
	}
	v, err := s.client.UpdateTenantImage(ctx, tenant, actor, &imagev1.UpdateTenantImageRequest{TargetTenantId: tenant, ImageId: request.GetImageId(), DisplayName: request.DisplayName, Description: request.Description, Purposes: request.GetPurposes(), Accelerator: request.Accelerator, ExpectedVersion: request.ExpectedVersion, IdempotencyKey: request.GetIdempotencyKey(), UpdateMask: request.GetUpdateMask()})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateManagedImage(v.GetImage(), tenant, r.GetImageId()); err != nil {
		return nil, err
	}
	return &admin.UpdateTenantImageResponse{Image: wireImageRegistration(v.GetImage())}, nil
}

func (s *AdminTenantImageService) UnregisterTenantImage(ctx context.Context, r *admin.UnregisterTenantImageRequest) (*admin.UnregisterTenantImageResponse, error) {
	if r == nil || !imageIDPattern.MatchString(r.GetImageId()) {
		return nil, imageBadRequest()
	}
	expected, key, err := imageUnregisterData(r.GetData())
	if err != nil {
		return nil, err
	}
	tenant, actor, err := s.resolveManagedImageTenant(ctx, r.GetTargetTenantId(), "tenant-registration-unregister", false)
	if err != nil {
		return nil, err
	}
	v, err := s.client.UnregisterTenantImage(ctx, tenant, actor, &imagev1.UnregisterTenantImageRequest{TargetTenantId: tenant, ImageId: r.GetImageId(), ExpectedVersion: &expected, IdempotencyKey: key})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validateManagedImage(v.GetImage(), tenant, r.GetImageId()); err != nil {
		return nil, err
	}
	if v.GetImage().GetUnregisteredAt() == nil {
		return nil, imageInvalidReply()
	}
	return &admin.UnregisterTenantImageResponse{Image: wireImageRegistration(v.GetImage())}, nil
}
