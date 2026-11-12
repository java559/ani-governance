package server

import (
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/transport/sse"

	sseServer "go-wind-admin/pkg/localdeps/kratos-transport/transport/sse"

	"go-wind-admin/app/admin/service/internal/service"
)

// NewSseServer creates a new SSE server.
func NewSseServer(
	ctx *bootstrap.Context,
	internalMessageService *service.InternalMessageService,
) *sseServer.Server {
	cfg := ctx.GetConfig()

	if cfg == nil || cfg.Server == nil || cfg.Server.Sse == nil {
		return nil
	}

	srv := sse.NewSseServer(cfg.Server.Sse,
		sseServer.WithSubscriberFunction(internalMessageService.HandleSubscribe),
		sseServer.WithAuthorizeFunc(internalMessageService.HandleAuthorize),
	)

	internalMessageService.RegisterInternalMessagePublisher(srv)

	// The SSE listener is an independent HTTP entry: wrap its existing handler
	// with the same outermost observability filter so stream start/finish/failure
	// and X-Trace-ID are recorded. The handler itself is not replaced and no new
	// listener is added.
	if srv != nil && srv.Server != nil {
		srv.Server.Handler = httpObservabilityFilter(ctx.GetLogger())(srv.Server.Handler)
	}

	//srv.CreateStream("test")

	return srv
}
