package auth

import (
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"google.golang.org/protobuf/proto"
	"net/http"
	"regexp"
	"strings"
)

// Registration is not a grant. User JWT, platform scope, action and Casbin apply.
var PlatformNetworkOperations = map[string]NetworkOperationPolicy{
	"/admin.service.v1.PlatformNetworkService/ListPlatformEIPs":                 {"GET", "/api/v1/admin/networks/eips", "network:platform:eip:list", true},
	"/admin.service.v1.PlatformNetworkService/GetPlatformEIP":                   {"GET", "/api/v1/admin/networks/eips/{eip_id}", "network:platform:eip:get", true},
	"/admin.service.v1.PlatformNetworkService/ReleasePlatformEIP":               {"DELETE", "/api/v1/admin/networks/eips/{eip_id}", "network:platform:eip:release", false},
	"/admin.service.v1.PlatformNetworkService/CreateDefaultVPCEgress":           {"POST", "/api/v1/admin/networks/default-vpc-egresses", "network:platform:default-egress:create", false},
	"/admin.service.v1.PlatformNetworkService/GetDefaultVPCEgress":              {"GET", "/api/v1/admin/networks/default-vpc-egresses/{egress_id}", "network:platform:default-egress:get", true},
	"/admin.service.v1.PlatformNetworkService/GetCurrentDefaultVPCEgress":       {"GET", "/api/v1/admin/networks/default-vpc-egress", "network:platform:default-egress:get", true},
	"/admin.service.v1.PlatformNetworkService/DeleteDefaultVPCEgress":           {"DELETE", "/api/v1/admin/networks/default-vpc-egresses/{egress_id}", "network:platform:default-egress:delete", false},
	"/admin.service.v1.PlatformNetworkService/RetireNetworkDevice":              {"POST", "/api/v1/admin/networks/devices/{device_id}/retirement", "network:platform:device-retirement:create", false},
	"/admin.service.v1.PlatformNetworkService/GetNetworkDeviceRetirement":       {"GET", "/api/v1/admin/networks/devices/{device_id}/retirement", "network:platform:device-retirement:get", true},
	"/admin.service.v1.PlatformNetworkService/CreateVirtualSwitch":              {"POST", "/api/v1/admin/networks/virtual-switches", "network:platform:virtual-switch:create", false},
	"/admin.service.v1.PlatformNetworkService/GetVirtualSwitch":                 {"GET", "/api/v1/admin/networks/virtual-switches/{virtual_switch_id}", "network:platform:virtual-switch:get", true},
	"/admin.service.v1.PlatformNetworkService/ListVirtualSwitches":              {"GET", "/api/v1/admin/networks/virtual-switches", "network:platform:virtual-switch:list", true},
	"/admin.service.v1.PlatformNetworkService/DeleteVirtualSwitch":              {"DELETE", "/api/v1/admin/networks/virtual-switches/{virtual_switch_id}", "network:platform:virtual-switch:delete", false},
	"/admin.service.v1.PlatformNetworkService/ListNodeInterfaces":               {"GET", "/api/v1/admin/networks/node-interfaces", "network:platform:device:list", true},
	"/admin.service.v1.PlatformNetworkService/AdoptNetworkDevice":               {"POST", "/api/v1/admin/networks/devices", "network:platform:device:adopt", false},
	"/admin.service.v1.PlatformNetworkService/GetNetworkDevice":                 {"GET", "/api/v1/admin/networks/devices/{device_id}", "network:platform:device:get", true},
	"/admin.service.v1.PlatformNetworkService/CreateVlanNetwork":                {"POST", "/api/v1/admin/networks/vlan-networks", "network:platform:vlan-network:create", false},
	"/admin.service.v1.PlatformNetworkService/GetVlanNetwork":                   {"GET", "/api/v1/admin/networks/vlan-networks/{vlan_network_id}", "network:platform:vlan-network:get", true},
	"/admin.service.v1.PlatformNetworkService/ListVlanNetworks":                 {"GET", "/api/v1/admin/networks/vlan-networks", "network:platform:vlan-network:list", true},
	"/admin.service.v1.PlatformNetworkService/DeleteVlanNetwork":                {"DELETE", "/api/v1/admin/networks/vlan-networks/{vlan_network_id}", "network:platform:vlan-network:delete", false},
	"/admin.service.v1.PlatformNetworkService/CreateEgressGateway":              {"POST", "/api/v1/admin/networks/egress-gateways", "network:platform:egress-gateway:create", false},
	"/admin.service.v1.PlatformNetworkService/GetEgressGateway":                 {"GET", "/api/v1/admin/networks/egress-gateways/{gateway_id}", "network:platform:egress-gateway:get", true},
	"/admin.service.v1.PlatformNetworkService/ListEgressGateways":               {"GET", "/api/v1/admin/networks/egress-gateways", "network:platform:egress-gateway:list", true},
	"/admin.service.v1.PlatformNetworkService/DeleteEgressGateway":              {"DELETE", "/api/v1/admin/networks/egress-gateways/{gateway_id}", "network:platform:egress-gateway:delete", false},
	"/admin.service.v1.PlatformNetworkService/CreatePublicAddressPool":          {"POST", "/api/v1/admin/networks/public-address-pools", "network:platform:public-pool:create", false},
	"/admin.service.v1.PlatformNetworkService/GetPublicAddressPool":             {"GET", "/api/v1/admin/networks/public-address-pools/{pool_id}", "network:platform:public-pool:get", true},
	"/admin.service.v1.PlatformNetworkService/ListPublicAddressPools":           {"GET", "/api/v1/admin/networks/public-address-pools", "network:platform:public-pool:list", true},
	"/admin.service.v1.PlatformNetworkService/DeletePublicAddressPool":          {"DELETE", "/api/v1/admin/networks/public-address-pools/{pool_id}", "network:platform:public-pool:delete", false},
	"/admin.service.v1.PlatformNetworkService/RecordPublicPoolVerification":     {"POST", "/api/v1/admin/networks/public-address-pools/{pool_id}/verification", "network:platform:public-pool:verify", false},
	"/admin.service.v1.PlatformNetworkService/SetPublicPoolAllocationEnabled":   {"PATCH", "/api/v1/admin/networks/public-address-pools/{pool_id}/allocation", "network:platform:public-pool:allocate", false},
	"/admin.service.v1.PlatformNetworkService/SetDefaultPublicPool":             {"POST", "/api/v1/admin/networks/public-address-pools/{pool_id}/default", "network:platform:public-pool:default", false},
	"/admin.service.v1.PlatformNetworkService/CreateIntranetAddressPool":        {"POST", "/api/v1/admin/networks/intranet-address-pools", "network:platform:intranet-pool:create", false},
	"/admin.service.v1.PlatformNetworkService/GetIntranetAddressPool":           {"GET", "/api/v1/admin/networks/intranet-address-pools/{pool_id}", "network:platform:intranet-pool:get", true},
	"/admin.service.v1.PlatformNetworkService/ListIntranetAddressPools":         {"GET", "/api/v1/admin/networks/intranet-address-pools", "network:platform:intranet-pool:list", true},
	"/admin.service.v1.PlatformNetworkService/DeleteIntranetAddressPool":        {"DELETE", "/api/v1/admin/networks/intranet-address-pools/{pool_id}", "network:platform:intranet-pool:delete", false},
	"/admin.service.v1.PlatformNetworkService/RecordIntranetPoolVerification":   {"POST", "/api/v1/admin/networks/intranet-address-pools/{pool_id}/verification", "network:platform:intranet-pool:verify", false},
	"/admin.service.v1.PlatformNetworkService/SetIntranetPoolAllocationEnabled": {"PATCH", "/api/v1/admin/networks/intranet-address-pools/{pool_id}/allocation", "network:platform:intranet-pool:allocate", false},
	"/admin.service.v1.PlatformNetworkService/SetDefaultIntranetPool":           {"POST", "/api/v1/admin/networks/intranet-address-pools/{pool_id}/default", "network:platform:intranet-pool:default", false},
	"/admin.service.v1.PlatformNetworkService/GetPlatformNetworkCapabilities":   {"GET", "/api/v1/admin/networks/capabilities", "network:platform:capability:get", true},
	"/admin.service.v1.PlatformNetworkService/GetPlatformOperation":             {"GET", "/api/v1/admin/networks/operations/{operation_id}", "network:platform:operation:get", true},
}

