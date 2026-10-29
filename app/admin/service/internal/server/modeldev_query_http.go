package server

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func registerModelDevQueryHTTP(server *khttp.Server, modeldev *service.ModelDevService) {
	route := server.Route("/")
	route.GET(data.ModelDevListExecutionsPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceListExecutions)
		var in modeldevv1.ListExecutionsRequest
		query, err := modelDevBoundedQuery(ctx.Request())
		if err != nil {
			return err
		}
		for name, values := range query {
			switch name {
			case "page":
				in.Page, err = modelDevPagePointer(values[0])
			case "pageSize":
				in.PageSize, err = modelDevPageSizePointer(values[0])
			case "compute_state":
				in.ComputeState, err = modelDevStatePointer(values[0])
			case "delivery_state":
				in.DeliveryState, err = modelDevStatePointer(values[0])
			case "close_state":
				in.CloseState, err = modelDevStatePointer(values[0])
			case "name":
				in.Name, err = modelDevNamePointer(values[0])
			case "kind":
				in.Kind, err = modelDevStatePointer(values[0])
			default:
				return invalidModelDevQueryHTTP()
			}
			if err != nil {
				return err
			}
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.ListExecutions(c, v.(*modeldevv1.ListExecutionsRequest))
		})
		out, err := handler(ctx, &in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})
	route.GET(data.ModelDevGetExecutionLogsPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceGetExecutionLogs)
		var in modeldevv1.GetExecutionLogsRequest
		if ctx.BindVars(&in) != nil {
			return invalidModelDevQueryHTTP()
		}
		query, err := modelDevBoundedQuery(ctx.Request())
		if err != nil {
			return err
		}
		// podUid is the documented spelling; pod_uid is the current slice's
		// canonical one. Both name the same selector and must not be combined.
		_, hasSnakePod := query["pod_uid"]
		_, hasCamelPod := query["podUid"]
		if hasSnakePod && hasCamelPod {
			return invalidModelDevQueryHTTP()
		}
		for name, values := range query {
			switch name {
			case "tail_lines":
				in.TailLines, err = modelDevQueryPositiveBound(values[0], 1000)
			case "max_bytes":
				in.MaxBytes, err = modelDevQueryPositiveBound(values[0], 65536)
			case "logId":
				in.LogId, err = modelDevQueryUUIDPointer(values[0])
			case "pod_uid", "podUid":
				in.PodUid, err = modelDevQueryUUIDPointer(values[0])
			case "container":
				in.Container, err = modelDevQueryContainerPointer(values[0])
			case "previous":
				in.Previous, err = modelDevQueryBoolPointer(values[0])
			default:
				return invalidModelDevQueryHTTP()
			}
			if err != nil {
				return err
			}
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.GetExecutionLogs(c, v.(*modeldevv1.GetExecutionLogsRequest))
		})
		out, err := handler(ctx, &in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})
	route.GET(data.ModelDevListExecutionPodsPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceListExecutionPods)
		var in modeldevv1.ListExecutionPodsRequest
		if ctx.BindVars(&in) != nil {
			return invalidModelDevQueryHTTP()
		}
		query, err := modelDevBoundedQuery(ctx.Request())
		if err != nil {
			return err
		}
		for name, values := range query {
			switch name {
			case "layer":
				layer, valid := data.ParseWorkloadLayer(values[0])
				if !valid {
					return invalidModelDevQueryHTTP()
				}
				value := string(layer)
				in.Layer = &value
			case "includeRecycled":
				in.IncludeRecycled, err = modelDevQueryBoolPointer(values[0])
			default:
				return invalidModelDevQueryHTTP()
			}
			if err != nil {
				return err
			}
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.ListExecutionPods(c, v.(*modeldevv1.ListExecutionPodsRequest))
		})
		out, err := handler(ctx, &in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})
	route.GET(data.ModelDevExecutionMetricsPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceQueryExecutionMetrics)
		var in modeldevv1.QueryExecutionMetricsRequest
		if ctx.BindVars(&in) != nil || in.ExecutionId == "" {
			return invalidModelDevQueryHTTP()
		}
		query, err := modelDevBoundedQuery(ctx.Request())
		if err != nil {
			return err
		}
		for name, values := range query {
			switch name {
			case "pod_uid":
				in.PodUid = values[0]
			case "container":
				in.Container = values[0]
			case "start":
				in.Start = values[0]
			case "end":
				in.End = values[0]
			case "step_seconds":
				in.StepSeconds, err = modelDevQueryStepPointer(values[0])
			case "metric_codes":
				in.MetricCodes = strings.Split(values[0], ",")
			default:
				return invalidModelDevQueryHTTP()
			}
			if err != nil {
				return err
			}
		}
		out, err := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.QueryExecutionMetrics(c, v.(*modeldevv1.QueryExecutionMetricsRequest))
		})(ctx, &in)
		if err != nil {
			return err
		}
		// Every unpopulated field is emitted: a missing point value must stay
		// absent while gap_reason and the fixed envelope stay visible.
		raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(out.(proto.Message))
		if err != nil {
			return err
		}
		ctx.Response().Header().Set("Content-Type", "application/json")
		ctx.Response().WriteHeader(http.StatusOK)
		_, err = ctx.Response().Write(raw)
		return err
	})
	route.GET(data.ModelDevListExecutionEventsPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceListExecutionEvents)
		var in modeldevv1.ListExecutionEventsRequest
		if ctx.BindVars(&in) != nil {
			return invalidModelDevQueryHTTP()
		}
		query, err := modelDevBoundedQuery(ctx.Request())
		if err != nil {
			return err
		}
		for name, values := range query {
			switch name {
			case "scope":
				scope, valid := data.ParseEventScope(values[0])
				if !valid {
					return invalidModelDevQueryHTTP()
				}
				value := string(scope)
				in.Scope = &value
			default:
				return invalidModelDevQueryHTTP()
			}
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.ListExecutionEvents(c, v.(*modeldevv1.ListExecutionEventsRequest))
		})
		out, err := handler(ctx, &in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})
	route.GET(data.ModelDevGetExecutionPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceGetExecution)
		var in modeldevv1.GetExecutionRequest
		if ctx.BindVars(&in) != nil || ctx.Request().URL.RawQuery != "" || ctx.Request().URL.ForceQuery {
			return invalidModelDevQueryHTTP()
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.GetExecution(c, v.(*modeldevv1.GetExecutionRequest))
		})
		out, err := handler(ctx, &in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out.(*modeldevv1.GetExecutionResponse).GetExecution())
	})
	route.GET(data.ModelDevListArtifactsPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceListExecutionArtifacts)
		var in modeldevv1.ListExecutionArtifactsRequest
		if ctx.BindVars(&in) != nil {
			return invalidModelDevQueryHTTP()
		}
		query, err := modelDevBoundedQuery(ctx.Request())
		if err != nil {
			return err
		}
		for name, values := range query {
			switch name {
			case "page":
				in.Page, err = modelDevPagePointer(values[0])
			case "pageSize":
				in.PageSize, err = modelDevPageSizePointer(values[0])
			default:
				return invalidModelDevQueryHTTP()
			}
			if err != nil {
				return err
			}
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.ListExecutionArtifacts(c, v.(*modeldevv1.ListExecutionArtifactsRequest))
		})
		out, err := handler(ctx, &in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})
	route.GET(data.ModelDevDownloadArtifactPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceAuthorizeArtifactDownload)
		var in modeldevv1.AuthorizeArtifactDownloadRequest
		if ctx.BindVars(&in) != nil || ctx.Request().URL.RawQuery != "" || ctx.Request().URL.ForceQuery {
			return invalidModelDevQueryHTTP()
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.AuthorizeArtifactDownload(c, v.(*modeldevv1.AuthorizeArtifactDownloadRequest))
		})
		out, err := handler(ctx, &in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})
}

