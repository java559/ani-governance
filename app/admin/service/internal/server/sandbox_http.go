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
	case codes.ResourceExhausted:
		code, reason = 413, "SANDBOX_FILE_TOO_LARGE"
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
	for field, entries := range values {
		if len(entries) != 1 {
			return invalid
		}
		value := entries[0]
		switch target := message.(type) {
		case *view.ListSandboxesRequest:
			if field != "page" && field != "pageSize" {
				return invalid
			}
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
		case *view.ListSandboxFilesRequest:
			switch field {
			case "path":
				target.Path = value
			case "cursor":
				target.Cursor = value
			case "limit":
				n, err := strconv.ParseUint(value, 10, 32)
				if err != nil || n == 0 || n > 500 || strconv.FormatUint(n, 10) != value {
					return invalid
				}
				target.Limit = uint32(n)
			default:
				return invalid
			}
		case *view.ReadSandboxFileRequest:
			if field != "path" {
				return invalid
			}
			target.Path = value
		default:
			return invalid
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
	case *view.CreateSandboxTokenRequest:
		path := bound.(*view.CreateSandboxTokenRequest).Data
		if in.Data == nil || path == nil || in.Data.SandboxId != "" && in.Data.SandboxId != path.SandboxId {
			return invalid
		}
		in.Data.SandboxId = path.SandboxId
	case *view.WriteSandboxFileRequest:
		path := bound.(*view.WriteSandboxFileRequest).Data
		if in.Data == nil || path == nil || in.Data.SandboxId != "" && in.Data.SandboxId != path.SandboxId {
			return invalid
		}
		in.Data.SandboxId = path.SandboxId
	case *view.DeleteSandboxFileRequest:
		path := bound.(*view.DeleteSandboxFileRequest).Data
		if in.Data == nil || path == nil || in.Data.SandboxId != "" && in.Data.SandboxId != path.SandboxId {
			return invalid
		}
		in.Data.SandboxId = path.SandboxId
	case *view.RunSandboxCodeRequest:
		path := bound.(*view.RunSandboxCodeRequest).Data
		if in.Data == nil || path == nil || in.Data.SandboxId != "" && in.Data.SandboxId != path.SandboxId {
			return invalid
		}
		in.Data.SandboxId = path.SandboxId
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
		{"POST", data.SandboxTokenCreatePath, admin.OperationSandboxServiceCreateSandboxToken, func() proto.Message { return new(view.CreateSandboxTokenRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.CreateSandboxToken(c, r.(*view.CreateSandboxTokenRequest))
		}},
		{"DELETE", data.SandboxTokenRevokePath, admin.OperationSandboxServiceRevokeSandboxToken, func() proto.Message { return new(view.RevokeSandboxTokenRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.RevokeSandboxToken(c, r.(*view.RevokeSandboxTokenRequest))
		}},
		{"GET", data.SandboxFilesListPath, admin.OperationSandboxServiceListSandboxFiles, func() proto.Message { return new(view.ListSandboxFilesRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.ListSandboxFiles(c, r.(*view.ListSandboxFilesRequest))
		}},
		{"GET", data.SandboxFilesContentPath, admin.OperationSandboxServiceReadSandboxFile, func() proto.Message { return new(view.ReadSandboxFileRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.ReadSandboxFile(c, r.(*view.ReadSandboxFileRequest))
		}},
		{"POST", data.SandboxFilesMutatePath, admin.OperationSandboxServiceWriteSandboxFile, func() proto.Message { return new(view.WriteSandboxFileRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.WriteSandboxFile(c, r.(*view.WriteSandboxFileRequest))
		}},
		{"DELETE", data.SandboxFilesMutatePath, admin.OperationSandboxServiceDeleteSandboxFile, func() proto.Message { return new(view.DeleteSandboxFileRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.DeleteSandboxFile(c, r.(*view.DeleteSandboxFileRequest))
		}},
		{"GET", data.SandboxFilesOperationPath, admin.OperationSandboxServiceGetSandboxFileOperation, func() proto.Message { return new(view.GetSandboxFileOperationRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.GetSandboxFileOperation(c, r.(*view.GetSandboxFileOperationRequest))
		}},
		{"POST", data.SandboxCodeRunPath, admin.OperationSandboxServiceRunSandboxCode, func() proto.Message { return new(view.RunSandboxCodeRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.RunSandboxCode(c, r.(*view.RunSandboxCodeRequest))
		}},
		{"GET", data.SandboxCodeRunGetPath, admin.OperationSandboxServiceGetSandboxCodeRun, func() proto.Message { return new(view.GetSandboxCodeRunRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.GetSandboxCodeRun(c, r.(*view.GetSandboxCodeRunRequest))
		}},
	}
	for _, route := range routes {
		r := route
		handler := func(ctx http.Context) error {
			http.SetOperation(ctx, r.operation)
			request := r.request()
			bindRequest := func() error {
				if r.method == "GET" || r.operation == admin.OperationSandboxServiceRevokeSandboxToken {
					if err := decodeSandboxQuery(ctx.Request(), request); err != nil {
						return err
					}
				} else {
					limit := int64(65536)
					if r.operation == admin.OperationSandboxServiceRunSandboxCode || r.operation == admin.OperationSandboxServiceWriteSandboxFile {
						limit = 7 << 20
					}
					if err := decodeStrictJSONHTTPWithLimit(ctx.Request(), request, limit); err != nil {
						return kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid sandbox request")
					}
				}
				bound := r.request()
				if err := ctx.BindVars(bound); err != nil {
					return kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid sandbox path")
				}
				return bindSandboxPath(request, bound)
			}
			// Code-run accepts a larger body. Authenticate via middleware before
			// reading it; other Sandbox routes retain their existing ordering.
			codeRun := r.operation == admin.OperationSandboxServiceRunSandboxCode || r.operation == admin.OperationSandboxServiceWriteSandboxFile
			if !codeRun {
				if err := bindRequest(); err != nil {
					return err
				}
			}
			result, err := ctx.Middleware(func(c context.Context, in any) (any, error) {
				if codeRun {
					if err := bindRequest(); err != nil {
						return nil, err
					}
				}
				return r.invoke(c, in.(proto.Message))
			})(ctx, request)
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
