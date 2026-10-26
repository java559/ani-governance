package server

import (
	"context"
	"errors"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"strings"
)

func RegisterVMHTTPServer(server *http.Server, s *service.VMService) {
	if server == nil || s == nil {
		return
	}
	type route struct {
		action, operation string
		request           func() proto.Message
	}
	change := func() proto.Message { return new(view.ChangeVMRequest) }
	get := func() proto.Message { return new(view.GetVMRequest) }
	routes := []route{
		{"create", "CreateVM", func() proto.Message { return new(view.CreateVMRequest) }},
		{"list", "ListVMs", func() proto.Message { return new(view.ListVMsRequest) }},
		{"get", "GetVM", get}, {"delete", "DeleteVM", change},
		{"operation", "GetVMOperation", func() proto.Message { return new(view.GetOperationRequest) }},
		{"start", "StartVM", change}, {"stop", "StopVM", change}, {"restart", "RestartVM", change}, {"force-stop", "ForceStopVM", change},
		{"disks", "ListVMDisks", get}, {"create-disk", "CreateVMDisk", change}, {"attach-disk", "AttachVMDisk", change}, {"detach-disk", "DetachVMDisk", change}, {"delete-disk", "DeleteVMDisk", change},
		{"media", "ListVMMedia", func() proto.Message { return new(view.ListMediaRequest) }},
		{"dns-defaults", "GetVMDNSDefaults", func() proto.Message { return new(view.GetDNSDefaultsRequest) }},
		{"set-media", "SetVMMedia", change}, {"eject-media", "EjectVMMedia", change}, {"set-boot", "SetVMBootDevice", change},
		{"snapshots", "ListVMSnapshots", get}, {"snapshot", "GetVMSnapshot", func() proto.Message { return new(view.GetSnapshotRequest) }},
		{"create-snapshot", "CreateVMSnapshot", change}, {"delete-snapshot", "DeleteVMSnapshot", change}, {"preview-restore", "PreviewVMRestore", change}, {"restore-snapshot", "RestoreVMSnapshot", change}, {"console", "CreateVMConsoleSession", change},
	}
	for _, entry := range routes {
		entry := entry
		registered, ok := data.VMActionRoute(entry.action)
		if !ok {
			panic("VM route missing")
		}
		handler := func(ctx http.Context) error {
			http.SetOperation(ctx, "/admin.service.v1.VMService/"+entry.operation)
			request := entry.request()
			if registered.Method == "GET" {
				if err := decodeInstanceQuery(ctx.Request(), request); err != nil {
					return kratoserrors.BadRequest("VM_INVALID_REQUEST", "invalid VM query")
				}
			} else {
				if err := decodeStrictJSONHTTP(ctx.Request(), request); err != nil {
					return kratoserrors.BadRequest("VM_INVALID_REQUEST", "invalid VM request")
				}
			}
			if strings.Contains(registered.Path, "{") {
				bound := entry.request()
				if err := ctx.BindVars(bound); err != nil {
					return err
				}
				if in, ok := request.(*view.ChangeVMRequest); ok {
					from := bound.(*view.ChangeVMRequest)
					if in.Data == nil || from.Data == nil {
						return kratoserrors.BadRequest("VM_INVALID_REQUEST", "VM data required")
					}
					pairs := []struct {
						body *string
						path string
					}{{&in.Data.VmId, from.Data.VmId}, {&in.Data.DiskId, from.Data.DiskId}, {&in.Data.SnapshotId, from.Data.SnapshotId}, {&in.Data.Slot, from.Data.Slot}}
					for _, pair := range pairs {
						if pair.path != "" {
							if *pair.body != "" && *pair.body != pair.path {
								return kratoserrors.BadRequest("VM_INVALID_REQUEST", "VM path mismatch")
							}
							*pair.body = pair.path
						}
					}
				} else {
					if err := ctx.BindVars(request); err != nil {
						return err
					}
				}
			}
			if entry.action == "console" {
				ctx.Response().Header().Set("Cache-Control", "no-store")
			}
			out, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
				return invokeVMHTTP(call, s, entry.action, in.(proto.Message))
			})(ctx, request)
			if err != nil {
				return vmHTTPError(err)
			}
			code := 200
			if registered.Method != "GET" && entry.action != "preview-restore" {
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
		case "PUT":
			router.PUT(registered.Path, handler)
		case "DELETE":
			router.DELETE(registered.Path, handler)
		}
	}
}

