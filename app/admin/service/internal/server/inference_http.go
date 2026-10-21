package server

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	nethttp "net/http"
	"net/url"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Strict decoding is required at the public boundary: internal attachment,
// tenant, plan and charge fields must be rejected rather than discarded by a
// permissive generated HTTP decoder. The normal authentication, tenant gate,
// Casbin and audit middleware still execute through ctx.Middleware.
func RegisterInferenceHTTPServer(server *http.Server, inference *service.InferenceService) {
	server.Route("/").GET("/api/v1/inference/services", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceServiceListInference)
		request := new(view.ListInferenceRequest)
		if err := ctx.BindQuery(request); err != nil {
			return err
		}
		out, err := ctx.Middleware(func(call context.Context, in interface{}) (interface{}, error) {
			return inference.ListInference(call, in.(*view.ListInferenceRequest))
		})(ctx, request)
		if err != nil {
			return err
		}
		return ctx.Result(nethttp.StatusOK, out)
	})
	server.Route("/").GET("/api/v1/inference/services/{resource_id}", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceServiceGetInference)
		request := new(view.GetInferenceRequest)
		if err := ctx.BindVars(request); err != nil {
			return err
		}
		out, err := ctx.Middleware(func(call context.Context, in interface{}) (interface{}, error) {
			return inference.GetInference(call, in.(*view.GetInferenceRequest))
		})(ctx, request)
		if err != nil {
			return err
		}
		return ctx.Result(nethttp.StatusOK, out)
	})
	server.Route("/").GET("/api/v1/inference/operations/{operation_id}", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceServiceGetInferenceOperation)
		request := new(view.GetInferenceOperationRequest)
		if err := ctx.BindVars(request); err != nil {
			return err
		}
		out, err := ctx.Middleware(func(call context.Context, in interface{}) (interface{}, error) {
			return inference.GetInferenceOperation(call, in.(*view.GetInferenceOperationRequest))
		})(ctx, request)
		if err != nil {
			return err
		}
		return ctx.Result(nethttp.StatusOK, out)
	})
	server.Route("/").POST(data.InferenceCreatePath, func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceServiceCreateInference)
		request := new(view.CreateInferenceRequest)
		if err := decodeInferenceHTTP(ctx.Request(), request); err != nil {
			return err
		}
		handler := ctx.Middleware(func(call context.Context, in interface{}) (interface{}, error) {
			return inference.CreateInference(call, in.(*view.CreateInferenceRequest))
		})
		out, err := handler(ctx, request)
		if err != nil {
			return err
		}
		return ctx.Result(nethttp.StatusAccepted, out)
	})
	server.Route("/").POST(data.InferenceDeletePath, func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceServiceDeleteInference)
		request := new(view.DeleteInferenceRequest)
		if err := decodeInferenceHTTP(ctx.Request(), request); err != nil {
			return err
		}
		resourceID := inferenceDeletePathResourceID(ctx.Vars())
		if resourceID == "" || request.Data == nil || (request.Data.ResourceId != "" && request.Data.ResourceId != resourceID) {
			return errors.BadRequest("INVALID_INFERENCE_REQUEST", "invalid inference resource")
		}
		request.Data.ResourceId = resourceID
		handler := ctx.Middleware(func(call context.Context, in interface{}) (interface{}, error) {
			return inference.DeleteInference(call, in.(*view.DeleteInferenceRequest))
		})
		out, err := handler(ctx, request)
		if err != nil {
			return err
		}
		return ctx.Result(nethttp.StatusAccepted, out)
	})
	server.Route("/").POST(data.InferenceUpdatePath, func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceServiceUpdateInference)
		request := new(view.UpdateInferenceRequest)
		if err := decodeInferenceHTTP(ctx.Request(), request); err != nil {
			return err
		}
		if err := bindInferenceUpdateResourceID(ctx, request); err != nil {
			return err
		}
		out, err := ctx.Middleware(func(call context.Context, in interface{}) (interface{}, error) {
			return inference.UpdateInference(call, in.(*view.UpdateInferenceRequest))
		})(ctx, request)
		if err != nil {
			return err
		}
		return ctx.Result(nethttp.StatusAccepted, out)
	})
	registerInferenceCommand := func(path string, operation string, call func(context.Context, *view.InferenceServiceCommandRequest) (*view.InferenceAcceptance, error)) {
		server.Route("/").POST(path, func(ctx http.Context) error {
			switch operation {
			case admin.OperationInferenceServiceStartInference:
				http.SetOperation(ctx, admin.OperationInferenceServiceStartInference)
			case admin.OperationInferenceServiceStopInference:
				http.SetOperation(ctx, admin.OperationInferenceServiceStopInference)
			case admin.OperationInferenceServiceRestartInference:
				http.SetOperation(ctx, admin.OperationInferenceServiceRestartInference)
			}
			request := new(view.InferenceServiceCommandRequest)
			if err := decodeInferenceHTTP(ctx.Request(), request); err != nil {
				return err
			}
			if err := bindInferenceCommandResourceID(ctx, request); err != nil {
				return err
			}
			out, err := ctx.Middleware(func(callCtx context.Context, in interface{}) (interface{}, error) {
				return call(callCtx, in.(*view.InferenceServiceCommandRequest))
			})(ctx, request)
			if err != nil {
				return err
			}
			return ctx.Result(nethttp.StatusAccepted, out)
		})
	}
	registerInferenceCommand(data.InferenceStartPath, admin.OperationInferenceServiceStartInference, inference.StartInference)
	registerInferenceCommand(data.InferenceStopPath, admin.OperationInferenceServiceStopInference, inference.StopInference)
	registerInferenceCommand(data.InferenceRestartPath, admin.OperationInferenceServiceRestartInference, inference.RestartInference)
	server.Route("/").GET(data.InferenceListOperationsPath, func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceServiceListInferenceOperations)
		request := new(view.ListInferenceOperationsRequest)
		if err := ctx.BindVars(request); err != nil {
			return err
		}
		if err := ctx.BindQuery(request); err != nil {
			return err
		}
		out, err := ctx.Middleware(func(call context.Context, in interface{}) (interface{}, error) {
			return inference.ListInferenceOperations(call, in.(*view.ListInferenceOperationsRequest))
		})(ctx, request)
		if err != nil {
			return err
		}
		return ctx.Result(nethttp.StatusOK, out)
	})
}

