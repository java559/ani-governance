package auth

import (
	"context"
	"reflect"
	"strconv"

	"github.com/go-kratos/kratos/v2/transport"
	"github.com/go-kratos/kratos/v2/transport/http"

	"go-wind-admin/pkg/localdeps/go-utils/trans"
	authzEngine "go-wind-admin/pkg/localdeps/kratos-authz/engine"
	authz "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	"go-wind-admin/pkg/permcode"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
)

func processAuthz(
	ctx context.Context,
	tr transport.Transporter,
	tokenPayload *authenticationV1.UserTokenPayload,
) (context.Context, error) {
	path := authzEngine.Resource(tr.Operation())
	action := defaultAction

	var htr *http.Transport
	var ok bool
	if htr, ok = tr.(*http.Transport); ok {
		// 路由模板来自 proto（蛇形参数名），策略路径来自 API 目录（OpenAPI 驼峰参数名）。
		// 两侧统一按参数段归一化后再比较，避免 {user_id} 与 {userId} 被判定为不同资源。
		path = authzEngine.Resource(permcode.NormalizePath(htr.PathTemplate()))
		action = authzEngine.Action(htr.Request().Method)
	}

	//log.Infof("Coming API Request: PATH[%s] ACTION[%s] USER ROLES[%v] USER ID[%d]",
	//	path, action, tokenPayload.GetRoles(), tokenPayload.UserId,
	//)

	authzClaims := authzEngine.AuthClaims{
		Subjects: trans.Ptr(tokenPayload.GetRoles()),
		Action:   trans.Ptr(action),
		Resource: trans.Ptr(path),
		// Casbin's domain must come from the verified token, never an HTTP header.
		Project: trans.Ptr(authzEngine.Project(strconv.FormatUint(uint64(tokenPayload.GetTenantId()), 10))),
	}

	ctx = authz.NewContext(ctx, &authzClaims)

	return ctx, nil
}

func setRequestOperationId(req interface{}, payload *authenticationV1.UserTokenPayload) error {
	if req == nil {
		return ErrInvalidRequest
	}

	v := reflect.ValueOf(req).Elem()
	field := v.FieldByName("OperatorId")
	if field.IsValid() && field.Kind() == reflect.Pointer {
		field.Set(reflect.ValueOf(&payload.UserId))
	}

	return nil
}

func setRequestTenantId(req interface{}, payload *authenticationV1.UserTokenPayload) error {
	if req == nil {
		return ErrInvalidRequest
	}

	v := reflect.ValueOf(req).Elem()
	// 此前误用小写 "tenantId"：proto 生成的导出字段为 "TenantId"，小写名匹配不到
	// 会导致 FieldByName 返回 invalid Value、注入被静默跳过。修正为正确的导出字段名。
	field := v.FieldByName("TenantId")
	if field.IsValid() && field.Kind() == reflect.Pointer && field.CanSet() {
		// payload.TenantId 本身就是 *uint32（proto optional 字段），
		// 再取地址会得到 **uint32，reflect.Set 直接 panic。
		field.Set(reflect.ValueOf(payload.TenantId))
	}

	return nil
}
