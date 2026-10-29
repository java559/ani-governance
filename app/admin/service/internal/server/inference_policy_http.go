package server

import (
	"context"
	"net/url"
	"strconv"

	"github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
)

func registerInferencePolicyHTTP(server *http.Server, inference *service.InferenceService) {
	r := server.Route("/")
	r.POST("/api/v1/inference-policies", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceCreateInferencePolicy)
		in := new(view.CreateInferencePolicyRequest)
		if err := decodeInferenceKeyWrite(ctx, in); err != nil {
			return err
		}
		out, err := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return inference.CreateInferencePolicy(c, v.(*view.CreateInferencePolicyRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
	r.GET("/api/v1/inference-policies", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceListInferencePolicies)
		if err := validateInferenceKeyRead(ctx, true); err != nil {
			return err
		}
		values, err := url.ParseQuery(ctx.Request().URL.RawQuery)
		if err != nil {
			return view.ErrorInvalidInferenceKeyRequest("invalid pagination")
		}
		in := new(view.ListInferencePoliciesRequest)
		for key, vs := range values {
			if (key != "page" && key != "pageSize") || len(vs) != 1 {
				return view.ErrorInvalidInferenceKeyRequest("invalid pagination")
			}
			n, err := strconv.ParseInt(vs[0], 10, 32)
			if err != nil || n < 1 || n > 100 || strconv.FormatInt(n, 10) != vs[0] {
				return view.ErrorInvalidInferenceKeyRequest("invalid pagination")
			}
			value := int32(n)
			if key == "page" {
				in.Page = &value
			} else {
				in.PageSize = &value
			}
		}
		out, err := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return inference.ListInferencePolicies(c, v.(*view.ListInferencePoliciesRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
	r.GET("/api/v1/inference-policies/{policy_id}", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceGetInferencePolicy)
		if err := validateInferenceKeyRead(ctx, false); err != nil {
			return err
		}
		in := &view.GetInferencePolicyRequest{PolicyId: ctx.Vars().Get("policy_id")}
		out, err := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return inference.GetInferencePolicy(c, v.(*view.GetInferencePolicyRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
	r.PUT("/api/v1/inference-policies/{policy_id}", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceUpdateInferencePolicy)
		in := new(view.UpdateInferencePolicyRequest)
		if err := decodeInferenceKeyWrite(ctx, in); err != nil {
			return err
		}
		id := ctx.Vars().Get("policy_id")
		if in.PolicyId != "" && in.PolicyId != id {
			return view.ErrorInvalidInferenceKeyRequest("policy_id must match path")
		}
		in.PolicyId = id
		out, err := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return inference.UpdateInferencePolicy(c, v.(*view.UpdateInferencePolicyRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
	r.DELETE("/api/v1/inference-policies/{policy_id}", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceDeleteInferencePolicy)
		if err := validateInferenceKeyRead(ctx, false); err != nil {
			return err
		}
		in := &view.DeleteInferencePolicyRequest{PolicyId: ctx.Vars().Get("policy_id")}
		out, err := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return inference.DeleteInferencePolicy(c, v.(*view.DeleteInferencePolicyRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
	r.POST("/api/v1/inference-keys/{key_id}:bind-policy", func(ctx http.Context) error {
		http.SetOperation(ctx, admin.OperationInferenceKeyServiceBindInferenceKeyPolicy)
		in := new(view.BindInferenceKeyPolicyRequest)
		if err := decodeInferenceKeyWrite(ctx, in); err != nil {
			return err
		}
		id := ctx.Vars().Get("key_id")
		if in.KeyId != "" && in.KeyId != id {
			return view.ErrorInvalidInferenceKeyRequest("key_id must match path")
		}
		in.KeyId = id
		out, err := ctx.Middleware(func(c context.Context, v interface{}) (interface{}, error) {
			return inference.BindInferenceKeyPolicy(c, v.(*view.BindInferenceKeyPolicyRequest))
		})(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(200, out)
	})
}
