package server

import (
	"context"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/compute/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/proto"
	"strings"
)

func RegisterComputeHTTPServer(server *http.Server, s *service.ComputePresetService) {
	type route struct {
		method, path, operation string
		request                 func() proto.Message
		invoke                  func(context.Context, proto.Message) (proto.Message, error)
	}
	routes := []route{
		{"GET", data.ComputeAdminPresetsPath, admin.OperationComputePresetServiceAdminListPresets, func() proto.Message { return new(view.AdminListPresetsRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminListPresets(c, r.(*view.AdminListPresetsRequest))
		}},
		{"GET", data.ComputeAdminPresetPath, admin.OperationComputePresetServiceAdminGetPreset, func() proto.Message { return new(view.PresetRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminGetPreset(c, r.(*view.PresetRequest))
		}},
		{"POST", data.ComputeAdminPresetsPath, admin.OperationComputePresetServiceAdminCreatePreset, func() proto.Message { return new(view.CreatePresetRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminCreatePreset(c, r.(*view.CreatePresetRequest))
		}},
		{"PATCH", data.ComputeAdminPresetPath, admin.OperationComputePresetServiceAdminUpdatePreset, func() proto.Message { return new(view.UpdatePresetRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminUpdatePreset(c, r.(*view.UpdatePresetRequest))
		}},
		{"POST", data.ComputeAdminPresetEnabledPath, admin.OperationComputePresetServiceAdminSetPresetEnabled, func() proto.Message { return new(view.SetPresetEnabledRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminSetPresetEnabled(c, r.(*view.SetPresetEnabledRequest))
		}},
		{"DELETE", data.ComputeAdminPresetPath, admin.OperationComputePresetServiceAdminDeletePreset, func() proto.Message { return new(view.PresetRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminDeletePreset(c, r.(*view.PresetRequest))
		}},
	}
	for _, r := range routes {
		r := r
		handler := func(ctx http.Context) error {
			http.SetOperation(ctx, r.operation)
			request := r.request()
			write := r.method != "GET"
			ctx.Response().Header().Set("Cache-Control", "no-store")
			if write && !(r.method == "DELETE" && r.path == data.ComputeAdminPresetPath) {
				if e := decodeInstanceHTTP(ctx.Request(), request); e != nil {
					return e
				}
			} else {
				if e := decodeInstanceQuery(ctx.Request(), request); e != nil {
					return e
				}
			}
			if strings.Contains(r.path, "{") {
				path := r.request()
				if e := ctx.BindVars(path); e != nil {
					return e
				}
				switch in := request.(type) {
				case *view.UpdatePresetRequest:
					bound := path.(*view.UpdatePresetRequest)
					if in.Id != "" && in.Id != bound.Id {
						return kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "preset path mismatch")
					}
					in.Id = bound.Id
				case *view.SetPresetEnabledRequest:
					bound := path.(*view.SetPresetEnabledRequest)
					if in.Id != "" && in.Id != bound.Id {
						return kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "preset path mismatch")
					}
					in.Id = bound.Id
				default:
					if e := ctx.BindVars(request); e != nil {
						return e
					}
				}
			}
			out, e := ctx.Middleware(func(call context.Context, in any) (any, error) { return r.invoke(call, in.(proto.Message)) })(ctx, request)
			if e != nil {
				return instanceHTTPError(e)
			}
			code := 200
			if write {
				code = 202
			}
			if r.path == data.ComputeAdminPresetsPath && r.method == "POST" {
				code = 201
			}
			if r.path == data.ComputeAdminPresetPath || r.path == data.ComputeAdminPresetEnabledPath {
				code = 200
				if r.method == "DELETE" {
					code = 204
				}
			}
			if code == 204 {
				ctx.Response().WriteHeader(code)
				return nil
			}
			return ctx.Result(code, out)
		}
		router := server.Route("/")
		switch r.method {
		case "GET":
			router.GET(r.path, handler)
		case "POST":
			router.POST(r.path, handler)
		case "DELETE":
			router.DELETE(r.path, handler)
		case "PUT":
			router.PUT(r.path, handler)
		case "PATCH":
			router.PATCH(r.path, handler)
		}
	}
}
