// T12 装配验收：证明换成本地 swagger UI 之后，NewRestServer 的 EnableSwagger 开关
// 仍然是唯一的注册条件，启用时 UI 与内存 OpenAPI 在真实路由上可取，禁用时既不注册
// 也不改变其余行为。
//
// 除 ApiService 外服务形参全部传 nil：这里不执行任何业务方法，只检查路由注册。若某个
// 注册路径需要真实服务，本用例会以明确的失败暴露它，而不是被跳过。
package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/app/admin/service/tests/testutil"
	bConf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

// newRestServerWithSwagger builds the real REST server for one EnableSwagger value.
func newRestServerWithSwagger(t *testing.T, enableSwagger bool) http.Handler {
	t.Helper()

	cfg := &bConf.Bootstrap{
		Server: &bConf.Server{
			Rest: &bConf.Server_REST{
				Network:       "tcp",
				Addr:          "127.0.0.1:0",
				EnableSwagger: enableSwagger,
			},
		},
	}
	ctx := bootstrap.NewContextWithParam(context.Background(), &bConf.AppInfo{}, cfg, bLogger.NopLogger())

	// ApiService is the one service NewRestServer touches while building (it stores the
	// route walker), so it gets a real instance over the isolated in-memory database.
	apiService := service.NewApiService(ctx, data.NewApiRepo(ctx, testutil.NewEntClientForTest(t)), nil)

	srv, err := NewRestServer(ctx, nil, nil,
		nil,                               // authenticationService
		nil,                               // mfaService
		nil,                               // loginPolicyService
		nil,                               // portalService
		nil,                               // taskService
		nil,                               // dictTypeService
		nil,                               // dictEntryService
		nil,                               // languageService
		nil,                               // tenantService
		nil,                               // planService
		nil,                               // planQuotaService
		nil,                               // quotaAdminService
		nil,                               // planModuleService
		nil,                               // userService
		nil,                               // userProfileService
		nil,                               // roleService
		nil,                               // positionService
		nil,                               // orgUnitService
		nil,                               // menuService
		apiService,                        // apiService
		nil,                               // permissionService
		nil,                               // permissionGroupService
		nil,                               // permissionAuditLogService
		nil,                               // policyEvaluationLogService
		nil,                               // loginAuditLogService
		nil,                               // apiAuditLogService
		nil,                               // operationAuditLogService
		nil,                               // dataAccessAuditLogService
		nil,                               // redisCacheMonitorService
		nil,                               // serverMonitorService
		nil,                               // notificationChannelService
		nil,                               // onlineSessionService
		nil,                               // dashboardService
		nil,                               // internalMessageService
		nil,                               // internalMessageCategoryService
		nil,                               // internalMessageRecipientService
		nil,                               // accessKeyService
		nil,                               // configService
		nil,                               // networkService
		nil,                               // platformNetworkService
		nil,                               // acceleratorService
		nil,                               // imageService
		nil,                               // storageService
		nil,                               // modeldevService
		service.NewInferenceService(nil),  // inferenceService
		service.NewModelService(nil, nil), // modelService: preserve route registration without a downstream client
		nil,                               // computePresetService
		nil,                               // overviewService
	)
	require.NoError(t, err)
	require.NotNil(t, srv, "NewRestServer must build a server for this configuration")
	return srv
}

func TestNewRestServerRegistersTheTakenOverSwaggerUi(t *testing.T) {
	ts := httptest.NewServer(newRestServerWithSwagger(t, true))
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/docs/") //nolint:gosec // loopback httptest server
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "EnableSwagger must expose the UI")
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")

	// the document route is the one WithMemoryData(assets.OpenApiData, "yaml") creates
	doc, err := http.Get(ts.URL + "/docs/openapi.yaml") //nolint:gosec // loopback httptest server
	require.NoError(t, err)
	defer doc.Body.Close()
	require.Equal(t, http.StatusOK, doc.StatusCode, "the embedded OpenAPI document must be served")
	body, err := io.ReadAll(doc.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "openapi:", "the served document must be the embedded OpenAPI content")
	assert.Greater(t, len(body), 100, "the embedded document is not a stub")
}

func TestNewRestServerKeepsTheUiDisabledWhenConfiguredOff(t *testing.T) {
	ts := httptest.NewServer(newRestServerWithSwagger(t, false))
	t.Cleanup(ts.Close)

	for _, path := range []string{"/docs/", "/docs/openapi.yaml"} {
		resp, err := http.Get(ts.URL + path) //nolint:gosec // loopback httptest server
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "%q must stay unregistered when the flag is off", path)
		assert.False(t, strings.Contains(resp.Header.Get("Content-Type"), "text/html"))
	}
}