func vmHTTPError(err error) error {
	if err == nil {
		return nil
	}
	var known *kratoserrors.Error
	if errors.As(err, &known) {
		switch known.Reason {
		case "INVALID_QUOTA_REQUEST":
			return kratoserrors.BadRequest("VM_INVALID_REQUEST", "invalid VM request")
		case "IDEMPOTENCY_CONFLICT":
			return kratoserrors.Conflict("VM_IDEMPOTENCY_CONFLICT", "VM request differs from original acceptance")
		case "QUOTA_NOT_FOUND":
			return kratoserrors.NotFound("VM_NOT_FOUND", "VM not found")
		case "QUOTA_ADMISSION_DENIED":
			return kratoserrors.Forbidden("VM_SCOPE_DENIED", "VM admission denied")
		case "QUOTA_STORAGE_UNAVAILABLE", "QUOTA_ADAPTER_UNAVAILABLE":
			return kratoserrors.ServiceUnavailable("VM_DEPENDENCY_NOT_READY", "VM dependency unavailable")
		}
		return known
	}
	code, reason := 503, "VM_DEPENDENCY_NOT_READY"
	switch status.Code(err) {
	case codes.InvalidArgument:
		code, reason = 400, "VM_INVALID_REQUEST"
	case codes.Unauthenticated:
		code, reason = 401, "VM_SCOPE_DENIED"
	case codes.PermissionDenied:
		code, reason = 403, "VM_SCOPE_DENIED"
	case codes.NotFound:
		code, reason = 404, "VM_NOT_FOUND"
	case codes.AlreadyExists:
		code, reason = 409, "VM_IDEMPOTENCY_CONFLICT"
	case codes.Aborted:
		code, reason = 409, "VM_VERSION_CONFLICT"
	case codes.FailedPrecondition:
		code, reason = 409, "VM_OPERATION_CONFLICT"
	case codes.Canceled:
		code, reason = 499, "VM_EXTERNAL_RESULT_UNKNOWN"
	}
	allowed := map[string]bool{"VM_SCOPE_DENIED": true, "VM_NOT_FOUND": true, "VM_INVALID_REQUEST": true, "VM_IDEMPOTENCY_CONFLICT": true, "VM_VERSION_CONFLICT": true, "VM_OPERATION_CONFLICT": true, "VM_DEPENDENCY_NOT_READY": true, "VM_DEPENDENCY_CHANGED": true, "VM_OBJECT_OWNERSHIP_CONFLICT": true, "VM_EXTERNAL_RESULT_UNKNOWN": true, "VM_SNAPSHOT_INCOMPLETE": true, "VM_RESTORE_PREVIEW_CHANGED": true, "VM_CONSOLE_TARGET_CHANGED": true}
	st := status.Convert(err)
	if allowed[st.Message()] {
		reason = st.Message()
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && allowed[info.Reason] {
			reason = info.Reason
		}
	}
	return kratoserrors.New(code, reason, "VM request failed")
}
func invokeVMHTTP(ctx context.Context, s *service.VMService, action string, in proto.Message) (proto.Message, error) {
	switch action {
	case "create":
		return s.CreateVM(ctx, in.(*view.CreateVMRequest))
	case "list":
		return s.ListVMs(ctx, in.(*view.ListVMsRequest))
	case "get":
		return s.GetVM(ctx, in.(*view.GetVMRequest))
	case "delete":
		return s.DeleteVM(ctx, in.(*view.ChangeVMRequest))
	case "operation":
		return s.GetVMOperation(ctx, in.(*view.GetOperationRequest))
	case "start":
		return s.StartVM(ctx, in.(*view.ChangeVMRequest))
	case "stop":
		return s.StopVM(ctx, in.(*view.ChangeVMRequest))
	case "restart":
		return s.RestartVM(ctx, in.(*view.ChangeVMRequest))
	case "force-stop":
		return s.ForceStopVM(ctx, in.(*view.ChangeVMRequest))
	case "disks":
		return s.ListVMDisks(ctx, in.(*view.GetVMRequest))
	case "create-disk":
		return s.CreateVMDisk(ctx, in.(*view.ChangeVMRequest))
	case "attach-disk":
		return s.AttachVMDisk(ctx, in.(*view.ChangeVMRequest))
	case "detach-disk":
		return s.DetachVMDisk(ctx, in.(*view.ChangeVMRequest))
	case "delete-disk":
		return s.DeleteVMDisk(ctx, in.(*view.ChangeVMRequest))
	case "media":
		return s.ListVMMedia(ctx, in.(*view.ListMediaRequest))
	case "dns-defaults":
		return s.GetVMDNSDefaults(ctx, in.(*view.GetDNSDefaultsRequest))
	case "set-media":
		return s.SetVMMedia(ctx, in.(*view.ChangeVMRequest))
	case "eject-media":
		return s.EjectVMMedia(ctx, in.(*view.ChangeVMRequest))
	case "set-boot":
		return s.SetVMBootDevice(ctx, in.(*view.ChangeVMRequest))
	case "snapshots":
		return s.ListVMSnapshots(ctx, in.(*view.GetVMRequest))
	case "snapshot":
		return s.GetVMSnapshot(ctx, in.(*view.GetSnapshotRequest))
	case "create-snapshot":
		return s.CreateVMSnapshot(ctx, in.(*view.ChangeVMRequest))
	case "delete-snapshot":
		return s.DeleteVMSnapshot(ctx, in.(*view.ChangeVMRequest))
	case "preview-restore":
		return s.PreviewVMRestore(ctx, in.(*view.ChangeVMRequest))
	case "restore-snapshot":
		return s.RestoreVMSnapshot(ctx, in.(*view.ChangeVMRequest))
	case "console":
		return s.CreateVMConsoleSession(ctx, in.(*view.ChangeVMRequest))
	}
	return nil, kratoserrors.BadRequest("VM_INVALID_REQUEST", "unknown VM route")
}
