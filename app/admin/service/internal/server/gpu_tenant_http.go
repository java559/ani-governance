package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	errors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/gpu/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/encoding/protojson"
)

const gpuTenantListPath = "/api/v1/admin/gpu/tenants"

func validateGpuTenantHTTP(r *http.Request) error {
	invalid := errors.BadRequest("GPU_INVALID_QUERY", "invalid GPU tenant page")
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.URL.RawQuery) > 4096 {
		return invalid
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid
	}
	for key, values := range query {
		if key != "page" && key != "pageSize" && key != "tenantId" || len(values) != 1 {
			return invalid
		}
		n, err := strconv.ParseUint(values[0], 10, 32)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != values[0] || key == "pageSize" && n > 6 {
			return invalid
		}
	}
	return nil
}

func RegisterGpuTenantHTTPServer(server *khttp.Server, svc *service.GpuTenantAdminService) {
	server.Route("/").GET(gpuTenantListPath, func(ctx khttp.Context) error {
		if err := validateGpuTenantHTTP(ctx.Request()); err != nil {
			return err
		}
		khttp.SetOperation(ctx, admin.OperationGpuTenantAdminServiceListGpuTenants)
		req := new(view.ListGpuTenantsRequest)
		if err := ctx.BindQuery(req); err != nil {
			return errors.BadRequest("GPU_INVALID_QUERY", "invalid GPU tenant page")
		}
		out, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
			return svc.ListGpuTenants(call, in.(*view.ListGpuTenantsRequest))
		})(ctx, req)
		if err != nil {
			return err
		}
		raw, err := gpuTenantHTTPJSON(out.(*view.ListGpuTenantsResponse))
		if err != nil {
			return err
		}
		ctx.Response().Header().Set("Content-Type", "application/json")
		ctx.Response().WriteHeader(http.StatusOK)
		_, err = ctx.Response().Write(raw)
		return err
	})
}

// Reuse the GPU point/quality projection so nested latest values have exactly
// the same explicit-null semantics as the existing standalone GPU response.
func gpuTenantHTTPJSON(message *view.ListGpuTenantsResponse) ([]byte, error) {
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(message)
	if err != nil {
		return nil, err
	}
	var body map[string]json.RawMessage
	if err = json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	rows := make([]json.RawMessage, 0, len(message.Items))
	for _, item := range message.Items {
		if item == nil {
			return nil, errors.ServiceUnavailable("GPU_INVALID_RESPONSE", "invalid GPU tenant row")
		}
		raw, err = (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(item)
		if err != nil {
			return nil, err
		}
		var row map[string]json.RawMessage
		if err = json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		usage := []byte("null")
		if item.Usage != nil {
			usage, err = gpuHTTPJSON(item.Usage)
			if err != nil {
				return nil, err
			}
		}
		row["usage"] = usage
		if item.Allocation != nil {
			var allocation map[string]json.RawMessage
			if err = json.Unmarshal(row["allocation"], &allocation); err != nil {
				return nil, err
			}
			for _, key := range []string{"memoryMiB", "bindingCount", "knownMemoryMiB", "knownBindingCount"} {
				if _, present := allocation[key]; !present {
					allocation[key] = json.RawMessage("null")
				}
			}
			row["allocation"], err = json.Marshal(allocation)
			if err != nil {
				return nil, err
			}
		}
		raw, err = json.Marshal(row)
		if err != nil {
			return nil, err
		}
		rows = append(rows, raw)
	}
	body["items"], err = json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	raw, err = json.Marshal(body)
	if err == nil && len(raw) > 8<<20 {
		return nil, errors.New(429, "GPU_QUERY_BUDGET_EXCEEDED", "GPU tenant page exceeds8MiB")
	}
	return raw, err
}
