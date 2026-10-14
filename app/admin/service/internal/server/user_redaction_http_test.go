// T09 行为验收：protoc-gen-go-redact 接管后，真实 HTTP 链路上的用户接口仍然
// 同时具备「字段权限裁剪」与「静态脱敏」两层，且包装顺序与生产装配一致。
//
// 覆盖目标（对应 goals/T09.md 的 NewRestServer 条款）：
//   - 走 net/http 的完整请求-响应（httptest + Kratos HTTP server + protojson 编码），
//     不是直接调用装饰器。
//   - 注册表达式与 rest_server.go:223-226 逐字符一致：
//     userServiceServerAdapter 最内层、NewFieldPermissionUserServiceServer 中间、
//     RedactedUserServiceServer(..., nil) 最外层。
//   - 邮箱按 (redact.value).email{keep_local_first:2} 脱敏，
//     手机按 (redact.value).mask{keep_first:3 keep_last:4} 脱敏。
//   - 未赋值的可选字段不出现在响应里（protojson 默认不输出未填充字段）。
//   - 外层脱敏不能把内层已清除的字段「救回来」，证明 redact 在字段权限之外。
//   - bypass 为 nil 时 redact.Falsy 生效：任何令牌（含平台管理员）都不会跳过脱敏。
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	redactV1 "go-wind-admin/pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/redact/v1"
	"go-wind-admin/pkg/middleware/auth"
)

// t09UserStub 是最内层的业务实现替身：只提供 HTTP 路由会调用的方法，
// 返回固定的明文邮箱/手机号，并记录收到的请求。
type t09UserStub struct {
	adminV1.UserServiceHTTPServer

	gotListReq *paginationV1.PagingRequest
	gotGetReq  *identityV1.GetUserRequest
}

func (s *t09UserStub) List(_ context.Context, in *paginationV1.PagingRequest) (*identityV1.ListUserResponse, error) {
	s.gotListReq = in
	return &identityV1.ListUserResponse{
		Items: []*identityV1.User{t09PlainUser(7)},
		Total: 1,
	}, nil
}

func (s *t09UserStub) Get(_ context.Context, in *identityV1.GetUserRequest) (*identityV1.User, error) {
	s.gotGetReq = in
	return t09PlainUser(in.GetId()), nil
}

// t09PlainUser 构造一个只填充必要字段的 User：
// Email/Mobile 是脱敏对象，Nickname/Username 是「未受控必须保持」的对照，
// Telephone/Address/Remark/DeletedAt 刻意不赋值，用于验证 protojson 的缺省字段行为。
func t09PlainUser(id uint32) *identityV1.User {
	return &identityV1.User{
		Id:       trans.Ptr(id),
		Username: trans.Ptr("zhangsan"),
		Nickname: trans.Ptr("张三"),
		Email:    trans.Ptr("zhangsan@example.com"),
		Mobile:   trans.Ptr("13812345678"),
	}
}

// 明文与期望脱敏值：邮箱 keep_local_first=2 且 mask_domain=false，
// 手机号 keep_first=3 keep_last=4，掩码字符 '*'。
const (
	t09PlainEmail   = "zhangsan@example.com"
	t09PlainMobile  = "13812345678"
	t09RedactEmail  = "zh******@example.com"
	t09RedactMobile = "138****5678"
)

// newT09UserServer 用生产装配的同一表达式注册用户路由。
func newT09UserServer(t *testing.T, payload *authenticationV1.UserTokenPayload) (*khttp.Server, *t09UserStub) {
	t.Helper()

	stub := &t09UserStub{}
	srv := khttp.NewServer(
		khttp.Middleware(
			auth.CredentialHeaders(),
			auth.Server(
				auth.WithAccessTokenCheckerFromFuncs(
					func(_ context.Context, _ string, _ bool) (bool, *authenticationV1.UserTokenPayload) {
						return true, payload
					},
					nil,
				),
				// 脱敏与字段权限都发生在认证之后、业务返回之前；本用例不引入
				// casbin 策略库，因此只关闭授权步骤，认证与上下文注入保持。
				auth.WithEnableAuthority(false),
				auth.WithInjectMetadata(false),
			),
		),
	)

	adminV1.RegisterUserServiceHTTPServer(srv, adminV1.RedactedUserServiceServer(
		service.NewFieldPermissionUserServiceServer(&userServiceServerAdapter{UserServiceHTTPServer: stub}),
		nil))

	return srv, stub
}

