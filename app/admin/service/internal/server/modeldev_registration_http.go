package server

import (
	"context"
	"io"
	"net/http"

	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
)

func registerModelDevRegistrationHTTP(server *khttp.Server, modeldev *service.ModelDevService) {
	server.Route("/").POST(data.ModelDevResumeModelRegistrationPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceResumeModelRegistration)
		var in modeldevv1.ResumeModelRegistrationRequest
		body, err := io.ReadAll(io.LimitReader(ctx.Request().Body, 1))
		if err != nil || len(body) != 0 || ctx.BindVars(&in) != nil || ctx.Request().URL.RawQuery != "" || ctx.Request().URL.ForceQuery {
			return errors.BadRequest("INVALID_MODELDEV_REGISTRATION", "invalid model registration request")
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.ResumeModelRegistration(c, v.(*modeldevv1.ResumeModelRegistrationRequest))
		})
		out, err := handler(ctx, &in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out.(*modeldevv1.GetExecutionResponse).Execution)
	})
}
