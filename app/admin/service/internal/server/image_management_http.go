package server

import (
	"context"
	"io"
	"mime"
	nethttp "net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// 平台镜像管理路由的手写注册。与 VM 镜像通道相同的理由：22 条路径是固定集合，
// 每条路径恰好携带一个权限码和一个 gRPC 操作，手写表保证 path/method/permission
// 三者唯一，避免生成式绑定与全局过滤器双重维护。

// imageManagementIdentityHeaders 是调用方不得携带的内部身份头；出现即 400。
var imageManagementIdentityHeaders = []string{"X-Ani-Tenant-Id", "X-Tenant-Id", "X-Ani-Actor", "X-Ani-Operator", "X-Ani-Request-Id", "X-Resource-Tenant-Id"}

var imageManagementImageIDPattern = regexp.MustCompile(`^img_[a-f0-9]{32}$`)

const (
	imageManagementBodyLimit       = 16384
	imageManagementMaxPage         = uint64(4294967295)
	imageManagementMaxPageSize     = uint64(100)
	imageManagementMaxTargetTenant = uint64(4294967295)
)

func imageManagementBadRequest() error {
	return kratoserrors.BadRequest("INVALID_ARGUMENT", "invalid image management request")
}

// imageManagementUnsigned 解析无符号十进制数：拒绝空值、前导零、负号、小数点、
// 非数字、0 与超过 ceiling 的值。所有路径参数与分页参数共用。
func imageManagementUnsigned(raw string, ceiling uint64) (uint32, bool) {
	if raw == "" || len(raw) > 10 {
		return 0, false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	if raw[0] == '0' {
		return 0, false
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n < 1 || n > ceiling {
		return 0, false
	}
	return uint32(n), true
}

// imageManagementPathParams 在绑定前校验路径参数的原始字符串，坏值一律 400，
// 不把 "00" 或 "1.0" 这类输入交给后续解码。
func imageManagementPathParams(ctx http.Context) error {
	vars := ctx.Vars()
	if raw, ok := vars["target_tenant_id"]; ok {
		if len(raw) != 1 {
			return imageManagementBadRequest()
		}
		if _, valid := imageManagementUnsigned(raw[0], imageManagementMaxTargetTenant); !valid {
			return imageManagementBadRequest()
		}
	}
	if raw, ok := vars["image_id"]; ok {
		if len(raw) != 1 || !imageManagementImageIDPattern.MatchString(raw[0]) {
			return imageManagementBadRequest()
		}
	}
	return nil
}

// decodeImageManagementBody 解码写请求：16KiB 上限、必须 application/json、
// protojson 拒绝未知字段（camelCase 与 snake_case 双写同一字段也会被拒绝）、
// 写路由不携带 query。
func decodeImageManagementBody(request *nethttp.Request, message proto.Message) error {
	if request == nil || request.Body == nil || request.URL == nil || request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.RawPath != "" && request.URL.RawPath != request.URL.Path {
		return imageManagementBadRequest()
	}
	media, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return imageManagementBadRequest()
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, imageManagementBodyLimit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > imageManagementBodyLimit {
		return imageManagementBadRequest()
	}
	if err = (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, message); err != nil {
		return imageManagementBadRequest()
	}
	return nil
}

// decodeImageManagementListQuery 严格解析列表 GET 的 query：search 与 accelerator
// 单值，purposes 允许重复出现但每个值非空，page/pageSize 走无符号校验；
// 未知参数、重复的单值参数与空值一律拒绝。
func decodeImageManagementListQuery(request *nethttp.Request, message proto.Message) error {
	if request == nil || request.URL == nil || request.URL.ForceQuery || request.URL.RawPath != "" && request.URL.RawPath != request.URL.Path {
		return imageManagementBadRequest()
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return imageManagementBadRequest()
	}
	var search, accelerator string
	var purposes []string
	var page, pageSize *uint32
	for field, list := range values {
		switch field {
		case "search", "accelerator":
			if len(list) != 1 || list[0] == "" || !utf8.ValidString(list[0]) {
				return imageManagementBadRequest()
			}
			if field == "search" {
				search = list[0]
			} else {
				accelerator = list[0]
			}
		case "purposes":
			if len(list) > 5 {
				return imageManagementBadRequest()
			}
			for _, v := range list {
				if v == "" || !utf8.ValidString(v) {
					return imageManagementBadRequest()
				}
			}
			purposes = list
		case "page", "pageSize":
			ceiling := imageManagementMaxPageSize
			if field == "page" {
				ceiling = imageManagementMaxPage
			}
			n, valid := imageManagementUnsigned(list[0], ceiling)
			if len(list) != 1 || !valid {
				return imageManagementBadRequest()
			}
			if field == "page" {
				page = &n
			} else {
				pageSize = &n
			}
		default:
			return imageManagementBadRequest()
		}
	}
	switch target := message.(type) {
	case *admin.ListPlatformImagesRequest:
		target.Search, target.Purposes, target.Accelerator, target.Page, target.PageSize = search, purposes, accelerator, page, pageSize
	case *admin.ListTenantImagesRequest:
		target.Search, target.Purposes, target.Accelerator, target.Page, target.PageSize = search, purposes, accelerator, page, pageSize
	default:
		return imageManagementBadRequest()
	}
	return nil
}

// RegisterImageManagementHTTPServer 挂载平台镜像管理与代管的 22 条路由。
func RegisterImageManagementHTTPServer(server *http.Server, p *service.PlatformImageService, t *service.AdminTenantImageService) {
	if server == nil || p == nil || t == nil {
		return
	}
	type entry struct {
		action, operation, service string
		request                    func() proto.Message
	}
	routes := []entry{
		{"platform-space-get", "GetPlatformSpace", "PlatformImageService", func() proto.Message { return new(admin.GetPlatformSpaceRequest) }},
		{"platform-space-enable", "InitializePlatform", "PlatformImageService", func() proto.Message { return new(admin.InitializePlatformRequest) }},
		{"platform-credential-get", "GetPlatformPublisherCredential", "PlatformImageService", func() proto.Message { return new(admin.GetPlatformPublisherCredentialRequest) }},
		{"platform-credential-issue", "IssuePlatformPublisherCredential", "PlatformImageService", func() proto.Message { return new(admin.IssuePlatformPublisherCredentialRequest) }},
		{"platform-credential-reset", "ResetPlatformPublisherCredential", "PlatformImageService", func() proto.Message { return new(admin.ResetPlatformPublisherCredentialRequest) }},
		{"platform-credential-disable", "DisablePlatformPublisherCredential", "PlatformImageService", func() proto.Message { return new(admin.DisablePlatformPublisherCredentialRequest) }},
		{"platform-registration-list", "ListPlatformImages", "PlatformImageService", func() proto.Message { return new(admin.ListPlatformImagesRequest) }},
		{"platform-registration-get", "GetPlatformImage", "PlatformImageService", func() proto.Message { return new(admin.GetPlatformImageRequest) }},
		{"platform-registration-create", "RegisterPlatformImage", "PlatformImageService", func() proto.Message { return new(admin.RegisterPlatformImageRequest) }},
		{"platform-registration-update", "UpdatePlatformImage", "PlatformImageService", func() proto.Message { return new(admin.UpdatePlatformImageRequest) }},
		{"platform-registration-unregister", "UnregisterPlatformImage", "PlatformImageService", func() proto.Message { return new(admin.UnregisterPlatformImageRequest) }},

		{"tenant-space-get", "GetTenantImageSpace", "AdminTenantImageService", func() proto.Message { return new(admin.GetTenantImageSpaceRequest) }},
		{"tenant-space-enable", "EnsureTenantImageSpace", "AdminTenantImageService", func() proto.Message { return new(admin.EnsureTenantImageSpaceRequest) }},
		{"tenant-credential-get", "GetTenantPublisherCredential", "AdminTenantImageService", func() proto.Message { return new(admin.GetTenantPublisherCredentialRequest) }},
		{"tenant-credential-issue", "IssueTenantPublisherCredential", "AdminTenantImageService", func() proto.Message { return new(admin.IssueTenantPublisherCredentialRequest) }},
		{"tenant-credential-reset", "ResetTenantPublisherCredential", "AdminTenantImageService", func() proto.Message { return new(admin.ResetTenantPublisherCredentialRequest) }},
		{"tenant-credential-disable", "DisableTenantPublisherCredential", "AdminTenantImageService", func() proto.Message { return new(admin.DisableTenantPublisherCredentialRequest) }},
		{"tenant-registration-list", "ListTenantImages", "AdminTenantImageService", func() proto.Message { return new(admin.ListTenantImagesRequest) }},
		{"tenant-registration-get", "GetTenantImage", "AdminTenantImageService", func() proto.Message { return new(admin.GetTenantImageRequest) }},
		{"tenant-registration-create", "RegisterTenantImage", "AdminTenantImageService", func() proto.Message { return new(admin.RegisterTenantImageRequest) }},
		{"tenant-registration-update", "UpdateTenantImage", "AdminTenantImageService", func() proto.Message { return new(admin.UpdateTenantImageRequest) }},
		{"tenant-registration-unregister", "UnregisterTenantImage", "AdminTenantImageService", func() proto.Message { return new(admin.UnregisterTenantImageRequest) }},
	}
	for _, e := range routes {
		e := e
		registered, ok := data.ImageManagementActionRoute(e.action)
		if !ok || registered.Operation != e.operation || registered.Service != e.service {
			panic("image management route table mismatch: " + e.action)
		}
		handler := func(ctx http.Context) error {
			// 凭据与状态都不可缓存；响应头在任何错误路径之前设置。
			ctx.Response().Header().Set("Cache-Control", "no-store")
			ctx.Response().Header().Set("Pragma", "no-cache")
			request := e.request()
			for _, header := range imageManagementIdentityHeaders {
				if len(ctx.Request().Header.Values(header)) != 0 {
					return imageManagementBadRequest()
				}
			}
			if err := imageManagementPathParams(ctx); err != nil {
				return err
			}
			switch registered.Method {
			case "GET":
				if e.action == "platform-registration-list" || e.action == "tenant-registration-list" {
					if err := decodeImageManagementListQuery(ctx.Request(), request); err != nil {
						return err
					}
				} else if ctx.Request().URL.RawQuery != "" || ctx.Request().URL.ForceQuery {
					return imageManagementBadRequest()
				}
			default:
				if err := decodeImageManagementBody(ctx.Request(), request); err != nil {
					return err
				}
			}
			if strings.Contains(registered.Path, "{") {
				if err := ctx.BindVars(request); err != nil {
					return imageManagementBadRequest()
				}
			}
			http.SetOperation(ctx, "/admin.service.v1."+registered.Service+"/"+registered.Operation)
			caller := func(call context.Context, in any) (any, error) {
				if registered.Service == "PlatformImageService" {
					return invokePlatformImageHTTP(call, p, e.action, in.(proto.Message))
				}
				return invokeAdminTenantImageHTTP(call, t, e.action, in.(proto.Message))
			}
			out, err := ctx.Middleware(caller)(ctx, request)
			if err != nil {
				return err
			}
			return ctx.Result(200, out)
		}
		router := server.Route("/")
		switch registered.Method {
		case "GET":
			router.GET(registered.Path, handler)
		case "POST":
			router.POST(registered.Path, handler)
		case "PATCH":
			router.PATCH(registered.Path, handler)
		default:
			panic("image management method unsupported")
		}
	}
}

func invokePlatformImageHTTP(ctx context.Context, s *service.PlatformImageService, action string, in proto.Message) (proto.Message, error) {
	switch action {
	case "platform-space-get":
		return s.GetPlatformSpace(ctx, in.(*admin.GetPlatformSpaceRequest))
	case "platform-space-enable":
		return s.InitializePlatform(ctx, in.(*admin.InitializePlatformRequest))
	case "platform-credential-get":
		return s.GetPlatformPublisherCredential(ctx, in.(*admin.GetPlatformPublisherCredentialRequest))
	case "platform-credential-issue":
		return s.IssuePlatformPublisherCredential(ctx, in.(*admin.IssuePlatformPublisherCredentialRequest))
	case "platform-credential-reset":
		return s.ResetPlatformPublisherCredential(ctx, in.(*admin.ResetPlatformPublisherCredentialRequest))
	case "platform-credential-disable":
		return s.DisablePlatformPublisherCredential(ctx, in.(*admin.DisablePlatformPublisherCredentialRequest))
	case "platform-registration-list":
		return s.ListPlatformImages(ctx, in.(*admin.ListPlatformImagesRequest))
	case "platform-registration-get":
		return s.GetPlatformImage(ctx, in.(*admin.GetPlatformImageRequest))
	case "platform-registration-create":
		return s.RegisterPlatformImage(ctx, in.(*admin.RegisterPlatformImageRequest))
	case "platform-registration-update":
		return s.UpdatePlatformImage(ctx, in.(*admin.UpdatePlatformImageRequest))
	case "platform-registration-unregister":
		return s.UnregisterPlatformImage(ctx, in.(*admin.UnregisterPlatformImageRequest))
	}
	return nil, imageManagementBadRequest()
}

func invokeAdminTenantImageHTTP(ctx context.Context, s *service.AdminTenantImageService, action string, in proto.Message) (proto.Message, error) {
	switch action {
	case "tenant-space-get":
		return s.GetTenantImageSpace(ctx, in.(*admin.GetTenantImageSpaceRequest))
	case "tenant-space-enable":
		return s.EnsureTenantImageSpace(ctx, in.(*admin.EnsureTenantImageSpaceRequest))
	case "tenant-credential-get":
		return s.GetTenantPublisherCredential(ctx, in.(*admin.GetTenantPublisherCredentialRequest))
	case "tenant-credential-issue":
		return s.IssueTenantPublisherCredential(ctx, in.(*admin.IssueTenantPublisherCredentialRequest))
	case "tenant-credential-reset":
		return s.ResetTenantPublisherCredential(ctx, in.(*admin.ResetTenantPublisherCredentialRequest))
	case "tenant-credential-disable":
		return s.DisableTenantPublisherCredential(ctx, in.(*admin.DisableTenantPublisherCredentialRequest))
	case "tenant-registration-list":
		return s.ListTenantImages(ctx, in.(*admin.ListTenantImagesRequest))
	case "tenant-registration-get":
		return s.GetTenantImage(ctx, in.(*admin.GetTenantImageRequest))
	case "tenant-registration-create":
		return s.RegisterTenantImage(ctx, in.(*admin.RegisterTenantImageRequest))
	case "tenant-registration-update":
		return s.UpdateTenantImage(ctx, in.(*admin.UpdateTenantImageRequest))
	case "tenant-registration-unregister":
		return s.UnregisterTenantImage(ctx, in.(*admin.UnregisterTenantImageRequest))
	}
	return nil, imageManagementBadRequest()
}
