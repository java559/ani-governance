package service

import (
	"regexp"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var imagePurposeNames = map[imagev1.ImagePurpose]string{1: "container", 2: "development", 3: "inference", 4: "finetuning", 5: "training"}
var imageAcceleratorNames = map[imagev1.AcceleratorKind]string{0: "undeclared", 1: "none", 2: "nvidia", 3: "amd", 4: "ascend", 5: "other"}
var imageIDPattern = regexp.MustCompile(`^img_[a-f0-9]{32}$`)
var imageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var imageRepositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
var imagePlatformPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// 平台项目名不是 t- 前缀的租户项目，规则与 Res 侧的 PlatformProject 校验一致。
var imagePlatformProjectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,46}[a-z0-9]$`)

func imageInvalidReply() error {
	return errors.ServiceUnavailable("IMAGE_INVALID_RESPONSE", "invalid Image response")
}
func imageBadRequest() error { return errors.BadRequest("INVALID_ARGUMENT", "invalid Image request") }
func imageUUID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}
func imageTime(t *timestamppb.Timestamp) bool { return t != nil && t.CheckValid() == nil }
func imageScope(s string) (imagev1.ImageScope, error) {
	switch s {
	case "tenant":
		return imagev1.ImageScope_IMAGE_SCOPE_TENANT, nil
	case "platform":
		return imagev1.ImageScope_IMAGE_SCOPE_PLATFORM, nil
	}
	return 0, imageBadRequest()
}
func imageDeclarations(p []string, a string) ([]imagev1.ImagePurpose, imagev1.AcceleratorKind, error) {
	values := make([]imagev1.ImagePurpose, 0, len(p))
	for _, name := range p {
		found := false
		for v, n := range imagePurposeNames {
			if n == name {
				values = append(values, v)
				found = true
				break
			}
		}
		if !found {
			return nil, 0, imageBadRequest()
		}
	}
	if a == "" {
		a = "undeclared"
	}
	for v, name := range imageAcceleratorNames {
		if name == a {
			return values, v, nil
		}
	}
	return nil, 0, imageBadRequest()
}
func validateImageSpace(v *imagev1.ImageSpace, tenant string) error {
	if v == nil || v.TenantId != tenant || !imageUUID(v.SpaceId) || !strings.HasPrefix(v.ProjectName, "t-") || len(v.ProjectName) > 42 || !regexp.MustCompile(`^t-[a-z][a-z0-9-]+[a-z0-9]$`).MatchString(v.ProjectName) || v.RegistryAuthority == "" || strings.ContainsAny(v.RegistryAuthority, "/@?#%\\ \r\n") || v.Version < 1 || !imageTime(v.CreatedAt) || !imageTime(v.UpdatedAt) {
		return imageInvalidReply()
	}
	switch v.State {
	case "provisioning", "blocked":
	case "available":
		if v.PullCredentialGeneration < 1 {
			return imageInvalidReply()
		}
	default:
		return imageInvalidReply()
	}
	return nil
}
func validateImageCredential(v *imagev1.PublisherCredential, tenant string) error {
	if v == nil || v.TenantId != tenant || !imageUUID(v.SpaceId) || !imageTime(v.UpdatedAt) || (v.ExpiresAt != nil && !imageTime(v.ExpiresAt)) {
		return imageInvalidReply()
	}
	if v.State == "not_issued" {
		if v.Generation != 0 || v.Version != 0 || v.Username != "" || v.ExpiresAt != nil {
			return imageInvalidReply()
		}
		return nil
	}
	// An interrupted first issuance has a durable candidate row but no active
	// generation yet. Its safe metadata remains readable while the command resumes.
	if v.State == "issuing" && v.Generation == 0 && v.Version > 0 && v.Username == "" && v.ExpiresAt == nil {
		return nil
	}
	if v.Generation < 1 || v.Version < 1 || v.Username == "" || len(v.Username) > 256 || strings.ContainsAny(v.Username, "\r\n\x00") {
		return imageInvalidReply()
	}
	switch v.State {
	case "issuing", "active", "disabled", "blocked":
		return nil
	}
	return imageInvalidReply()
}
func validateImageDelivery(v *imagev1.PublisherCredential, tenant, secret string, until *timestamppb.Timestamp) error {
	if err := validateImageCredential(v, tenant); err != nil {
		return err
	}
	if v.State != "active" || len(secret) == 0 || len(secret) > 4096 || !imageTime(until) || until.AsTime().Before(time.Now().Add(-30*time.Second)) || until.AsTime().After(time.Now().Add(11*time.Minute)) {
		return imageInvalidReply()
	}
	return nil
}
func validateImageReply(v *imagev1.ImageRegistration, tenant string, scope imagev1.ImageScope, id string) error {
	if v == nil || v.Scope != scope || !imageIDPattern.MatchString(v.ImageId) || (id != "" && v.ImageId != id) || !imageUUID(v.SpaceId) || v.Version < 1 || !imageTime(v.CreatedAt) || !imageTime(v.UpdatedAt) || (v.UnregisteredAt != nil && !imageTime(v.UnregisteredAt)) {
		return imageInvalidReply()
	}
	if (scope == imagev1.ImageScope_IMAGE_SCOPE_TENANT && v.TenantId != tenant) || (scope == imagev1.ImageScope_IMAGE_SCOPE_PLATFORM && v.TenantId != "") || (scope != imagev1.ImageScope_IMAGE_SCOPE_TENANT && scope != imagev1.ImageScope_IMAGE_SCOPE_PLATFORM) {
		return imageInvalidReply()
	}
	if !imageDigestPattern.MatchString(v.Digest) || len(v.Repository) > 128 || !imageRepositoryPattern.MatchString(v.Repository) || !strings.HasSuffix(v.ResolvedReference, "/"+v.Repository+"@"+v.Digest) || strings.ContainsAny(v.ResolvedReference, "?#%\\ \r\n") || strings.Contains(v.ResolvedReference, "://") {
		return imageInvalidReply()
	}
	path := strings.Split(v.ResolvedReference, "/")
	if len(path) != 3 || path[0] == "" || strings.Contains(path[0], "@") || !regexp.MustCompile(`^[a-z][a-z0-9-]{1,46}[a-z0-9]$`).MatchString(path[1]) {
		return imageInvalidReply()
	}
	if v.DisplayName == "" || len(v.Purposes) < 1 || len(v.Purposes) > 5 || len(v.Platforms) < 1 || len(v.Platforms) > 32 {
		return imageInvalidReply()
	}
	seen := map[imagev1.ImagePurpose]bool{}
	for _, p := range v.Purposes {
		if imagePurposeNames[p] == "" || seen[p] {
			return imageInvalidReply()
		}
		seen[p] = true
	}
	if _, ok := imageAcceleratorNames[v.Accelerator]; !ok {
		return imageInvalidReply()
	}
	for _, p := range v.Platforms {
		if p == nil || !imagePlatformPattern.MatchString(p.Os) || !imagePlatformPattern.MatchString(p.Architecture) || p.Os == "unknown" || p.Architecture == "unknown" || (p.Variant != "" && !imagePlatformPattern.MatchString(p.Variant)) {
			return imageInvalidReply()
		}
	}
	return nil
}
func wireImageSpace(v *imagev1.ImageSpace) *view.ImageSpace {
	return &view.ImageSpace{SpaceId: v.SpaceId, ProjectName: v.ProjectName, RegistryAuthority: v.RegistryAuthority, State: v.State, Reason: v.Reason, Version: v.Version, PullCredentialGeneration: v.PullCredentialGeneration, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
func wireImageCredential(v *imagev1.PublisherCredential) *view.PublisherCredential {
	return &view.PublisherCredential{SpaceId: v.SpaceId, Generation: v.Generation, Username: v.Username, State: v.State, Version: v.Version, ExpiresAt: v.ExpiresAt, UpdatedAt: v.UpdatedAt}
}
func wireImageRegistration(v *imagev1.ImageRegistration) *view.ImageRegistration {
	scope := "tenant"
	if v.Scope == imagev1.ImageScope_IMAGE_SCOPE_PLATFORM {
		scope = "platform"
	}
	out := &view.ImageRegistration{ImageId: v.ImageId, Scope: scope, SpaceId: v.SpaceId, DisplayName: v.DisplayName, Description: v.Description, Repository: v.Repository, SourceReference: v.SourceReference, Digest: v.Digest, ResolvedReference: v.ResolvedReference, Accelerator: imageAcceleratorNames[v.Accelerator], Version: v.Version, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, UnregisteredAt: v.UnregisteredAt}
	for _, p := range v.Platforms {
		out.Platforms = append(out.Platforms, &view.ImagePlatform{Os: p.Os, Architecture: p.Architecture, Variant: p.Variant})
	}
	for _, p := range v.Purposes {
		out.Purposes = append(out.Purposes, imagePurposeNames[p])
	}
	return out
}
func mapImageError(err error) error {
	if err == nil {
		return nil
	}
	s := status.Convert(err)
	httpCode := 500
	switch s.Code() {
	case codes.InvalidArgument:
		httpCode = 400
	case codes.Unauthenticated:
		httpCode = 401
	case codes.PermissionDenied:
		httpCode = 403
	case codes.NotFound:
		httpCode = 404
	case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
		httpCode = 409
	case codes.Unavailable:
		httpCode = 503
	case codes.DeadlineExceeded, codes.Canceled:
		httpCode = 504
	}
	reason := "IMAGE_UNAVAILABLE"
	for _, detail := range s.Details() {
		if d, ok := detail.(*errdetails.ErrorInfo); ok && d.Domain == "image.ani.io" && imageReasons[d.Reason] {
			reason = d.Reason
			break
		}
	}
	return errors.New(httpCode, reason, "Image request failed")
}

var imageReasons = map[string]bool{"INVALID_ARGUMENT": true, "INVALID_REFERENCE": true, "INVALID_CURSOR": true, "TRUSTED_CALLER_REQUIRED": true, "PERMISSION_DENIED": true, "IMAGE_PROJECT_DENIED": true, "IMAGE_NOT_FOUND": true, "SPACE_NOT_FOUND": true, "IMAGE_ALREADY_REGISTERED": true, "SPACE_NAME_CONFLICT": true, "SPACE_NAME_IMMUTABLE": true, "IDEMPOTENCY_CONFLICT": true, "VERSION_CONFLICT": true, "SPACE_NOT_READY": true, "CREDENTIAL_ALREADY_ACTIVE": true, "CREDENTIAL_NOT_ISSUED": true, "CREDENTIAL_DELIVERY_EXPIRED": true, "UNSUPPORTED_ARTIFACT": true, "PLATFORM_MISMATCH": true, "SPACE_OWNERSHIP_UNCONFIRMED": true, "DEPENDENCY_UNAVAILABLE": true, "REQUEST_IN_PROGRESS": true, "DEADLINE_EXCEEDED": true, "INTERNAL_ERROR": true, "IMAGE_SPACE_BINDING_MISMATCH": true, "CANDIDATE_RECOVERY_COMPLETED": true}
