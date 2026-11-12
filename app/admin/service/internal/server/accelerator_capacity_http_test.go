package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	admin "go-wind-admin/api/gen/go/admin/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
)

type capacityACCProtocol struct {
	acc.AcceleratorAdminServiceClient
	requests chan *acc.AdminRead
}

func (c *capacityACCProtocol) GetProfileCapacity(_ context.Context, r *acc.GetProfileCapacityRequest, _ ...grpc.CallOption) (*acc.ProfileCapacity, error) {
	c.requests <- r.Context
	return &acc.ProfileCapacity{ProfileId: r.ProfileId, ProfileVersion: r.ProfileVersion, AllocationType: acc.AllocationType_FIXED_PARTITION, ResourceKey: "hygon.com/hcu-share-8c-16g", Basis: acc.CapacityBasis_BOUND_POD_REQUEST, Granularity: acc.CapacityGranularity_NODE_RESOURCE, NominalUnits: proto.Int64(0), Totals: &acc.CapacityTotals{TotalUnits: proto.Int64(9007199254740993)}, Details: []*acc.CapacityDetail{{NodeUid: "node-uid", NodeName: "node1", NominalUnits: proto.Int64(0), Totals: &acc.CapacityTotals{AllocatedUnits: proto.Int64(0)}}}}, nil
}
func (c *capacityACCProtocol) GetSupplyGroupCapacity(_ context.Context, r *acc.GetSupplyGroupCapacityRequest, _ ...grpc.CallOption) (*acc.SupplyGroupCapacity, error) {
	c.requests <- r.Context
	return &acc.SupplyGroupCapacity{GroupId: r.GroupId, PhysicalDeviceCount: proto.Int64(0), ResourceTypes: []*acc.ResourceTypeCapacity{{AllocationType: acc.AllocationType_FIXED_PARTITION, ResourceKey: "hygon.com/hcu-share-8c-16g", Basis: acc.CapacityBasis_BOUND_POD_REQUEST, Granularity: acc.CapacityGranularity_NODE_RESOURCE, NominalUnits: proto.Int64(0), Details: []*acc.CapacityDetail{{NodeUid: "node-uid", NodeName: "node1", EstimatedAvailableUnits: proto.Int64(0)}}}}}, nil
}

