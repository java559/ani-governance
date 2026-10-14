package server

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
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
			case "limit":
				in.Limit, err = modelDevQueryPositiveBound(values[0], 100)
			case "cursor":
				if len(values[0]) > 2048 {
					return invalidModelDevQueryHTTP()
				}
				in.Cursor = values[0]
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
		for name, values := range query {
			switch name {
			case "tail_lines":
				in.TailLines, err = modelDevQueryPositiveBound(values[0], 1000)
			case "max_bytes":
				in.MaxBytes, err = modelDevQueryPositiveBound(values[0], 65536)
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
			case "limit":
				in.Limit, err = modelDevQueryPositiveBound(values[0], 100)
			case "cursor":
				if len(values[0]) > 2048 {
					return invalidModelDevQueryHTTP()
				}
				in.Cursor = values[0]
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