func modelDevBoundedQuery(request *http.Request) (url.Values, error) {
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 || request.URL.ForceQuery {
		return nil, invalidModelDevQueryHTTP()
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return nil, invalidModelDevQueryHTTP()
	}
	for _, values := range query {
		if len(values) != 1 {
			return nil, invalidModelDevQueryHTTP()
		}
	}
	return query, nil
}

func modelDevQueryPositiveBound(value string, maximum uint32) (uint32, error) {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n == 0 || n > uint64(maximum) || value != strconv.FormatUint(n, 10) {
		return 0, invalidModelDevQueryHTTP()
	}
	return uint32(n), nil
}

// modelDevPagePointer parses a single canonical decimal page number in
// 1..4294967295, mirroring the VPC page contract. A missing parameter stays nil
// so the default is applied independently downstream.
func modelDevPagePointer(value string) (*uint32, error) {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n == 0 || value != strconv.FormatUint(n, 10) {
		return nil, invalidModelDevQueryHTTP()
	}
	page := uint32(n)
	return &page, nil
}

// modelDevPageSizePointer parses a canonical decimal page size in 1..100. An
// explicit zero or an out-of-range value is rejected rather than corrected.
func modelDevPageSizePointer(value string) (*uint32, error) {
	n, err := modelDevQueryPositiveBound(value, 100)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// modelDevStatePointer accepts one upper-case enum member name without its
// prefix, matching the vocabulary the list response returns. Validation of the
// member itself happens in the service, where the owner enum is available.
func modelDevStatePointer(value string) (*string, error) {
	if value == "" || len(value) > 48 {
		return nil, invalidModelDevQueryHTTP()
	}
	for _, character := range value {
		if !(character >= 'A' && character <= 'Z' || character == '_') {
			return nil, invalidModelDevQueryHTTP()
		}
	}
	parsed := value
	return &parsed, nil
}

// modelDevNamePointer accepts one name substring. The admitted name is at most
// 80 printable runes, so a longer or control-bearing value cannot match any
// stored row and is rejected rather than answered with an empty page.
func modelDevNamePointer(value string) (*string, error) {
	if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 80 {
		return nil, invalidModelDevQueryHTTP()
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return nil, invalidModelDevQueryHTTP()
		}
	}
	parsed := value
	return &parsed, nil
}

