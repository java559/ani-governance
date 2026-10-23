package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type NetworkOperationPolicy struct {
	Method, Path, Permission string
	ReadOnly                 bool
}

// NetworkOperations is the complete tenant Network surface. Catalog membership
// never grants a role: current tenant, NETWORK and Casbin gates still apply.
var NetworkOperations = map[string]NetworkOperationPolicy{
	"/admin.service.v1.NetworkService/ListVPCCIDRPresets": {"GET", "/api/v1/networks/vpc-cidr-presets", "network:vpc:presets", true},
	"/admin.service.v1.NetworkService/CreateVPC":          {"POST", "/api/v1/networks/vpcs", "network:vpc:create", false},
	VPCReadOperation: {"GET", "/api/v1/networks/vpcs/{vpc_id}", "network:vpc:get", true},
	"/admin.service.v1.NetworkService/ListVPCs":                 {"GET", "/api/v1/networks/vpcs", "network:vpc:list", true},
	"/admin.service.v1.NetworkService/DeleteVPC":                {"DELETE", "/api/v1/networks/vpcs/{vpc_id}", "network:vpc:delete", false},
	"/admin.service.v1.NetworkService/GetOperation":             {"GET", "/api/v1/networks/operations/{operation_id}", "network:operation:get", true},
	"/admin.service.v1.NetworkService/CreateSubnet":             {"POST", "/api/v1/networks/subnets", "network:subnet:create", false},
	"/admin.service.v1.NetworkService/GetSubnet":                {"GET", "/api/v1/networks/subnets/{subnet_id}", "network:subnet:get", true},
	"/admin.service.v1.NetworkService/ListSubnets":              {"GET", "/api/v1/networks/subnets", "network:subnet:list", true},
	"/admin.service.v1.NetworkService/DeleteSubnet":             {"DELETE", "/api/v1/networks/subnets/{subnet_id}", "network:subnet:delete", false},
	"/admin.service.v1.NetworkService/CreateEIP":                {"POST", "/api/v1/networks/eips", "network:eip:create", false},
	"/admin.service.v1.NetworkService/GetEIP":                   {"GET", "/api/v1/networks/eips/{eip_id}", "network:eip:get", true},
	"/admin.service.v1.NetworkService/ListEIPs":                 {"GET", "/api/v1/networks/eips", "network:eip:list", true},
	"/admin.service.v1.NetworkService/DeleteEIP":                {"DELETE", "/api/v1/networks/eips/{eip_id}", "network:eip:delete", false},
	"/admin.service.v1.NetworkService/GetVPCSnat":               {"GET", "/api/v1/networks/vpcs/{vpc_id}/snat", "network:snat:get", true},
	"/admin.service.v1.NetworkService/BindVPCSnat":              {"POST", "/api/v1/networks/vpcs/{vpc_id}/snat/bindings", "network:snat:bind", false},
	"/admin.service.v1.NetworkService/GetVPCSnatBinding":        {"GET", "/api/v1/networks/snat/bindings/{binding_id}", "network:snat:get", true},
	"/admin.service.v1.NetworkService/SetVPCSnatEnabled":        {"PATCH", "/api/v1/networks/snat/bindings/{binding_id}", "network:snat:update", false},
	"/admin.service.v1.NetworkService/DeleteVPCSnatBinding":     {"DELETE", "/api/v1/networks/snat/bindings/{binding_id}", "network:snat:delete", false},
	"/admin.service.v1.NetworkService/CreateLoadBalancer":       {"POST", "/api/v1/networks/load-balancers", "network:load-balancer:create", false},
	"/admin.service.v1.NetworkService/GetLoadBalancer":          {"GET", "/api/v1/networks/load-balancers/{load_balancer_id}", "network:load-balancer:get", true},
	"/admin.service.v1.NetworkService/ListLoadBalancers":        {"GET", "/api/v1/networks/load-balancers", "network:load-balancer:list", true},
	"/admin.service.v1.NetworkService/UpdateLoadBalancer":       {"PATCH", "/api/v1/networks/load-balancers/{load_balancer_id}", "network:load-balancer:update", false},
	"/admin.service.v1.NetworkService/DeleteLoadBalancer":       {"DELETE", "/api/v1/networks/load-balancers/{load_balancer_id}", "network:load-balancer:delete", false},
	"/admin.service.v1.NetworkService/GetLoadBalancerOperation": {"GET", "/api/v1/networks/load-balancers/operations/{operation_id}", "network:load-balancer:operation:get", true},
}

