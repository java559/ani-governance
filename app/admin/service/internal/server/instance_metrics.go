package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func monitoringInvalidHTTP() error {
	return kratoserrors.BadRequest("MONITORING_INVALID_QUERY", "invalid monitoring query")
}

func decodeInstanceMetricsHTTP(r *http.Request, message *view.QueryInstanceMetricsRequest) error {
	if r == nil || r.Body == nil {
		return monitoringInvalidHTTP()
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	if err != nil {
		return monitoringInvalidHTTP()
	}
	if len(raw) > 65536 {
		return kratoserrors.New(413, "MONITORING_REQUEST_TOO_LARGE", "monitoring request too large")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || len(envelope) != 1 {
		return monitoringInvalidHTTP()
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(envelope["data"], &body) != nil || body == nil {
		return monitoringInvalidHTTP()
	}
	for field, value := range body {
		switch field {
		case "metric_codes", "start", "end", "step_seconds":
		default:
			return monitoringInvalidHTTP()
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return monitoringInvalidHTTP()
		}
	}
	if rawStep, present := body["step_seconds"]; present {
		var step uint32
		if json.Unmarshal(rawStep, &step) != nil {
			return monitoringInvalidHTTP()
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if decodeStrictJSONHTTP(r, message) != nil {
		return monitoringInvalidHTTP()
	}
	return nil
}

func RegisterInstanceMetricsHTTP(server *khttp.Server, s *service.InstanceService) {
	router := server.Route("/")
	router.POST(data.InstanceMetricsQueryPath, func(ctx khttp.Context) error {
		khttp.SetOperation(ctx, admin.OperationInstanceServiceQueryInstanceMetrics)
		request := new(view.QueryInstanceMetricsRequest)
		if err := decodeInstanceMetricsHTTP(ctx.Request(), request); err != nil {
			return err
		}
		if err := ctx.BindVars(request); err != nil {
			return monitoringInvalidHTTP()
		}
		out, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
			return s.QueryInstanceMetrics(call, in.(*view.QueryInstanceMetricsRequest))
		})(ctx, request)
		if err != nil {
			return instanceMetricsHTTPError(err)
		}
		return instanceMetricsResult(ctx, out.(proto.Message))
	})
	router.GET(data.InstanceMetricsLatestPath, func(ctx khttp.Context) error {
		khttp.SetOperation(ctx, admin.OperationInstanceServiceGetInstanceLatestMetrics)
		r := ctx.Request()
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path {
			return monitoringInvalidHTTP()
		}
		if r.Body != nil {
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1))
			if err != nil || len(raw) != 0 {
				return monitoringInvalidHTTP()
			}
		}
		request := new(view.GetInstanceRequest)
		if err := ctx.BindVars(request); err != nil {
			return monitoringInvalidHTTP()
		}
		out, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
			return s.GetInstanceLatestMetrics(call, in.(*view.GetInstanceRequest))
		})(ctx, request)
		if err != nil {
			return instanceMetricsHTTPError(err)
		}
		return instanceMetricsResult(ctx, out.(proto.Message))
	})
}

func instanceMetricsResult(ctx khttp.Context, message proto.Message) error {
	// Wrappers emit null for missing data while optional gap_reason stays absent
	// on valid points. This encoding is scoped to these two public endpoints.
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(message)
	if err != nil {
		return err
	}
	ctx.Response().Header().Set("Content-Type", "application/json")
	ctx.Response().WriteHeader(http.StatusOK)
	_, err = ctx.Response().Write(raw)
	return err
}

func instanceMetricsHTTPError(err error) error {
	if err == nil {
		return nil
	}
	var known *kratoserrors.Error
	if errors.As(err, &known) {
		return known
	}
	if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled {
		return kratoserrors.New(499, "INSTANCE_CANCELED", "instance request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded {
		return kratoserrors.New(504, "MONITORING_QUERY_TIMEOUT", "monitoring query timed out")
	}
	type mapping struct {
		code codes.Code
		http int
	}
	allowed := map[string]mapping{
		"MONITORING_INVALID_QUERY":              {codes.InvalidArgument, 400},
		"INSTANCE_METRIC_SCOPE_UNAVAILABLE":     {codes.FailedPrecondition, 409},
		"MONITORING_RESULT_TOO_LARGE":           {codes.ResourceExhausted, 422},
		"MONITORING_CONCURRENCY_LIMIT_EXCEEDED": {codes.ResourceExhausted, 429},
		"MONITORING_SOURCE_NOT_CONFIGURED":      {codes.Unavailable, 503},
		"MONITORING_SOURCE_UNAVAILABLE":         {codes.Unavailable, 503},
		"MONITORING_SOURCE_RESPONSE_INVALID":    {codes.DataLoss, 502},
		"MONITORING_QUERY_FAILED":               {codes.Internal, 500},
		"MONITORING_QUERY_TIMEOUT":              {codes.DeadlineExceeded, 504},
	}
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			if m, known := allowed[info.Reason]; known && m.code == status.Code(err) {
				return kratoserrors.New(m.http, info.Reason, "monitoring request failed")
			}
		}
	}
	switch status.Code(err) {
	case codes.Unavailable:
		return kratoserrors.ServiceUnavailable("MONITORING_SOURCE_UNAVAILABLE", "monitoring unavailable")
	case codes.ResourceExhausted, codes.DataLoss, codes.Internal:
		return kratoserrors.New(500, "MONITORING_QUERY_FAILED", "monitoring query failed")
	default:
		return instanceHTTPError(err)
	}
}
