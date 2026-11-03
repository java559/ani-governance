package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"io"
	"mime"
	"net/http"
	"strings"
)

func invalidModelDevEnvironment() error {
	return errors.BadRequest("INVALID_ARGUMENT", "invalid environment request")
}
func stripModelDevEnvironmentIdentity(r *http.Request) {
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-ani-") {
			r.Header.Del(name)
		}
	}
}
func decodeModelDevEnvironmentEnsure(r *http.Request) (string, error) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.Header.Values("Content-Type")) != 1 {
		return "", invalidModelDevEnvironment()
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return "", invalidModelDevEnvironment()
	}
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", invalidModelDevEnvironment()
	}
	id, err := uuid.Parse(values[0])
	if err != nil || id == uuid.Nil || id.String() != values[0] {
		return "", invalidModelDevEnvironment()
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if err != nil || len(raw) > 1024 {
		return "", invalidModelDevEnvironment()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	open, err := d.Token()
	if err != nil || open != json.Delim('{') || d.More() {
		return "", invalidModelDevEnvironment()
	}
	close, err := d.Token()
	if err != nil || close != json.Delim('}') {
		return "", invalidModelDevEnvironment()
	}
	if _, err = d.Token(); err != io.EOF {
		return "", invalidModelDevEnvironment()
	}
	return values[0], nil
}
func registerModelDevEnvironmentHTTP(server *khttp.Server, modeldev *service.ModelDevService) {
	server.Route("/").POST(data.ModelDevEnsureEnvironmentPath, func(ctx khttp.Context) error {
		r := ctx.Request()
		modelDevQueryHeaders(ctx)
		stripModelDevEnvironmentIdentity(r)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceEnsureTenantExecutionEnvironment)
		operation, err := decodeModelDevEnvironmentEnsure(r)
		if err != nil {
			return err
		}
		handler := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return modeldev.EnsureTenantExecutionEnvironment(c, v.(string))
		})
		out, err := handler(ctx, operation)
		if err != nil {
			return err
		}
		reply, ok := out.(*modeldevv1.EnsureTenantExecutionEnvironmentResponse)
		if !ok || reply == nil || reply.Environment == nil {
			return errors.ServiceUnavailable("UPSTREAM_UNAVAILABLE", "environment service unavailable")
		}
		code := http.StatusOK
		if reply.Environment.State == "PROVISIONING" {
			code = http.StatusAccepted
			ctx.Response().Header().Set("Retry-After", "2")
		}
		return writeModelDevEnvironmentHTTP(ctx, code, reply)
	})
	server.Route("/").GET(data.ModelDevGetEnvironmentPath, func(ctx khttp.Context) error {
		r := ctx.Request()
		modelDevQueryHeaders(ctx)
		stripModelDevEnvironmentIdentity(r)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceGetTenantExecutionEnvironment)
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(raw) != 0 || r.URL.RawQuery != "" || r.URL.ForceQuery {
			return invalidModelDevEnvironment()
		}
		handler := ctx.Middleware(func(c context.Context, _ interface{}) (interface{}, error) {
			return modeldev.GetTenantExecutionEnvironment(c)
		})
		out, err := handler(ctx, &modeldevv1.GetTenantExecutionEnvironmentRequest{})
		if err != nil {
			return err
		}
		reply, ok := out.(*modeldevv1.GetTenantExecutionEnvironmentResponse)
		if !ok || reply == nil || reply.Environment == nil {
			return errors.ServiceUnavailable("UPSTREAM_UNAVAILABLE", "environment service unavailable")
		}
		return writeModelDevEnvironmentHTTP(ctx, http.StatusOK, reply)
	})
}

// Keep fixed scalar observations (false, zero and empty reason) while omitting
// fields whose presence has not yet been confirmed, including timestamps.
func encodeModelDevEnvironment(value proto.Message) ([]byte, error) {
	return (protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}).Marshal(value)
}
func writeModelDevEnvironmentHTTP(ctx khttp.Context, code int, value proto.Message) error {
	raw, err := encodeModelDevEnvironment(value)
	if err != nil {
		return errors.ServiceUnavailable("UPSTREAM_UNAVAILABLE", "environment service unavailable")
	}
	ctx.Response().Header().Set("Content-Type", "application/json")
	ctx.Response().WriteHeader(code)
	_, err = ctx.Response().Write(raw)
	return err
}
