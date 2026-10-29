package server

import (
	"context"
	stderrors "errors"
	"io"
	nethttp "net/http"
	"net/url"
	"strconv"
	"strings"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/sandbox/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func sandboxHTTPError(err error) error {
	if err == nil {
		return nil
	}
	var known *kratoserrors.Error
	if stderrors.As(err, &known) {
		return known
	}
	code, reason := 503, "SANDBOX_DEPENDENCY_UNAVAILABLE"
	switch status.Code(err) {
	case codes.InvalidArgument:
		code, reason = 400, "INVALID_SANDBOX_REQUEST"
	case codes.Unauthenticated:
		code, reason = 401, "SANDBOX_AUTHENTICATION_FAILED"
	case codes.PermissionDenied:
		code, reason = 403, "SANDBOX_SCOPE_DENIED"
	case codes.NotFound:
		code, reason = 404, "SANDBOX_NOT_FOUND"
	case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
		code, reason = 409, "SANDBOX_CONFLICT"
	case codes.Unimplemented:
		code, reason = 501, "SANDBOX_UNSUPPORTED"
	case codes.Canceled:
		code, reason = 499, "SANDBOX_CANCELED"
	}
	message := status.Convert(err).Message()
	if instancePublicReason.MatchString(message) && (strings.HasPrefix(message, "SANDBOX_") || strings.HasPrefix(message, "INSTANCE_") || strings.HasPrefix(message, "COMPUTE_PRESET_")) {
		reason = message
	}
	return kratoserrors.New(code, reason, "sandbox request failed")
}

func decodeSandboxQuery(r *nethttp.Request, message proto.Message) error {
	invalid := kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid sandbox query")
	if r.URL.ForceQuery || r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path {
		return invalid
	}
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(raw) != 0 {
			return invalid
		}
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid
	}
	target, list := message.(*view.ListSandboxesRequest)
	for field, values := range values {
		if !list || field != "page" && field != "pageSize" || len(values) != 1 {
			return invalid
		}
		value := values[0]
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != value || field == "pageSize" && n > 100 {
			return invalid
		}
		number := uint32(n)
		if field == "page" {
			target.Page = &number
		} else {
			target.PageSize = &number
		}
	}
	return nil
}

func bindSandboxPath(request, bound proto.Message) error {
	invalid := kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "sandbox path mismatch")
	bind := func(in, path *view.SandboxActionData) error {
		if in == nil || path == nil || in.InstanceId != "" || in.SandboxId != "" && in.SandboxId != path.SandboxId {
			return invalid
		}
		in.SandboxId = path.SandboxId
		return nil
	}
	switch in := request.(type) {
	case *view.CreateSandboxRequest:
		if in.Data == nil || in.Data.InstanceId != "" {
			return invalid
		}
	case *view.PauseSandboxRequest:
		return bind(in.Data, bound.(*view.PauseSandboxRequest).Data)
	case *view.ResumeSandboxRequest:
		return bind(in.Data, bound.(*view.ResumeSandboxRequest).Data)
	case *view.DeleteSandboxRequest:
		return bind(in.Data, bound.(*view.DeleteSandboxRequest).Data)
	default:
		proto.Merge(request, bound)
	}
	return nil
}

// RegisterSandboxHTTPServer keeps the generated route/DTO contract while applying
// the public strict-input and error boundaries used by the other Resource BFFs.
func RegisterSandboxHTTPServer(server *http.Server, s admin.SandboxServiceHTTPServer) {
	type route struct {
		method, path, operation string
		request                 func() proto.Message
		invoke                  func(context.Context, proto.Message) (proto.Message, error)
	}
	routes := []route{
		{"POST", data.SandboxCreatePath, admin.OperationSandboxServiceCreateSandbox, func() proto.Message { return new(view.CreateSandboxRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.CreateSandbox(c, r.(*view.CreateSandboxRequest))
		}},
		{"GET", data.SandboxListPath, admin.OperationSandboxServiceListSandboxes, func() proto.Message { return new(view.ListSandboxesRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.ListSandboxes(c, r.(*view.ListSandboxesRequest))
		}},
		{"GET", data.SandboxGetPath, admin.OperationSandboxServiceGetSandbox, func() proto.Message { return new(view.GetSandboxRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.GetSandbox(c, r.(*view.GetSandboxRequest))
		}},
		{"GET", data.SandboxOperationPath, admin.OperationSandboxServiceGetSandboxOperation, func() proto.Message { return new(view.GetSandboxOperationRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.GetSandboxOperation(c, r.(*view.GetSandboxOperationRequest))
		}},
		{"POST", data.SandboxPausePath, admin.OperationSandboxServicePauseSandbox, func() proto.Message { return new(view.PauseSandboxRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.PauseSandbox(c, r.(*view.PauseSandboxRequest))
		}},
		{"POST", data.SandboxResumePath, admin.OperationSandboxServiceResumeSandbox, func() proto.Message { return new(view.ResumeSandboxRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.ResumeSandbox(c, r.(*view.ResumeSandboxRequest))
		}},
		{"DELETE", data.SandboxDeletePath, admin.OperationSandboxServiceDeleteSandbox, func() proto.Message { return new(view.DeleteSandboxRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.DeleteSandbox(c, r.(*view.DeleteSandboxRequest))
		}},
	}
	for _, route := range routes {
		r := route
		handler := func(ctx http.Context) error {
			http.SetOperation(ctx, r.operation)
			request := r.request()
			if r.method == "GET" {
				if err := decodeSandboxQuery(ctx.Request(), request); err != nil {
					return err
				}
			} else if err := decodeStrictJSONHTTP(ctx.Request(), request); err != nil {
				return kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid sandbox request")
			}
			bound := r.request()
			if err := ctx.BindVars(bound); err != nil {
				return kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid sandbox path")
			}
			if err := bindSandboxPath(request, bound); err != nil {
				return err
			}
			result, err := ctx.Middleware(func(c context.Context, in any) (any, error) { return r.invoke(c, in.(proto.Message)) })(ctx, request)
			if err != nil {
				return sandboxHTTPError(err)
			}
			ctx.Response().Header().Set("Cache-Control", "no-store")
			return ctx.Result(200, result)
		}
		router := server.Route("/")
		switch r.method {
		case "GET":
			router.GET(r.path, handler)
		case "POST":
			router.POST(r.path, handler)
		case "DELETE":
			router.DELETE(r.path, handler)
		}
	}
}
