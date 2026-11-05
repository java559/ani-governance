package service

import (
	"context"

	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"google.golang.org/protobuf/types/known/emptypb"

	"go-wind-admin/app/admin/service/internal/data"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	"go-wind-admin/pkg/middleware/auth"
)

type UserProfileService struct {
	adminV1.UserProfileServiceHTTPServer

	userRepo           data.UserRepo
	roleRepo           *data.RoleRepo
	userCredentialRepo *data.UserCredentialRepo
	authenticator      *data.Authenticator
	notificationRepo   *data.NotificationChannelRepo
	vcodeCache         *data.VCodeCache

	log *bLogger.Helper
}

func NewUserProfileService(
	ctx *bootstrap.Context,
	userRepo data.UserRepo,
	roleRepo *data.RoleRepo,
	userCredentialRepo *data.UserCredentialRepo,
	authenticator *data.Authenticator,
	notificationRepo *data.NotificationChannelRepo,
	vcodeCache *data.VCodeCache,
) *UserProfileService {
	return &UserProfileService{
		log:                ctx.NewLoggerHelper("user-profile/service/admin-service"),
		userRepo:           userRepo,
		roleRepo:           roleRepo,
		userCredentialRepo: userCredentialRepo,
		authenticator:      authenticator,
		notificationRepo:   notificationRepo,
		vcodeCache:         vcodeCache,
	}
}

func (s *UserProfileService) GetUser(ctx context.Context, _ *emptypb.Empty) (*identityV1.User, error) {
	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: operator.UserId,
		},
	})
	if err != nil {
		s.log.Errorf(ctx, "查询用户失败[%s]", err.Error())
		return nil, authenticationV1.ErrorNotFound("user not found")
	}

	roleCodes, err := s.roleRepo.ListRoleCodesByRoleIds(ctx, user.GetRoleIds())
	if err != nil {
		s.log.Errorf(ctx, "get user role codes failed [%s]", err.Error())
	}
	if roleCodes != nil {
		user.Roles = roleCodes
	}

	return user, err
}

func (s *UserProfileService) UpdateUser(ctx context.Context, req *identityV1.UpdateUserRequest) (*emptypb.Empty, error) {
	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	req.Data.Id = trans.Ptr(operator.UserId)
	req.Id = operator.UserId

	if err = s.userRepo.Update(ctx, req); err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (s *UserProfileService) ChangePassword(ctx context.Context, req *identityV1.ChangePasswordRequest) (*emptypb.Empty, error) {
	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 前端与登录/注册一致走 AES 密文传输，必须解密后再校验/哈希——
	// 此前漏设 NeedDecrypt，密文串被当明文哈希入库（改完即锁号）且绕过复杂度校验。
	// 自助改密按本人租户定位凭证：平台身份（tenant 0）不限定租户时，
	// 与他租户同名的账号会让凭证定位命中多行。
	operatorTenantID := operator.GetTenantId()
	err = s.userCredentialRepo.ChangeCredentialForTarget(ctx, &authenticationV1.ChangeCredentialRequest{
		IdentityType:  authenticationV1.UserCredential_USERNAME,
		Identifier:    operator.GetUsername(),
		OldCredential: req.GetOldPassword(),
		NewCredential: req.GetNewPassword(),
		NeedDecrypt:   true,
	}, &operatorTenantID)
	if err != nil {
		return nil, err
	}

	// 改密成功后吊销本人全部客户端类型的令牌（含当前会话），
	// 防止凭据泄露后旧令牌继续可用；前端在成功回调中引导重新登录。
	if err = s.authenticator.RevokeUserTokenAllClientTypes(ctx, operator.GetUserId()); err != nil {
		s.log.Errorf(ctx, "revoke tokens after password change failed for user [%d]: %v", operator.GetUserId(), err)
	}

	return &emptypb.Empty{}, nil
}
