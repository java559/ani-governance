package service

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// VMImageOwnerClient is the trusted Resource surface the image lane needs. The
// adapter is nil when Resource wiring is absent and every route then fails closed.
type VMImageOwnerClient interface {
	List(context.Context, string, string, uint32, uint32, string, string, string) (*vm.ListImagesResponse, error)
	Get(context.Context, string, string, string) (*vm.VMImage, error)
	Create(context.Context, string, string, *vm.CreateImageRequest) (*vm.VMImage, error)
	Session(context.Context, string, string, *vm.CreateUploadSessionRequest) (*vm.UploadSession, error)
	Update(context.Context, string, string, *vm.UpdateImageRequest) (*vm.VMImage, error)
	Delete(context.Context, string, string, *vm.DeleteImageRequest) (*vm.VMImage, error)
}

type VMImageService struct {
	admin.UnimplementedVMImageServiceServer
	client        VMImageOwnerClient
	authorization InstanceActionAuthorizer
	resolver      ResourceTenantResolver
}

func NewVMImageService(c VMImageOwnerClient, a InstanceActionAuthorizer, r ResourceTenantResolver) *VMImageService {
	return &VMImageService{client: c, authorization: a, resolver: r}
}

func vmImageInvalid() error {
	return kratoserrors.BadRequest("VM_IMAGE_INVALID_ARGUMENT", "invalid VM image request")
}

// scope resolves the authenticated tenant, rechecks the compiled permission code
// against the exact route, and returns only the identity the owner may trust.
// Nothing here reads a tenant, owner or namespace from the request body.
func (s *VMImageService) scope(ctx context.Context, action string) (string, string, error) {
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return "", "", err
	}
	route, ok := data.VMActionRoute(action)
	if !ok || s.authorization == nil {
		return "", "", kratoserrors.Forbidden("VM_SCOPE_DENIED", "VM image action denied")
	}
	if err = vmAuthorizationError(s.authorization.Authorize(ctx, p.TenantID, p.ID, route.Path, route.Method)); err != nil {
		return "", "", err
	}
	if s.client == nil || s.resolver == nil {
		return "", "", kratoserrors.ServiceUnavailable("VM_ADAPTER_UNAVAILABLE", "VM image owner unavailable")
	}
	tenant, err := s.resolver.ResourceTenantID(ctx, p.TenantID)
	return tenant, "governance:user:" + strconv.FormatUint(uint64(p.ID), 10), err
}

var vmImageStates = map[string]bool{
	"PREPARING": true, "IMPORTING": true, "VERIFYING": true, "READY": true,
	"FAILED": true, "UNAVAILABLE": true, "DELETING": true, "DELETED": true,
}

var vmImageKinds = map[string]string{"DISK_IMAGE": "qcow2", "INSTALL_ISO": "iso"}

// resolveImagePage mirrors the shared page contract: defaults 1/20, pageSize up to
// 100, and no silent repair of an out-of-contract value.
func resolveImagePage(page, pageSize *uint32) (uint32, uint32, error) {
	p, size := uint32(1), uint32(20)
	if page != nil {
		p = *page
	}
	if pageSize != nil {
		size = *pageSize
	}
	if p == 0 || size == 0 || size > 100 {
		return 0, 0, vmImageInvalid()
	}
	return p, size, nil
}

func imageListFilters(kind, state, keyword *string) (string, string, string, error) {
	k, s, q := "", "", ""
	if kind != nil {
		k = strings.TrimSpace(*kind)
	}
	if _, ok := vmImageKinds[k]; k != "" && !ok {
		return "", "", "", vmImageInvalid()
	}
	if state != nil {
		s = strings.TrimSpace(*state)
	}
	if s == "all" {
		s = ""
	}
	if s != "" && !vmImageStates[s] {
		return "", "", "", vmImageInvalid()
	}
	if keyword != nil {
		q = strings.TrimSpace(*keyword)
	}
	if utf8.RuneCountInString(q) > 80 {
		return "", "", "", vmImageInvalid()
	}
	return k, s, q, nil
}

func (s *VMImageService) ListVMImages(ctx context.Context, r *view.ListVMImagesRequest) (*view.ListVMImagesResponse, error) {
	if r == nil {
		return nil, vmImageInvalid()
	}
	tenant, actor, err := s.scope(ctx, "image-list")
	if err != nil {
		return nil, err
	}
	page, size, err := resolveImagePage(r.Page, r.PageSize)
	if err != nil {
		return nil, err
	}
	kind, state, keyword, err := imageListFilters(r.Kind, r.State, r.Keyword)
	if err != nil {
		return nil, err
	}
	result, err := s.client.List(ctx, tenant, actor, page, size, kind, state, keyword)
	if err != nil {
		return nil, err
	}
	out := new(view.ListVMImagesResponse)
	if err = vmWire(result, out); err != nil {
		return nil, err
	}
	if out.Items == nil {
		out.Items = []*view.VMImage{}
	}
	return out, nil
}

func (s *VMImageService) GetVMImage(ctx context.Context, r *view.GetVMImageRequest) (*view.VMImage, error) {
	if r == nil || !canonicalCatalogUUID(r.ImageId) {
		return nil, vmImageInvalid()
	}
	tenant, actor, err := s.scope(ctx, "image-get")
	if err != nil {
		return nil, err
	}
	result, err := s.client.Get(ctx, tenant, actor, r.ImageId)
	if err != nil {
		return nil, err
	}
	out := new(view.VMImage)
	if err = vmWire(result, out); err != nil {
		return nil, err
	}
	return out, nil
}

