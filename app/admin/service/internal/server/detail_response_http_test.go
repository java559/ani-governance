package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/cmd/server/assets"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// This transport test substitutes only the service reply. It exercises the
// generated routes and the production ModelDev route adapters, not business
// authentication or persistence. Authenticated Image/PG coverage lives in the
// Image joint integration suite.
func TestDetailHTTPResponsesReturnResourceObjects(t *testing.T) {
	tests := []struct {
		name, path, wrapper, key, value string
		response                        proto.Message
	}{
		{"VPC", "/api/v1/networks/vpcs/vpc_x", "vpc", "id", "vpc_x", &catalogv1.GetVPCResponse{Vpc: &catalogv1.VPC{Id: "vpc_x"}}},
		{"EIP", "/api/v1/networks/eips/eip_x", "eip", "id", "eip_x", &catalogv1.GetEIPResponse{Eip: &catalogv1.EIP{Id: "eip_x"}}},
		{"Operation", "/api/v1/networks/operations/op_x", "operation", "id", "op_x", &catalogv1.GetOperationResponse{Operation: &catalogv1.Operation{Id: "op_x"}}},
		{"SNAT", "/api/v1/networks/vpcs/vpc_x/snat", "snat", "id", "snat_x", &catalogv1.GetVPCSnatResponse{Snat: &catalogv1.VPCSnat{Id: "snat_x"}}},
		{"ImageSpace", "/api/v1/images/space", "space", "spaceId", "space_x", &catalogv1.GetImageSpaceResponse{Space: &catalogv1.ImageSpace{SpaceId: "space_x"}}},
		{"PublisherCredential", "/api/v1/images/publisher-credential", "credential", "username", "publisher_x", &catalogv1.GetPublisherCredentialResponse{Credential: &catalogv1.PublisherCredential{Username: "publisher_x"}}},
		{"Image", "/api/v1/images/registrations/img_x?scope=tenant", "image", "image_id", "img_x", &catalogv1.GetImageResponse{Image: &catalogv1.ImageRegistration{ImageId: "img_x"}}},
		{"InputVersion", "/api/v1/modeldev/input-versions/input_x", "input_version", "input_version_id", "input_x", &modeldevv1.GetInputVersionResponse{InputVersion: &modeldevv1.InputVersionView{InputVersionId: "input_x"}}},
		{"Execution", "/api/v1/modeldev/executions/execution_x", "execution", "execution_id", "execution_x", &modeldevv1.GetExecutionResponse{Execution: &modeldevv1.ExecutionView{ExecutionId: "execution_x"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			srv := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
				return func(ctx context.Context, request interface{}) (interface{}, error) {
					calls++
					return test.response, nil
				}
			}))
			adminv1.RegisterNetworkServiceHTTPServer(srv, nil)
			adminv1.RegisterImageServiceHTTPServer(srv, nil)
			registerModelDevHTTP(srv, nil)
			web := httptest.NewServer(srv)
			defer web.Close()
			response, err := web.Client().Get(web.URL + test.path)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, 1, calls)
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
			require.Equal(t, test.value, body[test.key])
			require.NotContains(t, body, test.wrapper)
			require.NotContains(t, body, "tenant_id")

			denied := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
				return func(ctx context.Context, request interface{}) (interface{}, error) {
					return nil, errors.Unauthorized("DETAIL_AUTH_REQUIRED", "login required")
				}
			}))
			adminv1.RegisterNetworkServiceHTTPServer(denied, nil)
			adminv1.RegisterImageServiceHTTPServer(denied, nil)
			registerModelDevHTTP(denied, nil)
			deniedWeb := httptest.NewServer(denied)
			defer deniedWeb.Close()
			deniedResponse, err := deniedWeb.Client().Get(deniedWeb.URL + test.path)
			require.NoError(t, err)
			defer deniedResponse.Body.Close()
			require.Equal(t, http.StatusUnauthorized, deniedResponse.StatusCode)
		})
	}
}

func TestDetailOpenAPIResponsesDescribeResourceObjects(t *testing.T) {
	var document struct {
		Paths map[string]struct {
			Get struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema struct {
							Ref string `yaml:"$ref"`
						} `yaml:"schema"`
					} `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"get"`
		} `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(assets.OpenApiData, &document))
	for path, schema := range map[string]string{
		"/api/v1/networks/vpcs/{vpc_id}":                       "VPC",
		"/api/v1/networks/eips/{eip_id}":                       "EIP",
		"/api/v1/networks/operations/{operation_id}":           "NetworkOperation",
		"/api/v1/networks/vpcs/{vpc_id}/snat":                  "VPCSnat",
		"/api/v1/images/space":                                 "ImageSpace",
		"/api/v1/images/publisher-credential":                  "PublisherCredential",
		"/api/v1/images/registrations/{image_id}":              "ImageRegistration",
		"/api/v1/modeldev/input-versions/{input_version_id}": "InputVersionView",
		"/api/v1/modeldev/executions/{execution_id}":         "ExecutionView",
	} {
		t.Run(schema, func(t *testing.T) {
			require.Equal(t, "#/components/schemas/"+schema, document.Paths[path].Get.Responses["200"].Content["application/json"].Schema.Ref)
		})
	}
}