// modelDevQueryUUIDPointer accepts one canonical UUID query value.
func modelDevQueryUUIDPointer(value string) (*string, error) {
	parsed := value
	if !modelDevQueryCanonicalUUID(parsed) {
		return nil, invalidModelDevQueryHTTP()
	}
	return &parsed, nil
}

// modelDevQueryContainerPointer accepts one Kubernetes container name.
func modelDevQueryContainerPointer(value string) (*string, error) {
	if value == "" || len(value) > 63 {
		return nil, invalidModelDevQueryHTTP()
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '.') {
			return nil, invalidModelDevQueryHTTP()
		}
	}
	parsed := value
	return &parsed, nil
}

// modelDevQueryBoolPointer accepts the exact literals `true` and `false`.
func modelDevQueryBoolPointer(value string) (*bool, error) {
	if value != "true" && value != "false" {
		return nil, invalidModelDevQueryHTTP()
	}
	parsed := value == "true"
	return &parsed, nil
}

// modelDevQueryStepPointer parses a canonical decimal step; the service applies
// the 30s..1h and multiple-of-30 bounds so both layers agree on the contract.
func modelDevQueryStepPointer(value string) (*uint32, error) {
	n, err := modelDevQueryPositiveBound(value, 3600)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func modelDevQueryCanonicalUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func modelDevQueryHeaders(ctx khttp.Context) {
	ctx.Response().Header().Set("Cache-Control", "no-store")
	ctx.Response().Header().Set("Pragma", "no-cache")
	ctx.Response().Header().Set("Referrer-Policy", "no-referrer")
	// Delegation is minted after current database authorization, never accepted
	// from public caller headers, including a valid authenticated caller.
	for _, name := range []string{"x-ani-authorized-method", "x-ani-data-scope", "x-ani-tenant-id", "x-ani-actor", "x-ani-request-id"} {
		ctx.Request().Header.Del(name)
	}
}
func invalidModelDevQueryHTTP() error {
	return errors.BadRequest("INVALID_MODELDEV_QUERY", "invalid modeldev query")
}