// The fixture supplies the authenticated Principal context. Only the external
// ACC protocol is replaced; the real BFF, mapper, middleware dispatch and custom
// HTTP registration/encoder execute. JWT/Casbin integration is a separate gate.
func capacityHTTPFixture(t *testing.T, principal *auth.Principal) (*khttp.Server, *capacityACCProtocol, *[]string) {
	t.Helper()
	peer := &capacityACCProtocol{requests: make(chan *acc.AdminRead, 4)}
	operations := []string{}
	svc := service.NewAcceleratorService(&data.AcceleratorClient{Admin: peer}, nil, nil, nil, nil)
	server := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, in any) (any, error) {
			if tr, ok := transport.FromServerContext(ctx); ok {
				operations = append(operations, tr.Operation())
			}
			if principal == nil {
				return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
			}
			return next(auth.NewPrincipalContext(ctx, principal), in)
		}
	}))
	RegisterAcceleratorCapacityHTTPServer(server, svc)
	return server, peer, &operations
}
func capacityBody(t *testing.T, server *khttp.Server, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
	body := map[string]any{}
	if response.Code == 200 {
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	return response, body
}
func requireNullCapacityField(t *testing.T, body map[string]any, key string) {
	t.Helper()
	value, present := body[key]
	if !present || value != nil {
		t.Fatalf("%s must be explicit null: %#v", key, body)
	}
}

func TestAcceleratorCapacityHTTPPreservesNestedNullAndExactStrings(t *testing.T) {
	server, peer, operations := capacityHTTPFixture(t, &auth.Principal{Type: auth.SubjectUser, ID: 42})
	id := "11111111-1111-4111-8111-111111111111"
	response, body := capacityBody(t, server, "/api/v1/admin/accelerator/profiles/"+id+"/capacity?profile_version=9007199254740993&include_details=true")
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/json" {
		t.Fatal(response.Code, response.Body.String())
	}
	if body["profile_version"] != "9007199254740993" || body["nominal_units"] != "0" || body["basis"] != "BOUND_POD_REQUEST" || body["granularity"] != "NODE_RESOURCE" {
		t.Fatal(body)
	}
	requireNullCapacityField(t, body, "estimated_available_units")
	totals := body["totals"].(map[string]any)
	if totals["total_units"] != "9007199254740993" {
		t.Fatal(totals)
	}
	for _, key := range []string{"allocated_units", "unallocated_units", "total_memory_mib", "allocated_memory_mib", "unallocated_memory_mib"} {
		requireNullCapacityField(t, totals, key)
	}
	detail := body["details"].([]any)[0].(map[string]any)
	if detail["nominal_units"] != "0" || detail["device_uid"] != "" {
		t.Fatal(detail)
	}
	requireNullCapacityField(t, detail, "estimated_available_units")
	rowTotals := detail["totals"].(map[string]any)
	if rowTotals["allocated_units"] != "0" {
		t.Fatal(rowTotals)
	}
	requireNullCapacityField(t, rowTotals, "total_units")
	if len(body["missing_fields"].([]any)) != 0 {
		t.Fatal(body)
	}
	trusted := <-peer.requests
	if trusted.Actor.GetId() != "42" || trusted.RequestId == "" {
		t.Fatal(trusted)
	}
	response, body = capacityBody(t, server, "/api/v1/admin/accelerator/supply-groups/"+id+"/capacity?include_details=true")
	if response.Code != 200 || body["physical_device_count"] != "0" {
		t.Fatal(response.Code, body)
	}
	kind := body["resource_types"].([]any)[0].(map[string]any)
	if kind["nominal_units"] != "0" {
		t.Fatal(kind)
	}
	requireNullCapacityField(t, kind, "estimated_available_units")
	for _, key := range []string{"total_units", "allocated_units", "unallocated_units", "total_memory_mib", "allocated_memory_mib", "unallocated_memory_mib"} {
		requireNullCapacityField(t, kind["totals"].(map[string]any), key)
	}
	detail = kind["details"].([]any)[0].(map[string]any)
	if detail["estimated_available_units"] != "0" {
		t.Fatal(detail)
	}
	requireNullCapacityField(t, detail, "nominal_units")
	if len(*operations) != 2 || (*operations)[0] != admin.OperationAcceleratorCapacityAdminServiceGetProfileCapacity || (*operations)[1] != admin.OperationAcceleratorCapacityAdminServiceGetSupplyGroupCapacity {
		t.Fatal(*operations)
	}
	count := map[string]int{}
	if err := server.WalkRoute(func(info khttp.RouteInfo) error { count[info.Method+" "+info.Path]++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count["GET "+acceleratorProfileCapacityPath] != 1 || count["GET "+acceleratorSupplyCapacityPath] != 1 || len(count) != 2 {
		t.Fatal(count)
	}
}

func TestAcceleratorCapacityHTTPRejectsAmbiguousQueriesAndTenantScope(t *testing.T) {
	server, peer, _ := capacityHTTPFixture(t, &auth.Principal{Type: auth.SubjectUser, ID: 42})
	base := "/api/v1/admin/accelerator/profiles/11111111-1111-4111-8111-111111111111/capacity"
	for _, query := range []string{"", "?profile_version=0", "?profile_version=01", "?profile_version=1&profile_version=2", "?profile_version=18446744073709551616", "?profile_version=1&include_details=1", "?profile_version=1&tenant_id=forged", "?profile_version=1&profile_id=forged"} {
		response, _ := capacityBody(t, server, base+query)
		if response.Code != 400 {
			t.Fatalf("query %s: %d %s", query, response.Code, response.Body.String())
		}
	}
	if len(peer.requests) != 0 {
		t.Fatal("invalid query reached ACC")
	}
	req := httptest.NewRequest("GET", base+"?profile_version=1", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != 400 {
		t.Fatal(response.Code)
	}
	for _, p := range []*auth.Principal{nil, {Type: auth.SubjectUser, ID: 42, TenantID: 1}, {Type: auth.SubjectAPIKey, ID: 42}} {
		scoped, boundary, _ := capacityHTTPFixture(t, p)
		response, _ := capacityBody(t, scoped, base+"?profile_version=1")
		if response.Code != 401 && response.Code != 403 {
			t.Fatal(response.Code, response.Body.String())
		}
		if len(boundary.requests) != 0 {
			t.Fatal("untrusted principal reached ACC")
		}
	}
	for _, path := range []string{"/api/v1/admin/accelerator/capacity", "/api/v1/admin/accelerator/bindings", "/api/v1/accelerator/capacity", "/api/v1/accelerator/usages"} {
		response, _ := capacityBody(t, server, path)
		if response.Code != http.StatusNotFound {
			t.Fatal(path, response.Code)
		}
	}
}
