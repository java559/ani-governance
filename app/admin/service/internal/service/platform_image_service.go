package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ImageManagementClient 是平台镜像管理下游客户端的接口形态，测试可替换实现。
type ImageManagementClient interface {
	GetPlatformSpace(context.Context, string) (*imagev1.GetPlatformSpaceResponse, error)
	InitializePlatform(context.Context, string, *imagev1.InitializePlatformRequest) (*imagev1.InitializePlatformResponse, error)
	GetPlatformPublisherCredential(context.Context, string) (*imagev1.GetPlatformPublisherCredentialResponse, error)
	IssuePlatformPublisher(context.Context, string, *imagev1.IssuePlatformPublisherCredentialRequest) (*imagev1.IssuePlatformPublisherCredentialResponse, error)
	ResetPlatformPublisher(context.Context, string, *imagev1.ResetPlatformPublisherCredentialRequest) (*imagev1.ResetPlatformPublisherCredentialResponse, error)
	DisablePlatformPublisher(context.Context, string, *imagev1.DisablePlatformPublisherCredentialRequest) (*imagev1.DisablePlatformPublisherCredentialResponse, error)
	ListPlatformImages(context.Context, string, *imagev1.ListPlatformImagesRequest) (*imagev1.ListPlatformImagesResponse, error)
	GetPlatformImage(context.Context, string, *imagev1.GetPlatformImageRequest) (*imagev1.GetPlatformImageResponse, error)
	RegisterPlatformImage(context.Context, string, *imagev1.RegisterPlatformImageRequest) (*imagev1.RegisterPlatformImageResponse, error)
	UpdatePlatformImage(context.Context, string, *imagev1.UpdatePlatformImageRequest) (*imagev1.UpdatePlatformImageResponse, error)
	UnregisterPlatformImage(context.Context, string, *imagev1.UnregisterPlatformImageRequest) (*imagev1.UnregisterPlatformImageResponse, error)

	GetTenantImageSpace(context.Context, string, string, *imagev1.GetTenantImageSpaceRequest) (*imagev1.GetTenantImageSpaceResponse, error)
	EnsureTenantImageSpace(context.Context, string, string, *imagev1.EnsureTenantImageSpaceRequest) (*imagev1.EnsureTenantImageSpaceResponse, error)
	GetTenantPublisherCredential(context.Context, string, string, *imagev1.GetTenantPublisherCredentialRequest) (*imagev1.GetTenantPublisherCredentialResponse, error)
	IssueTenantPublisher(context.Context, string, string, *imagev1.IssueTenantPublisherCredentialRequest) (*imagev1.IssueTenantPublisherCredentialResponse, error)
	ResetTenantPublisher(context.Context, string, string, *imagev1.ResetTenantPublisherCredentialRequest) (*imagev1.ResetTenantPublisherCredentialResponse, error)
	DisableTenantPublisher(context.Context, string, string, *imagev1.DisableTenantPublisherCredentialRequest) (*imagev1.DisableTenantPublisherCredentialResponse, error)
	ListTenantImages(context.Context, string, string, *imagev1.ListTenantImagesRequest) (*imagev1.ListTenantImagesResponse, error)
	GetTenantImage(context.Context, string, string, *imagev1.GetTenantImageRequest) (*imagev1.GetTenantImageResponse, error)
	RegisterTenantImage(context.Context, string, string, *imagev1.RegisterTenantImageRequest) (*imagev1.RegisterTenantImageResponse, error)
	UpdateTenantImage(context.Context, string, string, *imagev1.UpdateTenantImageRequest) (*imagev1.UpdateTenantImageResponse, error)
	UnregisterTenantImage(context.Context, string, string, *imagev1.UnregisterTenantImageRequest) (*imagev1.UnregisterTenantImageResponse, error)
}

// PlatformImageService 是平台镜像管理入口。只有平台用户 JWT + 逐操作权限码
// 才能进入；AK、sandbox token 与运行时身份一律拒绝，且任何请求都不能指定租户。
type PlatformImageService struct {
	admin.UnimplementedPlatformImageServiceServer
	client ImageManagementClient
}