func bindInferenceUpdateResourceID(ctx http.Context, request *view.UpdateInferenceRequest) error {
	resourceID := inferenceDeletePathResourceID(ctx.Vars())
	if resourceID == "" || request == nil || request.Data == nil || (request.Data.ResourceId != "" && request.Data.ResourceId != resourceID) {
		return errors.BadRequest("INVALID_INFERENCE_REQUEST", "invalid inference resource")
	}
	request.Data.ResourceId = resourceID
	return nil
}

func bindInferenceCommandResourceID(ctx http.Context, request *view.InferenceServiceCommandRequest) error {
	resourceID := inferenceDeletePathResourceID(ctx.Vars())
	if resourceID == "" || request == nil || request.Data == nil || (request.Data.ResourceId != "" && request.Data.ResourceId != resourceID) {
		return errors.BadRequest("INVALID_INFERENCE_REQUEST", "invalid inference resource")
	}
	request.Data.ResourceId = resourceID
	return nil
}

func inferenceDeletePathResourceID(vars url.Values) string {
	resourceID := vars.Get("data.resource_id")
	if resourceID == "" {
		resourceID = vars.Get("resource_id")
	}
	return strings.TrimSpace(resourceID)
}

func decodeInferenceHTTP(request *nethttp.Request, message proto.Message) error {
	invalid := errors.BadRequest("INVALID_INFERENCE_REQUEST", "invalid inference request")
	if request == nil || request.Body == nil || request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.RawPath != "" && request.URL.RawPath != request.URL.Path {
		return invalid
	}
	media, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return invalid
	}
	for _, header := range []string{"X-Ani-Tenant-Id", "X-Tenant-Id", "X-Ani-Actor", "X-Ani-Operator", "X-Resource-Tenant-Id"} {
		if len(request.Header.Values(header)) != 0 {
			return invalid
		}
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, 65537))
	if err != nil || len(raw) == 0 || len(raw) > 65536 {
		return invalid
	}
	switch message.(type) {
	case *view.CreateInferenceRequest, *view.UpdateInferenceRequest:
		var envelope struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return invalid
		}
		for _, key := range []string{"cpu", "memory", "gpu"} {
			if _, exists := envelope.Data[key]; exists {
				return invalid
			}
		}
		if _, update := message.(*view.UpdateInferenceRequest); update {
			for _, key := range []string{"replicas", "runtime_mode", "runtimeMode", "worker_replicas", "workerReplicas"} {
				if _, exists := envelope.Data[key]; exists {
					return invalid
				}
			}
		}
	}
	if err = (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, message); err != nil {
		return invalid
	}
	return nil
}
