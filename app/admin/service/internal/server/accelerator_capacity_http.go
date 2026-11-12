package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const acceleratorProfileCapacityPath = "/api/v1/admin/accelerator/profiles/{profile_id}/capacity"
const acceleratorSupplyCapacityPath = "/api/v1/admin/accelerator/supply-groups/{group_id}/capacity"

func acceleratorCapacityQuery(r *http.Request, profile bool) (url.Values, error) {
	invalid := errors.BadRequest("INVALID_ACCELERATOR_REQUEST", "invalid accelerator capacity query")
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.ForceQuery || len(r.URL.RawQuery) > 4096 {
		return nil, invalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalid
	}
	for key, values := range q {
		if len(values) != 1 {
			return nil, invalid
		}
		switch key {
		case "profile_version":
			if !profile {
				return nil, invalid
			}
			n, e := strconv.ParseUint(values[0], 10, 64)
			if e != nil || n == 0 || strconv.FormatUint(n, 10) != values[0] {
				return nil, invalid
			}
		case "include_details":
			if values[0] != "true" && values[0] != "false" {
				return nil, invalid
			}
		default:
			return nil, invalid
		}
	}
	if profile && q.Get("profile_version") == "" {
		return nil, invalid
	}
	return q, nil
}

// Register only these two custom handlers. Their generated handlers must not be
// registered alongside them, because the public JSON preserves optional nulls.
func RegisterAcceleratorCapacityHTTPServer(server *khttp.Server, svc admin.AcceleratorCapacityAdminServiceHTTPServer) {
	server.Route("/").GET(acceleratorProfileCapacityPath, func(ctx khttp.Context) error {
		query, err := acceleratorCapacityQuery(ctx.Request(), true)
		if err != nil {
			return err
		}
		khttp.SetOperation(ctx, admin.OperationAcceleratorCapacityAdminServiceGetProfileCapacity)
		request := new(view.GetAcceleratorProfileCapacityRequest)
		if err := ctx.BindVars(request); err != nil {
			return errors.BadRequest("INVALID_ACCELERATOR_REQUEST", "invalid capacity path")
		}
		request.ProfileVersion, _ = strconv.ParseUint(query.Get("profile_version"), 10, 64)
		request.IncludeDetails = query.Get("include_details") == "true"
		response, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
			return svc.GetProfileCapacity(call, in.(*view.GetAcceleratorProfileCapacityRequest))
		})(ctx, request)
		if err != nil {
			return err
		}
		message, ok := response.(*view.AcceleratorProfileCapacity)
		if !ok || message == nil {
			return errors.ServiceUnavailable("ACCELERATOR_INVALID_RESPONSE", "invalid profile capacity response")
		}
		return writeAcceleratorCapacity(ctx, message)
	})
	server.Route("/").GET(acceleratorSupplyCapacityPath, func(ctx khttp.Context) error {
		query, err := acceleratorCapacityQuery(ctx.Request(), false)
		if err != nil {
			return err
		}
		khttp.SetOperation(ctx, admin.OperationAcceleratorCapacityAdminServiceGetSupplyGroupCapacity)
		request := new(view.GetAcceleratorSupplyGroupCapacityRequest)
		if err := ctx.BindVars(request); err != nil {
			return errors.BadRequest("INVALID_ACCELERATOR_REQUEST", "invalid capacity path")
		}
		request.IncludeDetails = query.Get("include_details") == "true"
		response, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
			return svc.GetSupplyGroupCapacity(call, in.(*view.GetAcceleratorSupplyGroupCapacityRequest))
		})(ctx, request)
		if err != nil {
			return err
		}
		message, ok := response.(*view.AcceleratorSupplyGroupCapacity)
		if !ok || message == nil {
			return errors.ServiceUnavailable("ACCELERATOR_INVALID_RESPONSE", "invalid group capacity response")
		}
		return writeAcceleratorCapacity(ctx, message)
	})
}
func writeAcceleratorCapacity(ctx khttp.Context, message proto.Message) error {
	raw, err := acceleratorCapacityHTTPJSON(message)
	if err != nil {
		return err
	}
	ctx.Response().Header().Set("Content-Type", "application/json")
	ctx.Response().Header().Set("Cache-Control", "no-store")
	ctx.Response().WriteHeader(http.StatusOK)
	_, err = ctx.Response().Write(raw)
	return err
}

func acceleratorCapacityHTTPJSON(message proto.Message) ([]byte, error) {
	kind := ""
	switch message.(type) {
	case *view.AcceleratorProfileCapacity:
		kind = "profile"
	case *view.AcceleratorSupplyGroupCapacity:
		kind = "group"
	default:
		return nil, errors.InternalServer("ACCELERATOR_INVALID_RESPONSE", "unexpected capacity response type")
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(message)
	if err != nil {
		return nil, err
	}
	return projectAcceleratorCapacity(raw, kind)
}

func projectAcceleratorCapacity(raw json.RawMessage, kind string) (json.RawMessage, error) {
	body := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
	}
	nulls := []string{}
	switch kind {
	case "totals":
		nulls = []string{"total_units", "allocated_units", "unallocated_units", "total_memory_mib", "allocated_memory_mib", "unallocated_memory_mib"}
	case "detail":
		nulls = []string{"nominal_units", "estimated_available_units"}
	case "profile", "resource":
		nulls = []string{"nominal_units", "estimated_available_units"}
	case "group":
		nulls = []string{"physical_device_count"}
	}
	for _, key := range nulls {
		if _, ok := body[key]; !ok {
			body[key] = json.RawMessage("null")
		}
	}
	if kind == "profile" || kind == "resource" || kind == "detail" {
		value, err := projectAcceleratorCapacity(body["totals"], "totals")
		if err != nil {
			return nil, err
		}
		body["totals"] = value
	}
	if kind == "profile" || kind == "resource" {
		value, err := projectAcceleratorCapacityRows(body["details"], "detail")
		if err != nil {
			return nil, err
		}
		body["details"] = value
		if len(body["missing_fields"]) == 0 || string(body["missing_fields"]) == "null" {
			body["missing_fields"] = json.RawMessage("[]")
		}
	}
	if kind == "group" {
		value, err := projectAcceleratorCapacityRows(body["resource_types"], "resource")
		if err != nil {
			return nil, err
		}
		body["resource_types"] = value
	}
	return json.Marshal(body)
}
func projectAcceleratorCapacityRows(raw json.RawMessage, kind string) (json.RawMessage, error) {
	input := []json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
	}
	output := make([]json.RawMessage, 0, len(input))
	for _, row := range input {
		value, err := projectAcceleratorCapacity(row, kind)
		if err != nil {
			return nil, err
		}
		output = append(output, value)
	}
	return json.Marshal(output)
}
