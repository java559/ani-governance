package server

import (
	"context"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/proto"
	"strings"
)

func RegisterInstanceHTTPServer(server *http.Server, s *service.InstanceService) {
	RegisterInstanceDiagnosticsHTTP(server, s)
	type route struct {
		method, path, operation string
		request                 func() proto.Message
		invoke                  func(context.Context, proto.Message) (proto.Message, error)
	}
	routes := []route{
		{"GET", data.InstanceSchedulingSettingsPath, admin.OperationInstanceServiceGetInstanceSchedulingSettings, func() proto.Message { return new(view.InstanceSchedulingSettingsRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.GetInstanceSchedulingSettings(c, r.(*view.InstanceSchedulingSettingsRequest))
		}},
		{"PUT", data.InstanceSchedulingSettingsPath, admin.OperationInstanceServiceUpdateInstanceSchedulingSettings, func() proto.Message { return new(view.UpdateInstanceSchedulingSettingsRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.UpdateInstanceSchedulingSettings(c, r.(*view.UpdateInstanceSchedulingSettingsRequest))
		}},
		{"GET", data.InstanceSchedulerConfigPath, admin.OperationInstanceServiceGetSchedulerConfig, func() proto.Message { return new(view.SchedulerConfigRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.GetSchedulerConfig(c, r.(*view.SchedulerConfigRequest))
		}},
		{"PUT", data.InstanceSchedulerConfigPath, admin.OperationInstanceServiceUpdateSchedulerConfig, func() proto.Message { return new(view.UpdateSchedulerConfigRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.UpdateSchedulerConfig(c, r.(*view.UpdateSchedulerConfigRequest))
		}},
		{"GET", data.InstanceAdminPresetsPath, admin.OperationInstanceServiceAdminListPresets, func() proto.Message { return new(view.AdminListPresetsRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminListPresets(c, r.(*view.AdminListPresetsRequest))
		}},
		{"GET", data.InstanceAdminPresetPath, admin.OperationInstanceServiceAdminGetPreset, func() proto.Message { return new(view.PresetRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminGetPreset(c, r.(*view.PresetRequest))
		}},
		{"POST", data.InstanceAdminPresetsPath, admin.OperationInstanceServiceAdminCreatePreset, func() proto.Message { return new(view.CreatePresetRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminCreatePreset(c, r.(*view.CreatePresetRequest))
		}},
		{"PATCH", data.InstanceAdminPresetPath, admin.OperationInstanceServiceAdminUpdatePreset, func() proto.Message { return new(view.UpdatePresetRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminUpdatePreset(c, r.(*view.UpdatePresetRequest))
		}},
		{"POST", data.InstanceAdminPresetEnabledPath, admin.OperationInstanceServiceAdminSetPresetEnabled, func() proto.Message { return new(view.SetPresetEnabledRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminSetPresetEnabled(c, r.(*view.SetPresetEnabledRequest))
		}},
		{"DELETE", data.InstanceAdminPresetPath, admin.OperationInstanceServiceAdminDeletePreset, func() proto.Message { return new(view.PresetRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.AdminDeletePreset(c, r.(*view.PresetRequest))
		}},
		{"POST", data.InstanceCreatePath, admin.OperationInstanceServiceCreateInstance, func() proto.Message { return new(view.CreateInstanceRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.CreateInstance(c, r.(*view.CreateInstanceRequest))
		}},
		{"GET", data.InstanceCreatePath, admin.OperationInstanceServiceListInstances, func() proto.Message { return new(view.ListInstancesRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.ListInstances(c, r.(*view.ListInstancesRequest))
		}},
		{"GET", data.InstanceGetPath, admin.OperationInstanceServiceGetInstance, func() proto.Message { return new(view.GetInstanceRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.GetInstance(c, r.(*view.GetInstanceRequest))
		}},
		{"POST", data.InstanceStartPath, admin.OperationInstanceServiceStartInstance, func() proto.Message { return new(view.ChangeInstanceRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.StartInstance(c, r.(*view.ChangeInstanceRequest))
		}},
		{"POST", data.InstanceStopPath, admin.OperationInstanceServiceStopInstance, func() proto.Message { return new(view.ChangeInstanceRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.StopInstance(c, r.(*view.ChangeInstanceRequest))
		}},
		{"DELETE", data.InstanceDeletePath, admin.OperationInstanceServiceDeleteInstance, func() proto.Message { return new(view.ChangeInstanceRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.DeleteInstance(c, r.(*view.ChangeInstanceRequest))
		}},
		{"GET", data.InstanceOperationPath, admin.OperationInstanceServiceGetInstanceOperation, func() proto.Message { return new(view.GetOperationRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.GetInstanceOperation(c, r.(*view.GetOperationRequest))
		}},
		{"GET", data.InstancePresetPath, admin.OperationInstanceServiceListInstancePresets, func() proto.Message { return new(view.ListPresetsRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.ListInstancePresets(c, r.(*view.ListPresetsRequest))
		}},
		{"POST", data.InstanceExecPath, admin.OperationInstanceServiceExecInstance, func() proto.Message { return new(view.ExecInstanceRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return s.ExecInstance(c, r.(*view.ExecInstanceRequest))
		}},
	}
	for _, r := range routes {
		r := r
		handler := func(ctx http.Context) error {
			http.SetOperation(ctx, r.operation)
			request := r.request()
			write := r.method != "GET"
			if r.path == data.InstanceExecPath {
				ctx.Response().Header().Set("Cache-Control", "no-store")
			}
			if write && !(r.method == "DELETE" && r.path == data.InstanceAdminPresetPath) {
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
				case *view.ChangeInstanceRequest:
					bound := path.(*view.ChangeInstanceRequest)
					if in.Data == nil || bound.Data == nil || (in.Data.InstanceId != "" && in.Data.InstanceId != bound.Data.InstanceId) {
						return kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "instance path mismatch")
					}
					in.Data.InstanceId = bound.Data.InstanceId
				case *view.ExecInstanceRequest:
					bound := path.(*view.ExecInstanceRequest)
					if in.Data == nil || bound.Data == nil || (in.Data.InstanceId != "" && in.Data.InstanceId != bound.Data.InstanceId) {
						return kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "instance path mismatch")
					}
					in.Data.InstanceId = bound.Data.InstanceId
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
			if r.path == data.InstanceSchedulerConfigPath || r.path == data.InstanceSchedulingSettingsPath {
				code = 200
			}
			if r.path == data.InstanceAdminPresetsPath && r.method == "POST" {
				code = 201
			}
			if r.path == data.InstanceAdminPresetPath || r.path == data.InstanceAdminPresetEnabledPath {
				code = 200
				if r.method == "DELETE" {
					code = 204
				}
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
