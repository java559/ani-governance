package server

import (
	"context"
	"net/http"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
)

func registerModelDevCatalogueHTTP(server *khttp.Server, modeldev *service.ModelDevService) {
	route := server.Route("/")
	route.GET(data.ModelDevListPresetsPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceListPresets)
		size, token, _, err := modelDevCatalogueSelectors(ctx.Request(), false)
		if err != nil {
			return err
		}
		in := &modeldevv1.ListPresetsRequest{Limit: size, Cursor: token}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.ListPresets(c, v.(*modeldevv1.ListPresetsRequest))
		})
		out, err := handler(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})
	route.GET(data.ModelDevListInputVersionsPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceListInputVersions)
		size, token, state, err := modelDevCatalogueSelectors(ctx.Request(), true)
		if err != nil {
			return err
		}
		in := &modeldevv1.ListInputVersionsRequest{Limit: size, Cursor: token, State: state}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.ListInputVersions(c, v.(*modeldevv1.ListInputVersionsRequest))
		})
		out, err := handler(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})
	route.GET(data.ModelDevGetInputVersionPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceGetInputVersion)
		var in modeldevv1.GetInputVersionRequest
		if ctx.BindVars(&in) != nil || ctx.Request().URL.RawQuery != "" || ctx.Request().URL.ForceQuery {
			return invalidModelDevQueryHTTP()
		}
		if _, err := modelDevBoundedQuery(ctx.Request()); err != nil {
			return err
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.GetInputVersion(c, v.(*modeldevv1.GetInputVersionRequest))
		})
		out, err := handler(ctx, &in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out.(*modeldevv1.GetInputVersionResponse).GetInputVersion())
	})
}

func modelDevCatalogueSelectors(request *http.Request, allowState bool) (uint32, string, *string, error) {
	query, err := modelDevBoundedQuery(request)
	if err != nil {
		return 0, "", nil, err
	}
	var size uint32
	var token string
	var state *string
	for name, values := range query {
		switch name {
		case "limit":
			size, err = modelDevQueryPositiveBound(values[0], 100)
		case "cursor":
			if len(values[0]) > 2048 {
				return 0, "", nil, invalidModelDevQueryHTTP()
			}
			token = values[0]
		case "state":
			if !allowState {
				return 0, "", nil, invalidModelDevQueryHTTP()
			}
			value := values[0]
			switch value {
			case "FIXING", "VALIDATING", "READY", "FAILED":
				state = &value
			default:
				return 0, "", nil, invalidModelDevQueryHTTP()
			}
		default:
			return 0, "", nil, invalidModelDevQueryHTTP()
		}
		if err != nil {
			return 0, "", nil, err
		}
	}
	return size, token, state, nil
}