func NewPlatformImageService(client ImageManagementClient) *PlatformImageService {
	return &PlatformImageService{client: client}
}

// trustedPlatformImageOperator 复刻 PlatformNetworkService.actor 的平台身份判定，
// 并额外要求路由表里登记的权限码。返回值是审计用 actor。
func trustedPlatformImageOperator(ctx context.Context, action string) (string, data.ImageManagementRoute, error) {
	route, ok := data.ImageManagementActionRoute(action)
	if !ok {
		return "", data.ImageManagementRoute{}, errors.Forbidden("IMAGE_PLATFORM_DENIED", "image platform action denied")
	}
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return "", route, errors.Unauthorized("INVALID_LOGIN", "login required")
	}
	if p.Type != auth.SubjectUser || p.TenantID != 0 || p.Claims == nil || p.Claims.GetTenantId() != 0 || p.ID != p.Claims.GetUserId() || !hasPlatformRole(p.Roles) || !hasPermission(p.Claims, route.Permission) {
		return "", route, errors.Forbidden("IMAGE_PLATFORM_DENIED", "platform image permission required")
	}
	actor, err := p.Actor()
	if err != nil {
		return "", route, errors.Unauthorized("INVALID_LOGIN", "login required")
	}
	return actor, route, nil
}

func imagePlatformUnavailable() error {
	return errors.ServiceUnavailable("IMAGE_MANAGEMENT_UNAVAILABLE", "image management is not configured")
}

func requireImageManagementClient(c ImageManagementClient) error {
	if c == nil {
		return imagePlatformUnavailable()
	}
	return nil
}

// validatePlatformSpace 校验平台空间回包：平台域没有 tenant，项目名是平台项目
// 而非 t- 前缀的租户项目，因此不能复用租户侧校验。
func validatePlatformSpace(v *imagev1.ImageSpace) error {
	if v == nil || v.TenantId != "" || !imageUUID(v.SpaceId) || !imagePlatformProjectPattern.MatchString(v.ProjectName) || len(v.ProjectName) > 42 {
		return imageInvalidReply()
	}
	if v.RegistryAuthority == "" || strings.ContainsAny(v.RegistryAuthority, "/@?#%\\ \r\n") || v.Version < 1 || !imageTime(v.CreatedAt) || !imageTime(v.UpdatedAt) {
		return imageInvalidReply()
	}
	switch v.State {
	case "provisioning", "blocked":
		return nil
	case "available":
		if v.PullCredentialGeneration < 0 {
			return imageInvalidReply()
		}
		return nil
	}
	return imageInvalidReply()
}

// validatePlatformCredential 校验平台凭据回包：tenant 必须为空。
func validatePlatformCredential(v *imagev1.PublisherCredential) error {
	return validateImageCredential(v, "")
}

// validatePlatformDelivery 只有 Issue/Reset 允许携带 secret，且必须仍在重放窗口内。
func validatePlatformDelivery(v *imagev1.PublisherCredential, secret string, until *timestamppb.Timestamp) error {
	return validateImageDelivery(v, "", secret, until)
}

// validatePlatformImage 校验平台镜像回包：scope=PLATFORM 且 tenant 必须为空。
func validatePlatformImage(v *imagev1.ImageRegistration, id string) error {
	return validateImageReply(v, "", imagev1.ImageScope_IMAGE_SCOPE_PLATFORM, id)
}