// get 发一次真实 HTTP GET，返回 protojson 解码结果与原始报文体。
func get(t *testing.T, srv *khttp.Server, path string) (map[string]any, string) {
	t.Helper()
	host := httptest.NewServer(srv)
	t.Cleanup(host.Close)

	req, err := http.NewRequest(http.MethodGet, host.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer t09-opaque-token")
	resp, err := host.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "%s -> %d: %s", path, resp.StatusCode, body)

	var out map[string]any
	require.NoErrorf(t, json.Unmarshal(body, &out), "response is not JSON: %s", body)
	t.Logf("HTTP GET %s -> %d (%d bytes)", path, resp.StatusCode, len(body))
	return out, string(body)
}

// t09Token 构造带隐藏字段声明的令牌载荷。
func t09Token(hidden ...string) *authenticationV1.UserTokenPayload {
	return &authenticationV1.UserTokenPayload{UserId: 7, TenantId: trans.Ptr(uint32(1)), HiddenFields: hidden}
}

// TestT09UserHTTPListRedactsEmailAndMobile 列表路径：脱敏生效、明文不出现在报文任何位置、
// 未受控字段保持、未赋值字段缺席。
func TestT09UserHTTPListRedactsEmailAndMobile(t *testing.T) {
	srv, stub := newT09UserServer(t, t09Token())
	out, raw := get(t, srv, "/api/v1/admin/users")

	require.NotNil(t, stub.gotListReq, "内层业务应收到 List 请求")

	items, ok := out["items"].([]any)
	require.True(t, ok, "items 应为数组: %s", raw)
	require.Len(t, items, 1)
	item, ok := items[0].(map[string]any)
	require.True(t, ok)

	require.Equal(t, t09RedactEmail, item["email"], "邮箱应按 keep_local_first=2 脱敏")
	require.Equal(t, t09RedactMobile, item["mobile"], "手机号应按 keep_first=3 keep_last=4 脱敏")
	require.Equal(t, "张三", item["nickname"], "未受控字段必须原样保持")
	require.Equal(t, "zhangsan", item["username"], "未受控字段必须原样保持")

	require.NotContains(t, raw, t09PlainEmail, "明文邮箱不得出现在响应任何位置")
	require.NotContains(t, raw, t09PlainMobile, "明文手机号不得出现在响应任何位置")

	// protojson 默认不输出未填充字段：这些字段在桩里从未赋值。
	for _, absent := range []string{"telephone", "address", "remark", "deletedAt", "lockedUntil", "avatar"} {
		require.NotContains(t, item, absent, "%s 未赋值，不应出现在 JSON 中", absent)
	}
	require.Contains(t, item, "id")
	// protojson 把 64 位整数字符串化，这里同时锁住「total 未被两层包装器改动」。
	require.Equal(t, "1", out["total"])
}

// TestT09UserHTTPGetFieldPermissionClearsBeforeRedaction 单条路径：
// 隐藏 User.email 后该字段整体消失，而手机号仍被外层脱敏——
// 证明「字段权限在内、redact 在外」的包装顺序，且外层不会复活被清除的字段。
func TestT09UserHTTPGetFieldPermissionClearsBeforeRedaction(t *testing.T) {
	srv, stub := newT09UserServer(t, t09Token("User.email"))
	out, raw := get(t, srv, "/api/v1/admin/users/9")

	require.NotNil(t, stub.gotGetReq)
	require.EqualValues(t, 9, stub.gotGetReq.GetId(), "路径变量应绑定到 GetUserRequest.id")

	require.NotContains(t, out, "email", "命中隐藏字段的 email 应被清除（protojson 不输出未填充字段）")
	require.Equal(t, t09RedactMobile, out["mobile"], "未命中隐藏集的 mobile 仍应被外层脱敏")
	require.Equal(t, "张三", out["nickname"])
	require.NotContains(t, raw, t09PlainEmail)
	require.NotContains(t, raw, t09PlainMobile)
}

// TestT09UserHTTPFieldPermissionOnMobileKeepsRedactedEmail 反向组合：
// 隐藏 User.mobile 时该字段消失，email 仍走脱敏而不是消失，
// 排除「两层其实是同一层」的假阳性。
func TestT09UserHTTPFieldPermissionOnMobileKeepsRedactedEmail(t *testing.T) {
	srv, _ := newT09UserServer(t, t09Token("User.mobile"))
	out, raw := get(t, srv, "/api/v1/admin/users/9")

	require.NotContains(t, out, "mobile", "命中隐藏集的 mobile 应被清除")
	require.Equal(t, t09RedactEmail, out["email"], "未命中隐藏集的 email 仍应被外层脱敏")
	require.NotContains(t, raw, t09PlainEmail)
	require.NotContains(t, raw, t09PlainMobile)
}

