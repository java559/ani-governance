package server

import (
	"bytes"
	"encoding/json"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"

	view "go-wind-admin/api/gen/go/catalog/service/v1"
)

func acceleratorInitializationHTTPFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		list := request.Method == http.MethodGet && (request.URL.Path == "/api/v1/admin/accelerator/discovered-nodes" || request.URL.Path == "/api/v1/admin/accelerator/supply-candidates")
		configuration := request.Method == http.MethodGet && request.URL.Path == "/api/v1/admin/accelerator/supply-configurations"
		read := request.Method == http.MethodGet && request.URL.Path == "/api/v1/admin/accelerator/initialization"
		create := request.Method == http.MethodPost && (request.URL.Path == "/api/v1/admin/accelerator/initializations" || request.URL.Path == "/api/v1/admin/accelerator/supply-registrations" || request.URL.Path == "/api/v1/admin/accelerator/supply-groups:preview-policy" || request.URL.Path == "/api/v1/admin/accelerator/supply-groups:adopt" || request.URL.Path == "/api/v1/admin/accelerator/profiles")
		registrationPath := strings.TrimPrefix(request.URL.Path, "/api/v1/admin/accelerator/supply-registrations/")
		registration := request.Method == http.MethodGet && registrationPath != request.URL.Path && registrationPath != "" && !strings.Contains(registrationPath, "/")
		operationPath := strings.TrimPrefix(request.URL.Path, "/api/v1/admin/accelerator/initializations/")
		operation := operationPath != request.URL.Path && operationPath != "" && !strings.Contains(operationPath, "/") && (request.Method == http.MethodGet || request.Method == http.MethodPost && strings.HasSuffix(operationPath, ":retry"))
		if !list && !read && !create && !operation && !registration && !configuration {
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		if configuration && !validAcceleratorSupplyConfigurationQuery(request) || !configuration && !validAcceleratorInitializationQuery(request, list, read) {
			khttp.DefaultErrorEncoder(writer, request, errors.BadRequest("INVALID_ACCELERATOR_REQUEST", "invalid initialization query"))
			return
		}
		if (registration || configuration) && !validAcceleratorSupplyRegistrationRead(request) {
			khttp.DefaultErrorEncoder(writer, request, errors.BadRequest("INVALID_ACCELERATOR_REQUEST", "supply registration read does not accept a body"))
			return
		}
		if request.Method == http.MethodPost {
			if err := validateAcceleratorPolicyBody(request); err != nil {
				khttp.DefaultErrorEncoder(writer, request, errors.BadRequest("INVALID_ACCELERATOR_REQUEST", "invalid supply registration body"))
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func validAcceleratorSupplyRegistrationRead(request *http.Request) bool {
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
		return false
	}
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	original := request.Body
	_, err := io.ReadFull(original, make([]byte, 1))
	closeErr := original.Close()
	request.Body = http.NoBody
	return err == io.EOF && closeErr == nil
}

// Keep the generated route and middleware chain. The shared strict decoder
// rejects unknown Proto JSON fields before the framework's ordinary binding;
// the bounded body is restored for that generated handler.
func validateAcceleratorSupplyRegistrationBody(request *http.Request) error {
	return validateAcceleratorPolicyBody(request)
}

func validateAcceleratorPolicyBody(request *http.Request) error {
	var message proto.Message
	switch request.URL.Path {
	case "/api/v1/admin/accelerator/supply-registrations":
		message = &view.RegisterAcceleratorSupplyConfigurationRequest{}
	case "/api/v1/admin/accelerator/initializations":
		message = &view.CreateAcceleratorInitializationRequest{}
	case "/api/v1/admin/accelerator/supply-groups:preview-policy":
		message = &view.PreviewAcceleratorSupplyGroupPolicyRequest{}
	case "/api/v1/admin/accelerator/supply-groups:adopt":
		message = &view.AdoptAcceleratorSupplyRequest{}
	case "/api/v1/admin/accelerator/profiles":
		message = &view.CreateAcceleratorProfileRequest{}
	default:
		message = &view.RetryAcceleratorInitializationOperationRequest{}
	}
	const limit = 1 << 20
	original := request.Body
	var body bytes.Buffer
	if original != nil {
		request.Body = io.NopCloser(io.TeeReader(original, &body))
	}
	err := decodeStrictJSONHTTPWithLimit(request, message, limit)
	if original != nil {
		if closeErr := original.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}
	request.Body = io.NopCloser(bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	// proto3 scalar defaults lose presence after binding. Inspect the same
	// validated bytes before forwarding so explicit zero cannot override a
	// server-derived profile or conceal an unused policy member.
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body.Bytes(), &envelope) != nil || envelope.Data == nil {
		return errInvalidJSONRequest
	}
	if profiles, ok := envelope.Data["profiles"]; ok {
		var entries []map[string]json.RawMessage
		if json.Unmarshal(profiles, &entries) != nil {
			return errInvalidJSONRequest
		}
		for _, entry := range entries {
			var derive bool
			if value, ok := entry["derive_from_group_policy"]; ok && json.Unmarshal(value, &derive) != nil {
				return errInvalidJSONRequest
			}
			if derive {
				for _, field := range []string{"memory_mib_per_device", "core_limit_percent", "max_devices_per_replica", "allocation_type", "resource_key", "compute_units_per_device"} {
					if _, present := entry[field]; present {
						return errInvalidJSONRequest
					}
				}
			}
		}
	}
	if _, group := envelope.Data["group_id"]; group && request.URL.Path == "/api/v1/admin/accelerator/profiles" {
		if _, spec := envelope.Data["spec"]; spec {
			return errInvalidJSONRequest
		}
	}
	if err := validateAcceleratorPolicyJSON(envelope.Data); err != nil {
		return err
	}
	if registrations, ok := envelope.Data["registrations"]; ok {
		var entries []map[string]json.RawMessage
		if json.Unmarshal(registrations, &entries) != nil {
			return errInvalidJSONRequest
		}
		for _, entry := range entries {
			if _, whole := entry["whole_max_devices_per_replica"]; whole {
				if _, old := entry["max_devices_per_replica"]; old {
					return errInvalidJSONRequest
				}
			}
		}
	}
	return nil
}

func validAcceleratorSupplyConfigurationQuery(request *http.Request) bool {
	if request.URL.ForceQuery || len(request.URL.RawQuery) > 4096 {
		return false
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(query) != 2 {
		return false
	}
	for _, key := range []string{"connection_ref", "provider_config_ref"} {
		values := query[key]
		if len(values) != 1 || values[0] == "" || len(values[0]) > 256 {
			return false
		}
	}
	return true
}

func validAcceleratorInitializationQuery(request *http.Request, list bool, read ...bool) bool {
	if request.URL.ForceQuery || len(request.URL.RawQuery) > 4096 {
		return false
	}
	if !list && (len(read) == 0 || !read[0]) {
		return request.URL.RawQuery == ""
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range query {
		if len(values) != 1 {
			return false
		}
		if key == "connection_ref" {
			if values[0] == "" || len(values[0]) > 128 {
				return false
			}
			continue
		}
		if !list || key != "page" && key != "pageSize" {
			return false
		}
		value, err := strconv.ParseUint(values[0], 10, 32)
		if err != nil || value == 0 || values[0] != strconv.FormatUint(value, 10) || key == "pageSize" && value > 200 {
			return false
		}
	}
	return true
}

// Policy presence is part of the contract, including unused fields whose JSON
// value is null. A derived specification never accepts a client-side shape.
func validateAcceleratorPolicyJSON(input map[string]json.RawMessage) error {
	for key, raw := range input {
		if key == "provider_config_version" {
			var version string
			if json.Unmarshal(raw, &version) != nil {
				return errInvalidJSONRequest
			}
			value, err := strconv.ParseUint(version, 10, 64)
			if err != nil || value == 0 || strconv.FormatUint(value, 10) != version {
				return errInvalidJSONRequest
			}
		}
		if key == "policy" {
			var policy map[string]json.RawMessage
			if json.Unmarshal(raw, &policy) != nil || policy == nil {
				return errInvalidJSONRequest
			}
			var kind string
			if json.Unmarshal(policy["policy_kind"], &kind) != nil {
				return errInvalidJSONRequest
			}
			allowed := map[string]bool{"policy_kind": true}
			switch kind {
			case "WHOLE":
			case "EQUAL_SHARE":
				allowed["parts"] = true
			case "NATIVE_RESOURCE":
				allowed["resource_key"], allowed["native_allocation_type"] = true, true
			default:
				return errInvalidJSONRequest
			}
			for field := range policy {
				if !allowed[field] {
					return errInvalidJSONRequest
				}
			}
		}
		var object map[string]json.RawMessage
		if len(raw) != 0 && raw[0] == '{' {
			if json.Unmarshal(raw, &object) != nil {
				return errInvalidJSONRequest
			}
			if err := validateAcceleratorPolicyJSON(object); err != nil {
				return err
			}
		} else if len(raw) != 0 && raw[0] == '[' {
			var entries []json.RawMessage
			if json.Unmarshal(raw, &entries) != nil {
				return errInvalidJSONRequest
			}
			for _, entry := range entries {
				if len(entry) != 0 && entry[0] == '{' {
					if json.Unmarshal(entry, &object) != nil {
						return errInvalidJSONRequest
					}
					if err := validateAcceleratorPolicyJSON(object); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