func IsPlatformNetworkOperation(operation string) bool {
	_, ok := PlatformNetworkOperations[operation]
	return ok
}

var platformNetworkIDPatterns = map[string]*regexp.Regexp{
	"eip_id":            regexp.MustCompile(`^eip_[0-9a-f]{32}$`),
	"egress_id":         regexp.MustCompile(`^dve_[0-9a-f]{32}$`),
	"virtual_switch_id": regexp.MustCompile(`^vsw_[0-9a-f]{32}$`),
	"pool_id":           regexp.MustCompile(`^pool_[0-9a-f]{32}$`),
	"gateway_id":        regexp.MustCompile(`^egw_[0-9a-f]{32}$`),
	"vlan_network_id":   regexp.MustCompile(`^vlan_[0-9a-f]{32}$`),
	"device_id":         regexp.MustCompile(`^device_[0-9a-f]{32}$`),
	"operation_id":      regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`),
}

func ValidPlatformNetworkID(field, value string) bool {
	p := platformNetworkIDPatterns[field]
	return p != nil && p.MatchString(value)
}
func platformNetworkMessage(operation string) proto.Message {
	switch strings.TrimPrefix(operation, "/admin.service.v1.PlatformNetworkService/") {
	case "ListPlatformEIPs":
		return &view.ListPlatformEIPsRequest{}
	case "GetPlatformEIP":
		return &view.GetPlatformEIPRequest{}
	case "ReleasePlatformEIP":
		return &view.ReleasePlatformEIPRequest{}
	case "CreateDefaultVPCEgress":
		return &view.CreateDefaultVPCEgressRequest{}
	case "GetDefaultVPCEgress":
		return &view.GetDefaultVPCEgressRequest{}
	case "GetCurrentDefaultVPCEgress":
		return &view.GetCurrentDefaultVPCEgressRequest{}
	case "DeleteDefaultVPCEgress":
		return &view.DeleteDefaultVPCEgressRequest{}
	case "RetireNetworkDevice":
		return &view.RetireNetworkDeviceRequest{}
	case "GetNetworkDeviceRetirement":
		return &view.GetNetworkDeviceRetirementRequest{}
	case "CreateVirtualSwitch":
		return &view.CreateVirtualSwitchRequest{}
	case "GetVirtualSwitch":
		return &view.GetVirtualSwitchRequest{}
	case "ListVirtualSwitches":
		return &view.ListVirtualSwitchesRequest{}
	case "DeleteVirtualSwitch":
		return &view.DeleteVirtualSwitchRequest{}
	case "ListNodeInterfaces":
		return &view.ListNodeInterfacesRequest{}
	case "AdoptNetworkDevice":
		return &view.AdoptNetworkDeviceRequest{}
	case "GetNetworkDevice":
		return &view.GetNetworkDeviceRequest{}
	case "CreateVlanNetwork":
		return &view.CreateVlanNetworkRequest{}
	case "GetVlanNetwork":
		return &view.GetVlanNetworkRequest{}
	case "ListVlanNetworks":
		return &view.ListVlanNetworksRequest{}
	case "DeleteVlanNetwork":
		return &view.DeleteVlanNetworkRequest{}
	case "CreateEgressGateway":
		return &view.CreateEgressGatewayRequest{}
	case "GetEgressGateway":
		return &view.GetEgressGatewayRequest{}
	case "ListEgressGateways":
		return &view.ListEgressGatewaysRequest{}
	case "DeleteEgressGateway":
		return &view.DeleteEgressGatewayRequest{}
	case "CreatePublicAddressPool":
		return &view.CreatePublicAddressPoolRequest{}
	case "GetPublicAddressPool":
		return &view.GetPublicAddressPoolRequest{}
	case "ListPublicAddressPools":
		return &view.ListPublicAddressPoolsRequest{}
	case "DeletePublicAddressPool":
		return &view.DeletePublicAddressPoolRequest{}
	case "RecordPublicPoolVerification":
		return &view.RecordPublicPoolVerificationRequest{}
	case "SetPublicPoolAllocationEnabled":
		return &view.SetPublicPoolAllocationEnabledRequest{}
	case "SetDefaultPublicPool":
		return &view.SetDefaultPublicPoolRequest{}
	case "CreateIntranetAddressPool":
		return &view.CreateIntranetAddressPoolRequest{}
	case "GetIntranetAddressPool":
		return &view.GetIntranetAddressPoolRequest{}
	case "ListIntranetAddressPools":
		return &view.ListIntranetAddressPoolsRequest{}
	case "DeleteIntranetAddressPool":
		return &view.DeleteIntranetAddressPoolRequest{}
	case "RecordIntranetPoolVerification":
		return &view.RecordIntranetPoolVerificationRequest{}
	case "SetIntranetPoolAllocationEnabled":
		return &view.SetIntranetPoolAllocationEnabledRequest{}
	case "SetDefaultIntranetPool":
		return &view.SetDefaultIntranetPoolRequest{}
	case "GetPlatformNetworkCapabilities":
		return &view.GetPlatformNetworkCapabilitiesRequest{}
	case "GetPlatformOperation":
		return &view.GetPlatformOperationRequest{}
	}
	return nil
}
func platformNetworkHTTPRoute(r *http.Request) (string, proto.Message, string, string) {
	for op, p := range PlatformNetworkOperations {
		if p.Method != r.Method {
			continue
		}
		start := strings.IndexByte(p.Path, '{')
		if start < 0 {
			if p.Path == r.URL.Path {
				return op, platformNetworkMessage(op), "", ""
			}
			continue
		}
		end := strings.IndexByte(p.Path, '}')
		prefix, suffix := p.Path[:start], p.Path[end+1:]
		if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) || len(r.URL.Path) < len(prefix)+len(suffix) {
			continue
		}
		field := p.Path[start+1 : end]
		id := r.URL.Path[len(prefix) : len(r.URL.Path)-len(suffix)]
		if ValidPlatformNetworkID(field, id) {
			return op, platformNetworkMessage(op), field, id
		}
	}
	return "", nil, "", ""
}
func platformNetworkQueryFields(operation string) map[string]bool {
	fields := map[string]bool{}
	switch strings.TrimPrefix(operation, "/admin.service.v1.PlatformNetworkService/") {
	case "ListPlatformEIPs":
		for _, f := range []string{"page", "pageSize", "tenant_id", "name", "address", "pool_id", "state", "scope"} {
			fields[f] = true
		}
	case "ListVirtualSwitches":
		for _, f := range []string{"name", "state", "page", "pageSize"} {
			fields[f] = true
		}
	case "ListNodeInterfaces":
		for _, f := range []string{"node_name", "cluster_id", "virtual_switch_id"} {
			fields[f] = true
		}
	case "ListVlanNetworks", "ListEgressGateways", "ListPublicAddressPools", "ListIntranetAddressPools":
		for _, f := range []string{"name", "state", "limit", "cursor"} {
			fields[f] = true
		}
	}
	return fields
}

// The v1 device request remains replayable, but cannot be mixed with v2 binding.
func validPlatformNetworkInput(message proto.Message) bool {
	switch req := message.(type) {
	case *view.CreateVlanNetworkRequest:
		if req.Data == nil {
			return false
		}
		d := req.Data
		v2 := d.ClusterId != "" || d.VirtualSwitchId != "" || d.DeviceName != ""
		return (!v2 && d.DeviceId != "") || (v2 && d.DeviceId == "" && d.ClusterId != "" && d.VirtualSwitchId != "" && d.DeviceName != "")
	}
	return true
}