// TestT09UserHTTPRedactionIsNotBypassable 生产装配传入 bypass=nil，
// 运行时回退为 redact.Falsy；即便平台管理员（tenantId=0）也不能跳过脱敏。
func TestT09UserHTTPRedactionIsNotBypassable(t *testing.T) {
	srv, _ := newT09UserServer(t, &authenticationV1.UserTokenPayload{
		UserId:   1,
		TenantId: trans.Ptr(uint32(0)),
		Roles:    []string{"platform-admin"},
	})
	out, raw := get(t, srv, "/api/v1/admin/users/1")

	require.Equal(t, t09RedactEmail, out["email"], "bypass=nil 时任何令牌都必须脱敏")
	require.Equal(t, t09RedactMobile, out["mobile"])
	require.NotContains(t, raw, t09PlainEmail)
	require.NotContains(t, raw, t09PlainMobile)
}

// TestT09RedactOptionsSurviveOnBusinessDescriptors 脱敏 option 必须仍然挂在业务
// 描述符上：接管改的是生成器与 Proto 来源，不是把 (redact.value) 注解抹掉。
// 同时证明全进程只注册了一份 redact/v1 描述符，且解析到 pkg/localdeps 的接管副本。
func TestT09RedactOptionsSurviveOnBusinessDescriptors(t *testing.T) {
	var u any = t09PlainUser(1)
	redactor, ok := u.(interface{ Redact() })
	require.True(t, ok, "生成的 *.pb.redact.go 必须继续实现 Redact()")

	redactor.Redact()
	user := u.(*identityV1.User)
	require.Equal(t, t09RedactEmail, user.GetEmail())
	require.Equal(t, t09RedactMobile, user.GetMobile())

	fd := identityV1.File_identity_service_v1_user_proto
	require.Equal(t, "identity/service/v1/user.proto", fd.Path())

	// 唯一来源：redact/v1 在全进程只能注册一份，且 Go 包是本仓接管后的路径。
	var redactFiles []protoreflect.FileDescriptor
	protoregistry.GlobalFiles.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		if f.Path() == "redact/v1/redact.proto" {
			redactFiles = append(redactFiles, f)
		}
		return true
	})
	require.Len(t, redactFiles, 1, "redact/v1 描述符必须只注册一次，实际 %d 份", len(redactFiles))
	require.Equal(t, "redact", string(redactFiles[0].Package()))
	opts, ok := redactFiles[0].Options().(*descriptorpb.FileOptions)
	require.True(t, ok)
	require.Equal(t,
		"go-wind-admin/pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/redact/v1;redact",
		opts.GetGoPackage(), "go_package 必须指向本仓接管副本")

	fields := fd.Messages().ByName("User").Fields()
	emailRule, ok := proto.GetExtension(fields.ByName("email").Options(), redactV1.E_Value).(*redactV1.FieldRules)
	require.True(t, ok, "User.email 的 (redact.value) option 必须保留")
	require.NotNil(t, emailRule.GetEmail(), "email 规则应为 EmailRules")
	require.EqualValues(t, 2, emailRule.GetEmail().GetKeepLocalFirst(), "keep_local_first 必须仍为 2")
	require.False(t, emailRule.GetEmail().GetMaskDomain())

	mobileRule, ok := proto.GetExtension(fields.ByName("mobile").Options(), redactV1.E_Value).(*redactV1.FieldRules)
	require.True(t, ok, "User.mobile 的 (redact.value) option 必须保留")
	require.NotNil(t, mobileRule.GetMask(), "mask 规则应为 MaskRules")
	require.EqualValues(t, 3, mobileRule.GetMask().GetKeepFirst())
	require.EqualValues(t, 4, mobileRule.GetMask().GetKeepLast())
	// mask_char 在 user.proto 中未显式书写，默认 '*' 由生成器写入生成的 _redactMask 调用。
	require.Empty(t, mobileRule.GetMask().GetMaskChar())

	// 校验/HTTP option 同样保留：HTTP 路由能命中已在上面证明，这里直接读描述符。
	userSvc := adminV1.File_admin_service_v1_i_user_proto.Services().ByName("UserService")
	require.True(t, proto.HasExtension(userSvc.Methods().ByName("List").Options(),
		annotations.E_Http), "UserService.List 的 google.api.http option 必须保留")
	var redactImport protoreflect.FileDescriptor
	for i := 0; i < adminV1.File_admin_service_v1_i_user_proto.Imports().Len(); i++ {
		if imp := adminV1.File_admin_service_v1_i_user_proto.Imports().Get(i); imp.Path() == "redact/v1/redact.proto" {
			redactImport = imp
		}
	}
	require.NotNil(t, redactImport, "admin BFF 必须仍然 import 唯一那份 redact/v1")
	require.Equal(t, "redact", string(redactImport.Package()))
}