func (s *PlatformImageService) GetPlatformSpace(ctx context.Context, r *admin.GetPlatformSpaceRequest) (*admin.GetPlatformSpaceResponse, error) {
	imageNoStore(ctx)
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-space-get")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.GetPlatformSpace(ctx, actor)
	if err != nil {
		return nil, mapImageError(err)
	}
	value := v.GetView()
	if value == nil {
		return nil, imageInvalidReply()
	}
	out := &view.PlatformSpaceView{ConfiguredProject: value.GetConfiguredProject(), ConfiguredRegistryAuthority: value.GetConfiguredRegistryAuthority(), BindingStatus: value.GetBindingStatus()}
	if !imagePlatformProjectPattern.MatchString(out.ConfiguredProject) || out.ConfiguredRegistryAuthority == "" || strings.ContainsAny(out.ConfiguredRegistryAuthority, "/@?#%\\ \r\n") {
		return nil, imageInvalidReply()
	}
	switch out.BindingStatus {
	case "uninitialized":
		// 未开通时不允许凭空带回一个空间对象。
		if value.GetSpace() != nil {
			return nil, imageInvalidReply()
		}
	case "ready", "provisioning", "blocked", "configuration_mismatch":
		if err = validatePlatformSpace(value.GetSpace()); err != nil {
			return nil, err
		}
		out.Space = wireImageSpace(value.GetSpace())
	default:
		return nil, imageInvalidReply()
	}
	return &admin.GetPlatformSpaceResponse{Space: out}, nil
}

func (s *PlatformImageService) InitializePlatform(ctx context.Context, r *admin.InitializePlatformRequest) (*admin.InitializePlatformResponse, error) {
	imageNoStore(ctx)
	if r == nil || r.GetData() == nil || !imageIdempotencyKey(r.GetData().GetIdempotencyKey()) {
		return nil, imageBadRequest()
	}
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-space-enable")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.InitializePlatform(ctx, actor, &imagev1.InitializePlatformRequest{IdempotencyKey: r.GetData().GetIdempotencyKey()})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validatePlatformSpace(v.GetSpace()); err != nil {
		return nil, err
	}
	return &admin.InitializePlatformResponse{Space: wireImageSpace(v.GetSpace())}, nil
}

func (s *PlatformImageService) GetPlatformPublisherCredential(ctx context.Context, r *admin.GetPlatformPublisherCredentialRequest) (*admin.GetPlatformPublisherCredentialResponse, error) {
	imageNoStore(ctx)
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-credential-get")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.GetPlatformPublisherCredential(ctx, actor)
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validatePlatformCredential(v.GetCredential()); err != nil {
		return nil, err
	}
	return &admin.GetPlatformPublisherCredentialResponse{Credential: wireImageCredential(v.GetCredential())}, nil
}

func (s *PlatformImageService) IssuePlatformPublisherCredential(ctx context.Context, r *admin.IssuePlatformPublisherCredentialRequest) (*admin.IssuePlatformPublisherCredentialResponse, error) {
	imageNoStore(ctx)
	if r == nil {
		return nil, imageBadRequest()
	}
	expected, key, err := imageCredentialData(r.GetData())
	if err != nil {
		return nil, err
	}
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-credential-issue")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.IssuePlatformPublisher(ctx, actor, &imagev1.IssuePlatformPublisherCredentialRequest{IdempotencyKey: key, ExpectedVersion: &expected})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validatePlatformDelivery(v.GetCredential(), v.GetSecret(), v.GetReplayUntil()); err != nil {
		return nil, err
	}
	return &admin.IssuePlatformPublisherCredentialResponse{Credential: wireImageCredential(v.GetCredential()), Secret: v.GetSecret(), ReplayUntil: v.GetReplayUntil()}, nil
}

func (s *PlatformImageService) ResetPlatformPublisherCredential(ctx context.Context, r *admin.ResetPlatformPublisherCredentialRequest) (*admin.ResetPlatformPublisherCredentialResponse, error) {
	imageNoStore(ctx)
	if r == nil {
		return nil, imageBadRequest()
	}
	expected, key, err := imageCredentialData(r.GetData())
	if err != nil {
		return nil, err
	}
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-credential-reset")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.ResetPlatformPublisher(ctx, actor, &imagev1.ResetPlatformPublisherCredentialRequest{IdempotencyKey: key, ExpectedVersion: &expected})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validatePlatformDelivery(v.GetCredential(), v.GetSecret(), v.GetReplayUntil()); err != nil {
		return nil, err
	}
	return &admin.ResetPlatformPublisherCredentialResponse{Credential: wireImageCredential(v.GetCredential()), Secret: v.GetSecret(), ReplayUntil: v.GetReplayUntil()}, nil
}

