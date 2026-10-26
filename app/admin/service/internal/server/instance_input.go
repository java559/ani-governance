package server

import (
	"bytes"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
	computeview "go-wind-admin/api/gen/go/compute/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	vmview "go-wind-admin/api/gen/go/vm/service/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Inspect presence before ProtoJSON maps null to an absent optional field.
// This applies to creation only: PATCH gpu:null remains an explicit clear.
func decodeInstanceHTTP(r *http.Request, message proto.Message) error {
	invalid := errors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid instance request")
	if r == nil || r.Body == nil {
		return invalid
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	if err != nil || len(raw) == 0 || len(raw) > 65536 {
		return invalid
	}
	switch message.(type) {
	case *view.UpdateInstanceSchedulingSettingsRequest:
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil {
			return invalid
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(envelope["data"], &data) != nil {
			return invalid
		}
		var defaults map[string]json.RawMessage
		if value, ok := data["defaults"]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &defaults) != nil {
			return invalid
		}
		if len(defaults) > 0 {
			if err := validateSchedulingJSON(data["defaults"]); err != nil {
				return err
			}
		}
		var allowlists []json.RawMessage
		if value, ok := data["priority_class_allowlists"]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &allowlists) != nil {
			return invalid
		}
	case *computeview.UpdatePresetRequest, *computeview.SetPresetEnabledRequest:
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil {
			return invalid
		}
		if _, present := envelope["id"]; present {
			return invalid
		}
		if _, update := message.(*computeview.UpdatePresetRequest); update {
			var data map[string]json.RawMessage
			if json.Unmarshal(envelope["data"], &data) != nil {
				return invalid
			}
			if _, present := data["code"]; present {
				return invalid
			}
		}
	}
	if _, create := message.(*view.CreateInstanceRequest); create {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil {
			return invalid
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(envelope["data"], &data) != nil {
			return invalid
		}
		if err := validateInstanceConfigurationJSON(data); err != nil {
			return err
		}
		if value, present := data["scheduling"]; present {
			if err := validateSchedulingJSON(value); err != nil {
				return err
			}
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if decodeStrictJSONHTTP(r, message) != nil {
		return invalid
	}
	return nil
}

var instancePublicReason = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,95}$`)

func instanceHTTPError(err error) error {
	if err == nil {
		return nil
	}
	var known *errors.Error
	if stderrors.As(err, &known) {
		return known
	}
	code, reason := 503, "INSTANCE_DEPENDENCY_UNAVAILABLE"
	switch status.Code(err) {
	case codes.InvalidArgument:
		code, reason = 400, "INVALID_INSTANCE_REQUEST"
	case codes.Unauthenticated:
		code, reason = 401, "INSTANCE_AUTHENTICATION_FAILED"
	case codes.PermissionDenied:
		code, reason = 403, "INSTANCE_SCOPE_DENIED"
	case codes.NotFound:
		code, reason = 404, "INSTANCE_NOT_FOUND"
	case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
		code, reason = 409, "INSTANCE_CONFLICT"
	case codes.ResourceExhausted:
		code, reason = 429, "INSTANCE_LOG_CONNECTION_LIMIT"
	case codes.Canceled:
		code, reason = 499, "INSTANCE_CANCELED"
	}
	message := status.Convert(err).Message()
	if status.Code(err) == codes.ResourceExhausted && message == "INSTANCE_PVC_CATALOG_LIMIT" {
		code = 422
	}
	if instancePublicReason.MatchString(message) && (strings.HasPrefix(message, "INSTANCE_") || strings.HasPrefix(message, "COMPUTE_PRESET_") || strings.HasPrefix(message, "SCHEDULING_") || message == "INVALID_INSTANCE_REQUEST" || message == "INVALID_SCHEDULING_OPTIONS") {
		reason = message
	}
	return errors.New(code, reason, "instance request failed")
}

func validateSchedulingJSON(raw json.RawMessage) error {
	invalid := errors.BadRequest("INVALID_SCHEDULING_OPTIONS", "invalid scheduling options")
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) == 0 {
		return invalid
	}
	for field, value := range object {
		var text string
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &text) != nil {
			return invalid
		}
		switch field {
		case "pod_placement":
			switch text {
			case "NONE", "COLOCATE_PREFERRED", "SPREAD_PREFERRED":
			default:
				return invalid
			}
		case "priority_class_name":
			if text == "" || strings.TrimSpace(text) != text {
				return invalid
			}
		default:
			return invalid
		}
	}
	return nil
}

func decodeInstanceQuery(r *http.Request, message proto.Message) error {
	invalid := errors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid instance query")
	allowed := map[string]bool{}
	switch message.(type) {
	case *vmview.ListVMsRequest:
		allowed["page"], allowed["pageSize"] = true, true
	case *view.ListMountablePVCsRequest:
		for _, k := range []string{"page", "pageSize", "name"} {
			allowed[k] = true
		}
	case *view.ListInstancesRequest:
		for _, k := range []string{"page", "pageSize", "instance_kind", "compute_type", "observedState", "name"} {
			allowed[k] = true
		}
	case *computeview.ListPresetsRequest, *computeview.AdminListPresetsRequest:
		for _, k := range []string{"page", "pageSize", "name", "code", "compute_type", "gpu_mode"} {
			allowed[k] = true
		}
		if _, tenant := message.(*computeview.ListPresetsRequest); tenant {
			allowed["usage"] = true
		}
		if _, admin := message.(*computeview.AdminListPresetsRequest); admin {
			allowed["enabled"] = true
		}
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid
	}
	if _, tenant := message.(*computeview.ListPresetsRequest); tenant {
		if len(values["usage"]) != 1 {
			return invalid
		}
		switch values.Get("usage") {
		case "CONTAINER_INSTANCE", "TRAINING", "INFERENCE", "VIRTUAL_MACHINE":
		default:
			return invalid
		}
	}
	object := map[string]any{}
	for field, values := range values {
		if !allowed[field] || len(values) != 1 {
			return invalid
		}
		value := values[0]
		switch field {
		case "page", "pageSize":
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != value || field == "pageSize" && n > 100 {
				return invalid
			}
			object[field] = uint32(n)
		case "usage":
			object[field] = value
		case "instance_kind":
			if value != "container" {
				return invalid
			}
			object[field] = value
		case "compute_type":
			if value != "cpu" && value != "gpu" {
				return invalid
			}
			object[field] = value
		case "observedState":
			switch value {
			case "pending", "running", "stopped", "deleted":
			default:
				return invalid
			}
			object[field] = value
		case "gpu_mode":
			if value != "WHOLE_EXCLUSIVE" && value != "SHARED_FIXED" {
				return invalid
			}
			object[field] = value
		case "enabled":
			if value != "true" && value != "false" {
				return invalid
			}
			object[field] = value == "true"
		case "name", "code":
			value = strings.TrimSpace(value)
			max := 128
			if field == "code" {
				max = 64
			}
			if _, instances := message.(*view.ListInstancesRequest); instances {
				max = 80
			}
			if utf8.RuneCountInString(value) > max {
				return invalid
			}
			object[field] = value
		}
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return invalid
	}
	if err = (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, message); err != nil {
		return invalid
	}
	return nil
}

// ProtoJSON treats null as absence, so reject it before decoding user config.
func validateInstanceConfigurationJSON(data map[string]json.RawMessage) error {
	invalid := errors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid instance configuration")
	var rejectNull func(any) bool
	rejectNull = func(v any) bool {
		switch value := v.(type) {
		case nil:
			return true
		case map[string]any:
			for _, child := range value {
				if rejectNull(child) {
					return true
				}
			}
		case []any:
			for _, child := range value {
				if rejectNull(child) {
					return true
				}
			}
		}
		return false
	}
	for _, field := range []string{"workspace_mount_path", "volume_mounts", "network", "labels", "annotations", "env"} {
		raw, present := data[field]
		if !present {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) != nil || rejectNull(value) {
			return invalid
		}
		if field == "workspace_mount_path" && value == "" {
			return invalid
		}
		if field == "network" {
			object, ok := value.(map[string]any)
			if !ok || len(object) == 0 {
				return invalid
			}
		}
	}
	return nil
}
