package server

import (
	"bytes"
	"context"
	"errors"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"io"
	"strings"
)

// RegisterVMImageHTTPServer mounts the tenant image control routes. Like the VM
// lane these are registered from an explicit table, because the public surface is
// a fixed set of paths with a fixed permission code each and no generated
// handler may be reached with an unauthenticated tenant.
func RegisterVMImageHTTPServer(server *http.Server, s *service.VMImageService) {
	if server == nil || s == nil {
		return
	}
	type route struct {
		action, operation string
		request           func() proto.Message
	}
	routes := []route{
		{"image-list", "ListVMImages", func() proto.Message { return new(view.ListVMImagesRequest) }},
		{"image-get", "GetVMImage", func() proto.Message { return new(view.GetVMImageRequest) }},
		{"image-create", "CreateVMImage", func() proto.Message { return new(view.CreateVMImageRequest) }},
		{"image-session", "CreateVMImageUploadSession", func() proto.Message { return new(view.CreateVMImageUploadSessionRequest) }},
		{"image-update", "UpdateVMImage", func() proto.Message { return new(view.UpdateVMImageRequest) }},
		{"image-delete", "DeleteVMImage", func() proto.Message { return new(view.DeleteVMImageRequest) }},
	}
	for _, entry := range routes {
		entry := entry
		registered, ok := data.VMActionRoute(entry.action)
		if !ok {
			panic("VM image route missing")
		}
		handler := func(ctx http.Context) error {
			http.SetOperation(ctx, "/admin.service.v1.VMImageService/"+entry.operation)
			request := entry.request()
			if registered.Method == "GET" && entry.action != "image-get" {
				if err := decodeInstanceQuery(ctx.Request(), request); err != nil {
					return vmImageInvalidHTTP()
				}
			} else if registered.Method != "GET" {
				// The image lane never carries file bytes, so a fixed small body
				// limit is enough and a large upload cannot be mistaken for a
				// control request.
				raw, err := io.ReadAll(io.LimitReader(ctx.Request().Body, 65537))
				if err != nil || len(raw) > 65536 {
					return vmImageInvalidHTTP()
				}
				ctx.Request().Body = io.NopCloser(bytes.NewReader(raw))
				if err = decodeStrictJSONHTTP(ctx.Request(), request); err != nil {
					return vmImageInvalidHTTP()
				}
			}
			if strings.Contains(registered.Path, "{") {
				if err := ctx.BindVars(request); err != nil {
					return err
				}
			}
			if entry.action == "image-session" {
				// A ticket must not be cached, replayed from a cache or stored by
				// an intermediary.
				ctx.Response().Header().Set("Cache-Control", "no-store")
				ctx.Response().Header().Set("Pragma", "no-cache")
			}
			out, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
				return invokeVMImageHTTP(call, s, entry.action, in.(proto.Message))
			})(ctx, request)
			if err != nil {
				return vmImageHTTPError(err)
			}
			// POST create and DELETE are accepted work, not completed work: the
			// status code is decided here, not by a Proto annotation.
			code := 200
			switch registered.Method {
			case "POST":
				if entry.action == "image-create" {
					code = 202
				}
			case "PATCH", "DELETE":
				code = 202
			}
			return ctx.Result(code, out)
		}
		router := server.Route("/")
		switch registered.Method {
		case "GET":
			router.GET(registered.Path, handler)
		case "POST":
			router.POST(registered.Path, handler)
		case "PATCH":
			router.PATCH(registered.Path, handler)
		case "DELETE":
			router.DELETE(registered.Path, handler)
		default:
			panic("VM image method unsupported")
		}
	}
}

func invokeVMImageHTTP(ctx context.Context, s *service.VMImageService, action string, in proto.Message) (proto.Message, error) {
	switch action {
	case "image-list":
		return s.ListVMImages(ctx, in.(*view.ListVMImagesRequest))
	case "image-get":
		return s.GetVMImage(ctx, in.(*view.GetVMImageRequest))
	case "image-create":
		return s.CreateVMImage(ctx, in.(*view.CreateVMImageRequest))
	case "image-session":
		return s.CreateVMImageUploadSession(ctx, in.(*view.CreateVMImageUploadSessionRequest))
	case "image-update":
		return s.UpdateVMImage(ctx, in.(*view.UpdateVMImageRequest))
	case "image-delete":
		return s.DeleteVMImage(ctx, in.(*view.DeleteVMImageRequest))
	}
	return nil, vmImageInvalidHTTP()
}

func vmImageInvalidHTTP() error {
	return kratoserrors.BadRequest("VM_IMAGE_INVALID_ARGUMENT", "invalid VM image request")
}

// vmImageReasons is the closed set a backend failure may surface. Anything else
// collapses to a dependency failure so a Kubernetes status string, a namespace or
// a native object name can never reach a browser.
var vmImageReasons = map[string]bool{
	"VM_IMAGE_INVALID_ARGUMENT": true, "VM_IMAGE_NOT_FOUND": true, "VM_IMAGE_NOT_READY": true,
	"VM_IMAGE_NOT_UPLOADABLE": true, "VM_IMAGE_IN_USE": true, "VM_IMAGE_KIND_MISMATCH": true,
	"VM_IMAGE_UPLOAD_DISABLED": true, "VM_IMAGE_DEPENDENCY_UNAVAILABLE": true,
	"VM_IMAGE_NATIVE_IDENTITY_CHANGED": true, "VM_VERSION_CONFLICT": true, "VM_IDEMPOTENCY_CONFLICT": true,
	"VM_SCOPE_DENIED": true, "VM_ADAPTER_UNAVAILABLE": true,
}

func vmImageHTTPError(err error) error {
	if err == nil {
		return nil
	}
	// A reason the image contract does not publish is never forwarded: the caller
	// sees one dependency failure code instead of an internal status string.
	var known *kratoserrors.Error
	if errors.As(err, &known) {
		if vmImageReasons[known.Reason] {
			return known
		}
		return kratoserrors.New(503, "VM_IMAGE_DEPENDENCY_UNAVAILABLE", "VM image request failed")
	}
	code, reason := 503, "VM_IMAGE_DEPENDENCY_UNAVAILABLE"
	switch status.Code(err) {
	case codes.InvalidArgument:
		code, reason = 400, "VM_IMAGE_INVALID_ARGUMENT"
	case codes.Unauthenticated:
		code, reason = 401, "VM_SCOPE_DENIED"
	case codes.PermissionDenied:
		code, reason = 403, "VM_SCOPE_DENIED"
	case codes.NotFound:
		code, reason = 404, "VM_IMAGE_NOT_FOUND"
	case codes.AlreadyExists:
		code, reason = 409, "VM_IDEMPOTENCY_CONFLICT"
	case codes.FailedPrecondition:
		code, reason = 409, "VM_IMAGE_NOT_READY"
	}
	st := status.Convert(err)
	if vmImageReasons[st.Message()] {
		reason = st.Message()
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && vmImageReasons[info.Reason] {
			reason = info.Reason
		}
	}
	return kratoserrors.New(code, reason, "VM image request failed")
}