func (s *PlatformImageService) DisablePlatformPublisherCredential(ctx context.Context, r *admin.DisablePlatformPublisherCredentialRequest) (*admin.DisablePlatformPublisherCredentialResponse, error) {
	imageNoStore(ctx)
	if r == nil {
		return nil, imageBadRequest()
	}
	expected, key, err := imageCredentialData(r.GetData())
	if err != nil {
		return nil, err
	}
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-credential-disable")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.DisablePlatformPublisher(ctx, actor, &imagev1.DisablePlatformPublisherCredentialRequest{IdempotencyKey: key, ExpectedVersion: &expected})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validatePlatformCredential(v.GetCredential()); err != nil {
		return nil, err
	}
	if v.GetCredential().GetState() != "disabled" {
		return nil, imageInvalidReply()
	}
	return &admin.DisablePlatformPublisherCredentialResponse{Credential: wireImageCredential(v.GetCredential())}, nil
}

func (s *PlatformImageService) ListPlatformImages(ctx context.Context, r *admin.ListPlatformImagesRequest) (*admin.ListPlatformImagesResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-registration-list")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
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
	v, err := s.client.ListPlatformImages(ctx, actor, &imagev1.ListPlatformImagesRequest{Search: r.GetSearch(), Purposes: purposes, Accelerator: accelerator, Page: &page, PageSize: &size})
	if err != nil {
		return nil, mapImageError(err)
	}
	result, err := imageManagementPage(v.GetPage(), size, func(item *imagev1.ImageRegistration) error {
		return validatePlatformImage(item, "")
	})
	if err != nil {
		return nil, err
	}
	return &admin.ListPlatformImagesResponse{Items: result.GetItems(), Total: result.GetTotal()}, nil
}

func (s *PlatformImageService) GetPlatformImage(ctx context.Context, r *admin.GetPlatformImageRequest) (*admin.GetPlatformImageResponse, error) {
	if r == nil || !imageIDPattern.MatchString(r.GetImageId()) {
		return nil, imageBadRequest()
	}
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-registration-get")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.GetPlatformImage(ctx, actor, &imagev1.GetPlatformImageRequest{ImageId: r.GetImageId()})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validatePlatformImage(v.GetImage(), r.GetImageId()); err != nil {
		return nil, err
	}
	return &admin.GetPlatformImageResponse{Image: wireImageRegistration(v.GetImage())}, nil
}

func (s *PlatformImageService) RegisterPlatformImage(ctx context.Context, r *admin.RegisterPlatformImageRequest) (*admin.RegisterPlatformImageResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	request, err := imageRegisterData(r.GetData())
	if err != nil {
		return nil, err
	}
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-registration-create")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.RegisterPlatformImage(ctx, actor, request)
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validatePlatformImage(v.GetImage(), ""); err != nil {
		return nil, err
	}
	return &admin.RegisterPlatformImageResponse{Image: wireImageRegistration(v.GetImage())}, nil
}

func (s *PlatformImageService) UpdatePlatformImage(ctx context.Context, r *admin.UpdatePlatformImageRequest) (*admin.UpdatePlatformImageResponse, error) {
	if r == nil {
		return nil, imageBadRequest()
	}
	request, err := imagePatchData(r.GetImageId(), r.GetData(), r.GetUpdateMask())
	if err != nil {
		return nil, err
	}
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-registration-update")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.UpdatePlatformImage(ctx, actor, request)
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validatePlatformImage(v.GetImage(), r.GetImageId()); err != nil {
		return nil, err
	}
	return &admin.UpdatePlatformImageResponse{Image: wireImageRegistration(v.GetImage())}, nil
}