func imageOSInput(in *view.VMImageOS) (*vm.ImageOS, error) {
	if in == nil {
		return nil, vmImageInvalid()
	}
	family := strings.ToLower(strings.TrimSpace(in.OsFamily))
	if family != "linux" && family != "windows" {
		return nil, vmImageInvalid()
	}
	version := strings.TrimSpace(in.OsVersion)
	if version == "" || utf8.RuneCountInString(version) > 40 {
		return nil, vmImageInvalid()
	}
	architecture := strings.TrimSpace(in.Architecture)
	if architecture == "" {
		architecture = "amd64"
	}
	return &vm.ImageOS{OsFamily: family, OsVersion: version, Architecture: architecture}, nil
}

func (s *VMImageService) CreateVMImage(ctx context.Context, r *view.CreateVMImageRequest) (*view.VMImage, error) {
	if r == nil || r.Data == nil {
		return nil, vmImageInvalid()
	}
	tenant, actor, err := s.scope(ctx, "image-create")
	if err != nil {
		return nil, err
	}
	d := r.Data
	format, ok := vmImageKinds[d.Kind]
	if !ok || d.Format != format {
		return nil, vmImageInvalid()
	}
	name := strings.TrimSpace(d.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 || utf8.RuneCountInString(d.Description) > 512 {
		return nil, vmImageInvalid()
	}
	if d.IdempotencyKey == "" || len(d.IdempotencyKey) > 256 || d.SourceCapacityGb < 1 {
		return nil, vmImageInvalid()
	}
	profile, err := imageOSInput(d.Os)
	if err != nil {
		return nil, err
	}
	result, err := s.client.Create(ctx, tenant, actor, &vm.CreateImageRequest{TenantId: tenant,
		IdempotencyKey: d.IdempotencyKey, Name: name, Description: d.Description, Kind: d.Kind, Format: d.Format,
		SourceCapacityGb: d.SourceCapacityGb, Os: profile})
	if err != nil {
		return nil, err
	}
	out := new(view.VMImage)
	if err = vmWire(result, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *VMImageService) CreateVMImageUploadSession(ctx context.Context, r *view.CreateVMImageUploadSessionRequest) (*view.VMImageUploadSession, error) {
	if r == nil || !canonicalCatalogUUID(r.ImageId) || r.Data == nil || r.Data.IdempotencyKey == "" || len(r.Data.IdempotencyKey) > 256 {
		return nil, vmImageInvalid()
	}
	tenant, actor, err := s.scope(ctx, "image-session")
	if err != nil {
		return nil, err
	}
	result, err := s.client.Session(ctx, tenant, actor, &vm.CreateUploadSessionRequest{TenantId: tenant,
		ImageId: r.ImageId, IdempotencyKey: r.Data.IdempotencyKey})
	if err != nil {
		return nil, err
	}
	out := new(view.VMImageUploadSession)
	if err = vmWire(result, out); err != nil {
		return nil, err
	}
	return out, nil
}

// imageUpdateMask accepts only the two mutable paths and rejects an empty,
// wildcard, unknown or repeated list before anything reaches the owner.
func imageUpdateMask(mask *fieldmaskpb.FieldMask) ([]string, error) {
	if mask == nil || len(mask.Paths) == 0 {
		return nil, vmImageInvalid()
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(mask.Paths))
	for _, path := range mask.Paths {
		path = strings.TrimSpace(path)
		if path == "" || path == "*" || path != "name" && path != "description" || seen[path] {
			return nil, vmImageInvalid()
		}
		seen[path] = true
		out = append(out, path)
	}
	return out, nil
}

func (s *VMImageService) UpdateVMImage(ctx context.Context, r *view.UpdateVMImageRequest) (*view.VMImage, error) {
	if r == nil || r.Data == nil || !canonicalCatalogUUID(r.ImageId) || r.Data.IdempotencyKey == "" ||
		len(r.Data.IdempotencyKey) > 256 || r.Data.ExpectedVersion < 1 {
		return nil, vmImageInvalid()
	}
	tenant, actor, err := s.scope(ctx, "image-update")
	if err != nil {
		return nil, err
	}
	paths, err := imageUpdateMask(r.UpdateMask)
	if err != nil {
		return nil, err
	}
	name, description := r.Data.Name, r.Data.Description
	for _, path := range paths {
		if path == "name" {
			name = strings.TrimSpace(name)
			if name == "" || utf8.RuneCountInString(name) > 80 {
				return nil, vmImageInvalid()
			}
		} else if utf8.RuneCountInString(description) > 512 {
			return nil, vmImageInvalid()
		}
	}
	result, err := s.client.Update(ctx, tenant, actor, &vm.UpdateImageRequest{TenantId: tenant, ImageId: r.ImageId,
		IdempotencyKey: r.Data.IdempotencyKey, ExpectedVersion: r.Data.ExpectedVersion, Name: name, Description: description,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: paths}})
	if err != nil {
		return nil, err
	}
	out := new(view.VMImage)
	if err = vmWire(result, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *VMImageService) DeleteVMImage(ctx context.Context, r *view.DeleteVMImageRequest) (*view.VMImage, error) {
	if r == nil || r.Data == nil || !canonicalCatalogUUID(r.ImageId) || r.Data.IdempotencyKey == "" ||
		len(r.Data.IdempotencyKey) > 256 || r.Data.ExpectedVersion < 1 {
		return nil, vmImageInvalid()
	}
	tenant, actor, err := s.scope(ctx, "image-delete")
	if err != nil {
		return nil, err
	}
	result, err := s.client.Delete(ctx, tenant, actor, &vm.DeleteImageRequest{TenantId: tenant, ImageId: r.ImageId,
		IdempotencyKey: r.Data.IdempotencyKey, ExpectedVersion: r.Data.ExpectedVersion})
	if err != nil {
		return nil, err
	}
	out := new(view.VMImage)
	if err = vmWire(result, out); err != nil {
		return nil, err
	}
	return out, nil
}
