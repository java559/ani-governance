package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	admin "go-wind-admin/api/gen/go/admin/service/v1"
)

func TestQuotaAdminEnvelopeWireContract(t *testing.T) {
	cases := []struct {
		message protoreflect.MessageDescriptor
		name    protoreflect.FullName
		fields  []struct {
			name, jsonName string
			number         protoreflect.FieldNumber
			kind           protoreflect.Kind
			list           bool
			message        protoreflect.FullName
		}
	}{
		{
			message: (&admin.ListQuotaDefinitionsResponse{}).ProtoReflect().Descriptor(),
			name:    "admin.service.v1.ListQuotaDefinitionsResponse",
			fields: []struct {
				name, jsonName string
				number         protoreflect.FieldNumber
				kind           protoreflect.Kind
				list           bool
				message        protoreflect.FullName
			}{
				{"items", "items", 1, protoreflect.MessageKind, true, "quota.service.v1.QuotaDefinition"},
				{"total", "total", 2, protoreflect.Uint64Kind, false, ""},
			},
		},
		{
			message: (&admin.GetTenantQuotaAccountsRequest{}).ProtoReflect().Descriptor(),
			name:    "admin.service.v1.GetTenantQuotaAccountsRequest",
			fields: []struct {
				name, jsonName string
				number         protoreflect.FieldNumber
				kind           protoreflect.Kind
				list           bool
				message        protoreflect.FullName
			}{
				{"id", "id", 1, protoreflect.Uint32Kind, false, ""},
			},
		},
		{
			message: (&admin.ListTenantQuotaAccountsResponse{}).ProtoReflect().Descriptor(),
			name:    "admin.service.v1.ListTenantQuotaAccountsResponse",
			fields: []struct {
				name, jsonName string
				number         protoreflect.FieldNumber
				kind           protoreflect.Kind
				list           bool
				message        protoreflect.FullName
			}{
				{"tenant_id", "tenantId", 1, protoreflect.Uint32Kind, false, ""},
				{"items", "items", 2, protoreflect.MessageKind, true, "quota.service.v1.QuotaAccountView"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(string(tc.name), func(t *testing.T) {
			require.Equal(t, tc.name, tc.message.FullName())
			require.Equal(t, len(tc.fields), tc.message.Fields().Len())
			for _, expected := range tc.fields {
				f := tc.message.Fields().ByName(protoreflect.Name(expected.name))
				require.NotNil(t, f)
				require.Equal(t, expected.number, f.Number())
				require.Equal(t, expected.kind, f.Kind())
				require.Equal(t, expected.jsonName, f.JSONName())
				require.Equal(t, expected.list, f.IsList())
				require.False(t, f.HasOptionalKeyword())
				require.Nil(t, f.ContainingOneof())
				if expected.message != "" {
					require.Equal(t, expected.message, f.Message().FullName())
				}
			}
		})
	}
	raw, err := protojson.Marshal(&admin.ListQuotaDefinitionsResponse{Total: 42})
	require.NoError(t, err)
	require.JSONEq(t, `{"total":"42"}`, string(raw))
	raw, err = protojson.Marshal(&admin.ListTenantQuotaAccountsResponse{TenantId: 42})
	require.NoError(t, err)
	require.JSONEq(t, `{"tenantId":42}`, string(raw))
}

func TestQuotaAdminHTTPAndRPCContract(t *testing.T) {
	cases := []struct {
		service, method, input, output, path string
	}{
		{"admin.service.v1.QuotaAdminService", "ListQuotaDefinitions", "pagination.PagingRequest", "admin.service.v1.ListQuotaDefinitionsResponse", "/api/v1/admin/quota-definitions"},
		{"admin.service.v1.QuotaAdminService", "ListTenantQuotaAccounts", "admin.service.v1.GetTenantQuotaAccountsRequest", "admin.service.v1.ListTenantQuotaAccountsResponse", "/api/v1/admin/tenants/{id}/quota-accounts"},
		{"admin.service.v1.QuotaSelfService", "GetMyQuotaAccounts", "google.protobuf.Empty", "admin.service.v1.ListTenantQuotaAccountsResponse", "/api/v1/me/quota-accounts"},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(tc.service))
			require.NoError(t, err)
			svc, ok := d.(protoreflect.ServiceDescriptor)
			require.True(t, ok)
			method := svc.Methods().ByName(protoreflect.Name(tc.method))
			require.NotNil(t, method)
			require.Equal(t, tc.service+"."+tc.method, string(method.FullName()))
			require.Equal(t, tc.input, string(method.Input().FullName()))
			require.Equal(t, tc.output, string(method.Output().FullName()))
			opts, ok := method.Options().(*descriptorpb.MethodOptions)
			require.True(t, ok)
			require.True(t, proto.HasExtension(opts, annotations.E_Http))
			rule, ok := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
			require.True(t, ok)
			require.Equal(t, tc.path, rule.GetGet())
			require.Empty(t, rule.GetBody())
		})
	}
}