func (s *PlatformImageService) UnregisterPlatformImage(ctx context.Context, r *admin.UnregisterPlatformImageRequest) (*admin.UnregisterPlatformImageResponse, error) {
	if r == nil || !imageIDPattern.MatchString(r.GetImageId()) {
		return nil, imageBadRequest()
	}
	expected, key, err := imageUnregisterData(r.GetData())
	if err != nil {
		return nil, err
	}
	actor, _, err := trustedPlatformImageOperator(ctx, "platform-registration-unregister")
	if err != nil {
		return nil, err
	}
	if err = requireImageManagementClient(s.client); err != nil {
		return nil, err
	}
	v, err := s.client.UnregisterPlatformImage(ctx, actor, &imagev1.UnregisterPlatformImageRequest{ImageId: r.GetImageId(), ExpectedVersion: &expected, IdempotencyKey: key})
	if err != nil {
		return nil, mapImageError(err)
	}
	if err = validatePlatformImage(v.GetImage(), r.GetImageId()); err != nil {
		return nil, err
	}
	if v.GetImage().GetUnregisteredAt() == nil {
		return nil, imageInvalidReply()
	}
	return &admin.UnregisterPlatformImageResponse{Image: wireImageRegistration(v.GetImage())}, nil
}

// imageManagementDeclarations 复用既有 purpose/accelerator 词表，以可重复的
// 字符串形式接收，供两条管理分页接口使用。
func imageManagementDeclarations(purposes []string, accelerator string) ([]imagev1.ImagePurpose, imagev1.AcceleratorKind, error) {
	if len(purposes) > 5 {
		return nil, 0, imageBadRequest()
	}
	seen := map[string]bool{}
	for _, p := range purposes {
		if seen[p] {
			return nil, 0, imageBadRequest()
		}
		seen[p] = true
	}
	return imageDeclarations(purposes, accelerator)
}

// imageManagementPage 校验管理分页回包：条数不超过 pageSize、item 唯一且归属正确、
// total 非负；管理分页没有 next_cursor，因此响应结构里也不存在该字段。
func imageManagementPage(p *imagev1.ManagementImagePage, size uint32, validate func(*imagev1.ImageRegistration) error) (*view.ManagementImagePage, error) {
	if p == nil || p.GetTotal() < 0 || len(p.GetItems()) > int(size) {
		return nil, imageInvalidReply()
	}
	out := &view.ManagementImagePage{Total: p.GetTotal(), Items: make([]*view.ImageRegistration, 0, len(p.GetItems()))}
	for _, item := range p.GetItems() {
		if item.GetUnregisteredAt() != nil {
			return nil, imageInvalidReply()
		}
		if err := validate(item); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, wireImageRegistration(item))
	}
	return out, nil
}

func imageIdempotencyKey(key string) bool {
	return key != "" && len(key) <= 256 && utf8.ValidString(key) && !strings.ContainsAny(key, "\r\n\x00 ")
}

// imageCredentialData 读取签发/停用的控制字段：expectedVersion 必填且 >= 0
// （0 表示“从未签发”），idempotencyKey 必填。
func imageCredentialData(d *view.ImageManagementCredentialData) (int64, string, error) {
	if d == nil || !imageIdempotencyKey(d.GetIdempotencyKey()) || d.ExpectedVersion == nil || d.GetExpectedVersion() < 0 {
		return 0, "", imageBadRequest()
	}
	return d.GetExpectedVersion(), d.GetIdempotencyKey(), nil
}

func imageRegisterData(d *view.ImageManagementRegisterData) (*imagev1.RegisterPlatformImageRequest, error) {
	if d == nil || !imageIdempotencyKey(d.GetIdempotencyKey()) {
		return nil, imageBadRequest()
	}
	reference := d.GetImageReference()
	if reference == "" || len(reference) > 512 || strings.ContainsAny(reference, "\r\n\x00 ") {
		return nil, imageBadRequest()
	}
	if strings.TrimSpace(d.GetDisplayName()) == "" || utf8.RuneCountInString(d.GetDisplayName()) > 100 || utf8.RuneCountInString(d.GetDescription()) > 2000 {
		return nil, imageBadRequest()
	}
	if len(d.GetPurposes()) == 0 || len(d.GetPurposes()) > 5 {
		return nil, imageBadRequest()
	}
	purposes, accelerator, err := imageManagementDeclarations(d.GetPurposes(), d.GetAccelerator())
	if err != nil {
		return nil, err
	}
	return &imagev1.RegisterPlatformImageRequest{ImageReference: reference, DisplayName: d.GetDisplayName(), Description: d.GetDescription(), Purposes: purposes, Accelerator: accelerator, IdempotencyKey: d.GetIdempotencyKey()}, nil
}

