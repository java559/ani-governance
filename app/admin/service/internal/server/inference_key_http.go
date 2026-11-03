package server

import (
	"context"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/proto"
)

// The generated descriptor remains the API catalog contract. These handlers
// enforce strict JSON/query decoding while retaining the normal auth, Casbin,
// subscription and audit middleware on every operation.
func registerInferenceKeyHTTP(server *http.Server, inference *service.InferenceService) {
	r := server.Route("/")
	r.POST("/api/v1/inference-keys", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceCreateInferenceKey)
		in := new(view.CreateInferenceKeyRequest)
		if err := decodeInferenceKeyWrite(ctx, in); err != nil {
			return err
		}
		out, err := ctx.Middleware(func(call context.Context, request interface{}) (interface{}, error) {
			return inference.CreateInferenceKey(call, request.(*view.CreateInferenceKeyRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
	r.GET("/api/v1/inference-keys", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceListInferenceKeys)
		if err := validateInferenceKeyRead(ctx, true); err != nil {
			return err
		}
		values, err := url.ParseQuery(ctx.Request().URL.RawQuery)
		if err != nil {
			return view.ErrorInvalidInferenceKeyRequest("invalid pagination")
		}
		in := new(view.ListInferenceKeysRequest)
		for key, vs := range values {
			if (key != "page" && key != "pageSize" && key != "served_model_name") || len(vs) != 1 {
				return view.ErrorInvalidInferenceKeyRequest("only single page, pageSize and served_model_name parameters are accepted")
			}
			if key == "served_model_name" {
				model := strings.TrimSpace(vs[0])
				if model == "" || strings.ContainsRune(model, '\x00') {
					return view.ErrorInvalidInferenceKeyRequest("served_model_name must be nonempty")
				}
				in.ServedModelName = &model
				continue
			}
			n, err := strconv.ParseInt(vs[0], 10, 32)
			if err != nil || n < 1 || n > 100 || strconv.FormatInt(n, 10) != vs[0] {
				return view.ErrorInvalidInferenceKeyRequest("page and pageSize must be canonical integers within 1..100")
			}
			value := int32(n)
			if key == "page" {
				in.Page = &value
			} else {
				in.PageSize = &value
			}
		}
		out, err := ctx.Middleware(func(call context.Context, request interface{}) (interface{}, error) {
			return inference.ListInferenceKeys(call, request.(*view.ListInferenceKeysRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
	r.GET("/api/v1/inference-keys/{key_id}", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceGetInferenceKey)
		if err := validateInferenceKeyRead(ctx, false); err != nil {
			return err
		}
		in := &view.GetInferenceKeyRequest{KeyId: ctx.Vars().Get("key_id")}
		out, err := ctx.Middleware(func(call context.Context, request interface{}) (interface{}, error) {
			return inference.GetInferenceKey(call, request.(*view.GetInferenceKeyRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
	r.DELETE("/api/v1/inference-keys/{key_id}", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceRevokeInferenceKey)
		if err := validateInferenceKeyRead(ctx, false); err != nil {
			return err
		}
		in := &view.RevokeInferenceKeyRequest{KeyId: ctx.Vars().Get("key_id")}
		out, err := ctx.Middleware(func(call context.Context, request interface{}) (interface{}, error) {
			return inference.RevokeInferenceKey(call, request.(*view.RevokeInferenceKeyRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
	r.POST("/api/v1/inference-keys/{key_id}:rotate", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceRotateInferenceKey)
		in := new(view.RotateInferenceKeyRequest)
		if err := decodeInferenceKeyWrite(ctx, in); err != nil {
			return err
		}
		id := ctx.Vars().Get("key_id")
		if in.KeyId != "" && in.KeyId != id {
			return view.ErrorInvalidInferenceKeyRequest("key_id must match the path")
		}
		in.KeyId = id
		out, err := ctx.Middleware(func(call context.Context, request interface{}) (interface{}, error) {
			return inference.RotateInferenceKey(call, request.(*view.RotateInferenceKeyRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
}

func inferenceKeyNoStore(ctx http.Context) {
	ctx.Response().Header().Set("Cache-Control", "no-store")
	ctx.Response().Header().Set("Pragma", "no-cache")
}

func decodeInferenceKeyWrite(ctx http.Context, in proto.Message) error {
	inferenceKeyNoStore(ctx)
	if err := decodeInferenceHTTP(ctx.Request(), in); err != nil {
		return view.ErrorInvalidInferenceKeyRequest("invalid inference key request").WithCause(err)
	}
	return nil
}

func validateInferenceKeyRead(ctx http.Context, pagination bool) error {
	inferenceKeyNoStore(ctx)
	r := ctx.Request()
	invalid := view.ErrorInvalidInferenceKeyRequest("unexpected body, query or identity headers")
	if r.URL.RawPath != "" || r.URL.ForceQuery || (!pagination && r.URL.RawQuery != "") || r.ContentLength > 0 || len(r.TransferEncoding) != 0 {
		return invalid
	}
	for _, header := range []string{"X-Ani-Tenant-Id", "X-Tenant-Id", "X-Ani-Actor", "X-Ani-Operator", "X-Resource-Tenant-Id"} {
		if len(r.Header.Values(header)) > 0 {
			return invalid
		}
	}
	if r.Body != nil {
		var b [1]byte
		n, err := r.Body.Read(b[:])
		if n > 0 || (err != nil && err != io.EOF) {
			return invalid
		}
	}
	return nil
}
