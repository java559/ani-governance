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
		selectors, err := modelDevCatalogueSelectors(ctx.Request(), map[string]bool{"kind": true})
		if err != nil {
			return err
		}
		in := &modeldevv1.ListImplementationsRequest{Page: selectors.page, PageSize: selectors.pageSize, Kind: selectors.kind}
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
		selectors, err := modelDevCatalogueSelectors(ctx.Request(), map[string]bool{"state": true, "format": true})
		if err != nil {
			return err
		}
		in := &modeldevv1.ListInputVersionsRequest{Page: selectors.page, PageSize: selectors.pageSize, State: selectors.state, Format: selectors.format}
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

// modelDevCatalogueSelectors parses the shared pagination window plus the
// filters the route explicitly allows. Any other parameter, a repeated value or
// an unsupported value is rejected rather than ignored.
func modelDevCatalogueSelectors(request *http.Request, allowed map[string]bool) (modelDevCatalogueSelector, error) {
	var selector modelDevCatalogueSelector
	query, err := modelDevBoundedQuery(request)
	if err != nil {
		return selector, err
	}
	for name, values := range query {
		switch name {
		case "page":
			selector.page, err = modelDevPagePointer(values[0])
		case "pageSize":
			selector.pageSize, err = modelDevPageSizePointer(values[0])
		case "state":
			if !allowed["state"] {
				return selector, invalidModelDevQueryHTTP()
			}
			value := values[0]
			switch value {
			case "FIXING", "VALIDATING", "READY", "FAILED":
				selector.state = &value
			default:
				return selector, invalidModelDevQueryHTTP()
			}
		case "kind":
			if !allowed["kind"] {
				return selector, invalidModelDevQueryHTTP()
			}
			value := values[0]
			switch value {
			case "GENERAL_TRAINING", "FINETUNING":
				selector.kind = &value
			default:
				return selector, invalidModelDevQueryHTTP()
			}
		case "format":
			if !allowed["format"] {
				return selector, invalidModelDevQueryHTTP()
			}
			value := values[0]
			switch value {
			case "CSV", "JSON":
				selector.format = &value
			default:
				return selector, invalidModelDevQueryHTTP()
			}
		default:
			return selector, invalidModelDevQueryHTTP()
		}
		if err != nil {
			return selector, err
		}
	}
	return selector, nil
}

// modelDevCatalogueSelector carries the optional list filters. A nil field
// means the caller did not ask for that filter.
type modelDevCatalogueSelector struct {
	page     *uint32
	pageSize *uint32
	state    *string
	kind     *string
	format   *string
}