// imagePatchPaths 是 PATCH 允许的四个可改字段（规范蛇形）。HTTP 层的
// update_mask 必须写成 camelCase：protojson 的 FieldMask 解码器会把 JSON 名
// 归一化成蛇形，而纯蛇形输入会被它直接判为非法路径。因此到这里拿到的
// Paths 已经是规范蛇形，本表按蛇形建键。
var imagePatchPaths = map[string]bool{"display_name": true, "description": true, "purposes": true, "accelerator": true}

// imagePatchMask 归一化掩码，并拒绝空、*、未知、重复与 data. 前缀。
func imagePatchMask(mask *fieldmaskpb.FieldMask) (*fieldmaskpb.FieldMask, error) {
	if mask == nil || len(mask.GetPaths()) == 0 || len(mask.GetPaths()) > len(imagePatchPaths) {
		return nil, imageBadRequest()
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(mask.GetPaths()))
	for _, path := range mask.GetPaths() {
		if !imagePatchPaths[path] || seen[path] {
			return nil, imageBadRequest()
		}
		seen[path] = true
		out = append(out, path)
	}
	return &fieldmaskpb.FieldMask{Paths: out}, nil
}

// imagePatchData 组装掩码 PATCH 请求。掩码内字段必须出现，掩码外字段必须缺省；
// expectedVersion 与 idempotencyKey 是控制字段，永远不进掩码。
func imagePatchData(id string, d *view.ImageManagementPatchData, mask *fieldmaskpb.FieldMask) (*imagev1.UpdatePlatformImageRequest, error) {
	if !imageIDPattern.MatchString(id) || d == nil || !imageIdempotencyKey(d.GetIdempotencyKey()) || d.ExpectedVersion == nil || d.GetExpectedVersion() < 1 {
		return nil, imageBadRequest()
	}
	canonical, err := imagePatchMask(mask)
	if err != nil {
		return nil, err
	}
	inMask := map[string]bool{}
	for _, p := range canonical.GetPaths() {
		inMask[p] = true
	}
	expected := d.GetExpectedVersion()
	req := &imagev1.UpdatePlatformImageRequest{ImageId: id, ExpectedVersion: &expected, IdempotencyKey: d.GetIdempotencyKey(), UpdateMask: canonical}
	if inMask["display_name"] {
		name := strings.TrimSpace(d.GetDisplayName())
		if name == "" || utf8.RuneCountInString(name) > 100 {
			return nil, imageBadRequest()
		}
		req.DisplayName = &name
	} else if d.DisplayName != nil {
		return nil, imageBadRequest()
	}
	if inMask["description"] {
		if d.Description == nil || utf8.RuneCountInString(d.GetDescription()) > 2000 {
			return nil, imageBadRequest()
		}
		description := d.GetDescription()
		req.Description = &description
	} else if d.Description != nil {
		return nil, imageBadRequest()
	}
	if inMask["purposes"] {
		if len(d.GetPurposes()) == 0 || len(d.GetPurposes()) > 5 {
			return nil, imageBadRequest()
		}
		purposes, _, err := imageManagementDeclarations(d.GetPurposes(), "undeclared")
		if err != nil {
			return nil, err
		}
		req.Purposes = purposes
	} else if len(d.GetPurposes()) > 0 {
		return nil, imageBadRequest()
	}
	if inMask["accelerator"] {
		if d.Accelerator == nil {
			return nil, imageBadRequest()
		}
		_, accelerator, err := imageDeclarations(nil, d.GetAccelerator())
		if err != nil {
			return nil, err
		}
		req.Accelerator = &accelerator
	} else if d.Accelerator != nil {
		return nil, imageBadRequest()
	}
	return req, nil
}

func imageUnregisterData(d *view.ImageManagementUnregisterData) (int64, string, error) {
	if d == nil || !imageIdempotencyKey(d.GetIdempotencyKey()) || d.ExpectedVersion == nil || d.GetExpectedVersion() < 1 {
		return 0, "", imageBadRequest()
	}
	return d.GetExpectedVersion(), d.GetIdempotencyKey(), nil
}
