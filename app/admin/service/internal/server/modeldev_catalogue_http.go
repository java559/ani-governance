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
	route.GET(data.ModelDevListImplementationsPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceListImplementations)
		size, pageSize, _, err := modelDevCatalogueSelectors(ctx.Request(), false)
		if err != nil {
			return err
		}
		in := &modeldevv1.ListImplementationsRequest{Page: size, PageSize: pageSize}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.ListImplementations(c, v.(*modeldevv1.ListImplementationsRequest))
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
		size, pageSize, state, err := modelDevCatalogueSelectors(ctx.Request(), true)
		if err != nil {
			return err
		}
		in := &modeldevv1.ListInputVersionsRequest{Page: size, PageSize: pageSize, State: state}
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

func modelDevCatalogueSelectors(request *http.Request, allowState bool) (*uint32, *uint32, *string, error) {
	query, err := modelDevBoundedQuery(request)
	if err != nil {
		return nil, nil, nil, err
	}
	var page, pageSize *uint32
	var state *string
	for name, values := range query {
		switch name {
		case "page":
			page, err = modelDevPagePointer(values[0])
		case "pageSize":
			pageSize, err = modelDevPageSizePointer(values[0])
		case "state":
			if !allowState {
				return nil, nil, nil, invalidModelDevQueryHTTP()
			}
			value := values[0]
			switch value {
			case "FIXING", "VALIDATING", "READY", "FAILED":
				state = &value
			default:
				return nil, nil, nil, invalidModelDevQueryHTTP()
			}
		default:
			return nil, nil, nil, invalidModelDevQueryHTTP()
		}
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return page, pageSize, state, nil
}
