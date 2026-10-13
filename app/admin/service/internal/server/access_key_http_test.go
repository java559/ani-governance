package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	accesskeyV1 "go-wind-admin/api/gen/go/access_key/service/v1"
	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"google.golang.org/protobuf/types/known/emptypb"
)

type keyHTTPProbe struct {
	adminV1.AccessKeyServiceHTTPServer
	created *accesskeyV1.CreateAccessKeyRequest
	updated *accesskeyV1.UpdateAccessKeyRequest
	deleted uint32
}

func (p *keyHTTPProbe) Create(_ context.Context, in *accesskeyV1.CreateAccessKeyRequest) (*accesskeyV1.CreateAccessKeyResponse, error) {
	p.created = in
	return &accesskeyV1.CreateAccessKeyResponse{Data: &accesskeyV1.AccessKey{Id: trans.Ptr(uint32(42)), RoleId: in.Data.RoleId, Name: in.Data.Name, AccessKey: trans.Ptr("ak-public-fixture"), IsActive: trans.Ptr(true), RoleName: trans.Ptr("reader")}, SecretKey: "sk-public-test-fixture"}, nil
}
func (p *keyHTTPProbe) Update(_ context.Context, in *accesskeyV1.UpdateAccessKeyRequest) (*emptypb.Empty, error) {
	p.updated = in
	return &emptypb.Empty{}, nil
}
func (p *keyHTTPProbe) Delete(_ context.Context, in *accesskeyV1.DeleteAccessKeyRequest) (*accesskeyV1.DeleteAccessKeyResponse, error) {
	p.deleted = in.GetKeyId()
	return &accesskeyV1.DeleteAccessKeyResponse{Status: "revoked"}, nil
}
func fireKeyHTTP(s *http.Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestAccessKeyHTTPCreateAndDeleteContract(t *testing.T) {
	probe := &keyHTTPProbe{}
	s := http.NewServer()
	registerAccessKeyHTTP(s, probe)
	w := fireKeyHTTP(s, "POST", "/api/v1/auth/api-keys", `{"data":{"name":"reader","role_id":12}}`)
	require.Equal(t, 201, w.Code, w.Body.String())
	require.Equal(t, uint32(12), probe.created.GetData().GetRoleId())
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "sk-public-test-fixture", body["secret_key"])
	require.NotContains(t, body, "secretKey")
	data := body["data"].(map[string]interface{})
	require.Equal(t, float64(42), data["id"])
	require.Equal(t, float64(12), data["role_id"])
	require.Equal(t, "ak-public-fixture", data["access_key"])
	require.Equal(t, true, data["is_active"])
	require.Equal(t, "reader", data["role_name"])
	require.NotContains(t, data, "roleName")
	require.NotContains(t, data, "secret_key")
	require.NotContains(t, data, "roleId")
	w = fireKeyHTTP(s, "DELETE", "/api/v1/auth/api-keys/42", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, uint32(42), probe.deleted)
	require.JSONEq(t, `{"status":"revoked"}`, w.Body.String())
}
func TestAccessKeyHTTPFieldMaskCodec(t *testing.T) {
	probe := &keyHTTPProbe{}
	s := http.NewServer()
	registerAccessKeyHTTP(s, probe)
	w := fireKeyHTTP(s, "PUT", "/api/v1/auth/api-keys/42", `{"data":{"role_id":12,"is_active":false,"expires_at":null},"update_mask":"roleId,isActive,expiresAt"}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, uint32(42), probe.updated.GetKeyId())
	require.Equal(t, []string{"role_id", "is_active", "expires_at"}, probe.updated.GetUpdateMask().GetPaths())
	require.NotNil(t, probe.updated.Data.IsActive)
	require.False(t, probe.updated.Data.GetIsActive())
	require.Nil(t, probe.updated.Data.ExpiresAt)
	// Underscores are not valid in Protobuf FieldMask's JSON string form.
	probe.updated = nil
	w = fireKeyHTTP(s, "PUT", "/api/v1/auth/api-keys/42", `{"data":{"is_active":false},"update_mask":"is_active"}`)
	require.Equal(t, 400, w.Code, w.Body.String())
	require.Nil(t, probe.updated)
}
func TestAccessKeyHTTPRejectsInvalidUpdateMasks(t *testing.T) {
	// The real service validates the request before touching its repository.
	s := http.NewServer()
	registerAccessKeyHTTP(s, service.NewAccessKeyService(nil, nil))
	for _, body := range []string{
		`{"data":{"is_active":false}}`,
		`{"data":{"is_active":false},"update_mask":""}`,
		`{"data":{},"update_mask":"roleId"}`,
		`{"data":{"name":"reader"},"update_mask":"accessKey"}`,
		`{"data":{"role_name":"reader"},"update_mask":"roleName"}`,
		`{"data":{"role_description":"x"},"update_mask":"roleDescription"}`,
		`{"update_mask":"name"}`,
	} {
		w := fireKeyHTTP(s, "PUT", "/api/v1/auth/api-keys/42", body)
		require.Equal(t, 400, w.Code, w.Body.String())
	}
}