func IsNetworkOperation(operation string) bool { _, ok := NetworkOperations[operation]; return ok }

func NetworkPermission(method, path string) (string, bool) {
	for _, policy := range NetworkOperations {
		if policy.Method == method && policy.Path == path {
			return policy.Permission, true
		}
	}
	return "", false
}

func IsNetworkPath(path string) bool {
	return path == "/api/v1/networks" || strings.HasPrefix(path, "/api/v1/networks/") || path == "/api/v1/admin/networks" || strings.HasPrefix(path, "/api/v1/admin/networks/")
}

var networkIDPatterns = map[string]*regexp.Regexp{
	"vpc_id":           regexp.MustCompile(`^vpc_[0-9a-f]{32}$`),
	"subnet_id":        regexp.MustCompile(`^subnet_[0-9a-f]{32}$`),
	"eip_id":           regexp.MustCompile(`^eip_[0-9a-f]{32}$`),
	"public_eip_id":    regexp.MustCompile(`^eip_[0-9a-f]{32}$`),
	"binding_id":       regexp.MustCompile(`^snat_[0-9a-f]{32}$`),
	"load_balancer_id": regexp.MustCompile(`^lb_[0-9a-f]{32}$`),
	"operation_id":     regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`),
}

type networkRequestDigestKey struct{}
type networkRequestDigest struct{ Operation, Method, Path, Query, Digest string }

func networkRequestMessage(operation string) proto.Message {
	switch strings.TrimPrefix(operation, "/admin.service.v1.NetworkService/") {
	case "CreateVPC":
		return &view.CreateVPCRequest{}
	case "GetVPC":
		return &view.GetVPCRequest{}
	case "ListVPCCIDRPresets":
		return &view.ListVPCCIDRPresetsRequest{}
	case "ListVPCs":
		return &view.ListVPCsRequest{}
	case "DeleteVPC":
		return &view.DeleteVPCRequest{}
	case "GetOperation":
		return &view.GetOperationRequest{}
	case "CreateSubnet":
		return &view.CreateSubnetRequest{}
	case "GetSubnet":
		return &view.GetSubnetRequest{}
	case "ListSubnets":
		return &view.ListSubnetsRequest{}
	case "DeleteSubnet":
		return &view.DeleteSubnetRequest{}
	case "CreateEIP":
		return &view.CreateEIPRequest{}
	case "GetEIP":
		return &view.GetEIPRequest{}
	case "ListEIPs":
		return &view.ListEIPsRequest{}
	case "DeleteEIP":
		return &view.DeleteEIPRequest{}
	case "GetVPCSnat":
		return &view.GetVPCSnatRequest{}
	case "BindVPCSnat":
		return &view.BindVPCSnatRequest{}
	case "GetVPCSnatBinding":
		return &view.GetVPCSnatBindingRequest{}
	case "SetVPCSnatEnabled":
		return &view.SetVPCSnatEnabledRequest{}
	case "DeleteVPCSnatBinding":
		return &view.DeleteVPCSnatBindingRequest{}
	case "CreateLoadBalancer":
		return &view.CreateLoadBalancerRequest{}
	case "GetLoadBalancer":
		return &view.GetLoadBalancerRequest{}
	case "ListLoadBalancers":
		return &view.ListLoadBalancersRequest{}
	case "UpdateLoadBalancer":
		return &view.UpdateLoadBalancerRequest{}
	case "DeleteLoadBalancer":
		return &view.DeleteLoadBalancerRequest{}
	case "GetLoadBalancerOperation":
		return &view.GetLoadBalancerOperationRequest{}
	}
	return nil
}

func networkHTTPRoute(r *http.Request) (string, proto.Message, string, string) {
	if op, msg, field, id := platformNetworkHTTPRoute(r); op != "" {
		return op, msg, field, id
	}
	for operation, policy := range NetworkOperations {
		if policy.Method != r.Method {
			continue
		}
		start := strings.IndexByte(policy.Path, '{')
		if start < 0 {
			if policy.Path == r.URL.Path {
				return operation, networkRequestMessage(operation), "", ""
			}
			continue
		}
		end := strings.IndexByte(policy.Path, '}')
		prefix, suffix := policy.Path[:start], policy.Path[end+1:]
		if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) || len(r.URL.Path) < len(prefix)+len(suffix) {
			continue
		}
		field := policy.Path[start+1 : end]
		id := r.URL.Path[len(prefix) : len(r.URL.Path)-len(suffix)]
		if pattern := networkIDPatterns[field]; pattern != nil && pattern.MatchString(id) {
			return operation, networkRequestMessage(operation), field, id
		}
	}
	return "", nil, "", ""
}

func networkQueryFields(operation string) map[string]bool {
	if IsPlatformNetworkOperation(operation) {
		return platformNetworkQueryFields(operation)
	}
	fields := map[string]bool{}
	switch strings.TrimPrefix(operation, "/admin.service.v1.NetworkService/") {
	case "ListVPCs":
		for _, field := range []string{"name", "state", "page", "pageSize"} {
			fields[field] = true
		}
	case "ListEIPs":
		for _, field := range []string{"name", "state", "limit", "cursor"} {
			fields[field] = true
		}
	case "ListSubnets":
		for _, field := range []string{"vpc_id", "name", "state", "limit", "cursor"} {
			fields[field] = true
		}
	case "ListLoadBalancers":
		for _, field := range []string{"name", "vpc_id", "subnet_id", "exposure", "state", "limit", "cursor"} {
			fields[field] = true
		}
	}
	return fields
}

func validateNetworkQuery(operation string, query url.Values) bool {
	allowed := networkQueryFields(operation)
	for field, values := range query {
		if !allowed[field] || len(values) != 1 || len(values[0]) > 4096 {
			return false
		}
		value := values[0]
		pattern := networkIDPatterns[field]
		if pattern == nil {
			pattern = platformNetworkIDPatterns[field]
		}
		if pattern != nil && value != "" && !pattern.MatchString(value) {
			return false
		}
		if field == "page" || field == "pageSize" {
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != value || (field == "pageSize" && n > 100) || (operation == "/admin.service.v1.PlatformNetworkService/ListVirtualSwitches" && n > 2147483647) {
				return false
			}
		}
		if field == "limit" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 100 || strconv.Itoa(n) != value {
				return false
			}
		}
		if field == "state" && value != "" {
			switch strings.TrimPrefix(strings.ToUpper(value), "RESOURCE_STATE_") {
			case "UNSPECIFIED", "PROVISIONING", "AVAILABLE", "DEGRADED", "FAILED", "DELETING", "DELETED":
			default:
				return false
			}
		}
		if field == "exposure" {
			_, named := view.LoadBalancerExposure_value[value]
			n, err := strconv.Atoi(value)
			if !named && (err != nil || n < 0 || n > 3 || strconv.Itoa(n) != value) {
				return false
			}
		}
	}
	return true
}

func validateNetworkMessage(m protoreflect.Message) bool {
	valid := true
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		check := func(value protoreflect.Value) bool {
			if f.Kind() == protoreflect.EnumKind {
				return f.Enum().Values().ByNumber(value.Enum()) != nil
			}
			if f.Kind() == protoreflect.MessageKind {
				return validateNetworkMessage(value.Message())
			}
			pattern := networkIDPatterns[string(f.Name())]
			if pattern == nil {
				pattern = platformNetworkIDPatterns[string(f.Name())]
			}
			if pattern != nil && f.Kind() == protoreflect.StringKind {
				return value.String() == "" || pattern.MatchString(value.String())
			}
			return true
		}
		if f.IsList() {
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				if !check(list.Get(i)) {
					valid = false
					return false
				}
			}
		} else if !check(v) {
			valid = false
			return false
		}
		return true
	})
	return valid
}

// NetworkHTTPFilter runs before generated decoding for both JWT and AK calls.
// It validates registered tenant and platform routes, rejects identity/field ambiguity and
// restores the exact bounded body bytes used by the HMAC verifier.
func NetworkHTTPFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsNetworkPath(r.URL.Path) || r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			khttp.DefaultErrorEncoder(w, r, errors.BadRequest("INVALID_NETWORK_REQUEST", "invalid Network HTTP request"))
		}
		operation, message, pathField, id := networkHTTPRoute(r)
		if operation == "" || message == nil || r.URL.RawPath != "" || r.URL.ForceQuery || strings.Contains(r.URL.EscapedPath(), "%") || len(r.URL.RawQuery) > 8192 {
			fail()
			return
		}
		for _, header := range []string{"X-Ani-Tenant-Id", "X-Tenant-Id", "X-Ani-Actor", "X-Actor", "X-Ani-Operator", "X-Operator", "X-Operator-Id", "X-Ani-Request-Id", "X-Resource-Tenant-Id", "X-Platform-Admin", "X-Is-Platform-Admin", "X-Ani-Scope"} {
			if len(r.Header.Values(header)) != 0 {
				fail()
				return
			}
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || !validateNetworkQuery(operation, query) {
			fail()
			return
		}
		var body []byte
		if r.Body != nil {
			body, err = io.ReadAll(io.LimitReader(r.Body, 65537))
			_ = r.Body.Close()
			if err != nil || len(body) > 65536 {
				fail()
				return
			}
		}
		if r.Method == "POST" || r.Method == "PATCH" {
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || media != "application/json" || len(body) == 0 || len(r.Header.Values("Content-Type")) != 1 {
				fail()
				return
			}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(body, message); err != nil {
				fail()
				return
			}
			m := message.ProtoReflect()
			if !validateNetworkMessage(m) || (IsPlatformNetworkOperation(operation) && !validPlatformNetworkInput(message)) {
				fail()
				return
			}
			fields := m.Descriptor().Fields()
			if pathField != "" {
				f := fields.ByName(protoreflect.Name(pathField))
				if f == nil || (m.Get(f).String() != "" && m.Get(f).String() != id) {
					fail()
					return
				}
			}
		} else if len(body) != 0 || r.ContentLength > 0 || len(r.TransferEncoding) != 0 {
			fail()
			return
		}
		sum := sha256.Sum256(body)
		digest := networkRequestDigest{operation, r.Method, r.URL.Path, r.URL.RawQuery, hex.EncodeToString(sum[:])}
		r = r.WithContext(context.WithValue(r.Context(), networkRequestDigestKey{}, digest))
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

func ValidateNetworkRequest(r *http.Request, operation string) error {
	digest, ok := r.Context().Value(networkRequestDigestKey{}).(networkRequestDigest)
	if !ok && operation == VPCReadOperation {
		return ValidateVPCReadRequest(r)
	}
	if !ok || digest.Operation != operation || digest.Method != r.Method || digest.Path != r.URL.Path || digest.Query != r.URL.RawQuery {
		return errors.BadRequest("INVALID_NETWORK_REQUEST", "Network HTTP request was not validated")
	}
	return nil
}

// Network signatures use the Image canonical form and sign the raw body bytes.
// Query must be url.Values.Encode(); the GetVPC empty-query vector is unchanged.
func NetworkSignature(secret, method, path, query, accessKey, timestamp string, body []byte) string {
	return ImageSignature(secret, method, path, query, accessKey, timestamp, body)
}
