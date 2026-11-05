package service

import (
	"context"
	"fmt"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/pkg/localdeps/go-utils/aggregator"
	"go-wind-admin/pkg/localdeps/go-utils/sliceutil"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	"google.golang.org/protobuf/types/known/emptypb"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"

	"go-wind-admin/pkg/constants"

	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/utils"
)

type UserService struct {
	adminV1.UserServiceHTTPServer

	log *bLogger.Helper

	userRepo           data.UserRepo
	userCredentialRepo *data.UserCredentialRepo

	roleRepo     *data.RoleRepo
	positionRepo *data.PositionRepo
	orgUnitRepo  *data.OrgUnitRepo
	tenantRepo   *data.TenantRepo

	membershipRepo *data.MembershipRepo

	authenticator        *data.Authenticator
	notificationChannels *data.NotificationChannelRepo
}

func NewUserService(
	ctx *bootstrap.Context,
	userRepo data.UserRepo,
	roleRepo *data.RoleRepo,
	userCredentialRepo *data.UserCredentialRepo,
	positionRepo *data.PositionRepo,
	orgUnitRepo *data.OrgUnitRepo,
	tenantRepo *data.TenantRepo,
	membershipRepo *data.MembershipRepo,
	authenticator *data.Authenticator,
	notificationChannels *data.NotificationChannelRepo,
) *UserService {
	svc := &UserService{
		log:                  ctx.NewLoggerHelper("user/service/admin-service"),
		userRepo:             userRepo,
		roleRepo:             roleRepo,
		userCredentialRepo:   userCredentialRepo,
		positionRepo:         positionRepo,
		orgUnitRepo:          orgUnitRepo,
		tenantRepo:           tenantRepo,
		membershipRepo:       membershipRepo,
		authenticator:        authenticator,
		notificationChannels: notificationChannels,
	}

	// Database initialization is an explicit deployment step; constructors never seed data.

	return svc
}

func (s *UserService) extractRelationIDs(
	users []*identityV1.User,
	roleSet aggregator.ResourceMap[uint32, *permissionV1.Role],
	tenantSet aggregator.ResourceMap[uint32, *identityV1.Tenant],
	orgUnitSet aggregator.ResourceMap[uint32, *identityV1.OrgUnit],
	posSet aggregator.ResourceMap[uint32, *identityV1.Position],
) {
	for _, v := range users {
		if v == nil {
			continue
		}

		if id := v.GetTenantId(); id > 0 {
			tenantSet[id] = nil
		}

		if id := v.GetRoleId(); id > 0 {
			roleSet[id] = nil
		}
		for _, roleId := range v.RoleIds {
			if roleId > 0 {
				roleSet[roleId] = nil
			}
		}

		if v.GetOrgUnitId() > 0 {
			orgUnitSet[v.GetOrgUnitId()] = nil
		}
		if len(v.OrgUnitIds) > 0 {
			for _, orgID := range v.OrgUnitIds {
				if orgID > 0 {
					orgUnitSet[orgID] = nil
				}
			}
		}

		if v.GetPositionId() > 0 {
			posSet[v.GetPositionId()] = nil
		}
		if len(v.PositionIds) > 0 {
			for _, posID := range v.PositionIds {
				if posID > 0 {
					posSet[posID] = nil
				}
			}
		}

	}
}

func (s *UserService) fetchRelationInfo(
	ctx context.Context,
	roleSet aggregator.ResourceMap[uint32, *permissionV1.Role],
	tenantSet aggregator.ResourceMap[uint32, *identityV1.Tenant],
	orgUnitSet aggregator.ResourceMap[uint32, *identityV1.OrgUnit],
	posSet aggregator.ResourceMap[uint32, *identityV1.Position],
) error {
	if len(roleSet) > 0 {
		roleIds := make([]uint32, 0, len(roleSet))
		for id := range roleSet {
			roleIds = append(roleIds, id)
		}

		roles, err := s.roleRepo.ListRolesByRoleIds(ctx, roleIds)
		if err != nil {
			s.log.Errorf(context.Background(), "query roles err: %v", err)
			return err
		}

		for _, role := range roles {
			roleSet[role.GetId()] = role
		}
	}

	if len(tenantSet) > 0 {
		tenantIds := make([]uint32, 0, len(tenantSet))
		for id := range tenantSet {
			tenantIds = append(tenantIds, id)
		}

		tenants, err := s.tenantRepo.ListTenantsByIds(ctx, tenantIds)
		if err != nil {
			s.log.Errorf(context.Background(), "query tenants err: %v", err)
			return err
		}

		for _, tenant := range tenants {
			tenantSet[tenant.GetId()] = tenant
		}
	}

	if len(orgUnitSet) > 0 {
		orgUnitIds := make([]uint32, 0, len(orgUnitSet))
		for id := range orgUnitSet {
			orgUnitIds = append(orgUnitIds, id)
		}

		orgUnits, err := s.orgUnitRepo.ListOrgUnitsByIds(ctx, orgUnitIds)
		if err != nil {
			s.log.Errorf(context.Background(), "query orgUnits err: %v", err)
			return err
		}

		for _, orgUnit := range orgUnits {
			orgUnitSet[orgUnit.GetId()] = orgUnit
		}
	}

	if len(posSet) > 0 {
		posIds := make([]uint32, 0, len(posSet))
		for id := range posSet {
			posIds = append(posIds, id)
		}

		positions, err := s.positionRepo.ListPositionByIds(ctx, posIds)
		if err != nil {
			s.log.Errorf(context.Background(), "query positions err: %v", err)
			return err
		}

		for _, position := range positions {
			posSet[position.GetId()] = position
		}
	}

	return nil
}

func (s *UserService) bindRelations(
	users []*identityV1.User,
	roleSet aggregator.ResourceMap[uint32, *permissionV1.Role],
	tenantSet aggregator.ResourceMap[uint32, *identityV1.Tenant],
	orgUnitSet aggregator.ResourceMap[uint32, *identityV1.OrgUnit],
	posSet aggregator.ResourceMap[uint32, *identityV1.Position],
) {
	aggregator.PopulateMulti(
		users,
		roleSet,
		func(ou *identityV1.User) []uint32 { return ou.GetRoleIds() },
		func(ou *identityV1.User, r []*permissionV1.Role) {
			for _, role := range r {
				ou.RoleNames = append(ou.RoleNames, role.GetName())
				ou.Roles = append(ou.Roles, role.GetCode())
			}
		},
	)
	aggregator.Populate(
		users,
		roleSet,
		func(ou *identityV1.User) uint32 { return ou.GetRoleId() },
		func(ou *identityV1.User, r *permissionV1.Role) {
			ou.RoleNames = append(ou.RoleNames, r.GetName())
			ou.Roles = append(ou.Roles, r.GetCode())
		},
	)

	aggregator.Populate(
		users,
		tenantSet,
		func(ou *identityV1.User) uint32 { return ou.GetTenantId() },
		func(ou *identityV1.User, r *identityV1.Tenant) {
			ou.TenantName = r.Name
		},
	)

	aggregator.PopulateMulti(
		users,
		posSet,
		func(ou *identityV1.User) []uint32 { return ou.GetPositionIds() },
		func(ou *identityV1.User, r []*identityV1.Position) {
			for _, pos := range r {
				ou.PositionNames = append(ou.PositionNames, pos.GetName())
			}
		},
	)
	aggregator.Populate(
		users,
		posSet,
		func(ou *identityV1.User) uint32 { return ou.GetPositionId() },
		func(ou *identityV1.User, r *identityV1.Position) {
			ou.PositionName = r.Name
		},
	)

	aggregator.PopulateMulti(
		users,
		orgUnitSet,
		func(ou *identityV1.User) []uint32 { return ou.GetOrgUnitIds() },
		func(ou *identityV1.User, orgs []*identityV1.OrgUnit) {
			for _, org := range orgs {
				ou.OrgUnitNames = append(ou.OrgUnitNames, org.GetName())
			}
		},
	)
	aggregator.Populate(
		users,
		orgUnitSet,
		func(ou *identityV1.User) uint32 { return ou.GetOrgUnitId() },
		func(ou *identityV1.User, org *identityV1.OrgUnit) {
			ou.OrgUnitName = org.Name
		},
	)
}

func (s *UserService) enrichRelations(ctx context.Context, users []*identityV1.User) error {
	var roleSet = make(aggregator.ResourceMap[uint32, *permissionV1.Role])
	var tenantSet = make(aggregator.ResourceMap[uint32, *identityV1.Tenant])
	var orgUnitSet = make(aggregator.ResourceMap[uint32, *identityV1.OrgUnit])
	var posSet = make(aggregator.ResourceMap[uint32, *identityV1.Position])

	s.extractRelationIDs(users, roleSet, tenantSet, orgUnitSet, posSet)
	if err := s.fetchRelationInfo(ctx, roleSet, tenantSet, orgUnitSet, posSet); err != nil {
		return err
	}
	s.bindRelations(users, roleSet, tenantSet, orgUnitSet, posSet)
	return nil
}

func (s *UserService) List(ctx context.Context, req *paginationV1.PagingRequest) (*identityV1.ListUserResponse, error) {
	resp, err := s.userRepo.List(ctx, req)
	if err != nil {
		return nil, err
	}

	_ = s.enrichRelations(ctx, resp.Items)

	return resp, nil
}

func (s *UserService) Count(ctx context.Context, req *paginationV1.PagingRequest) (*identityV1.CountUserResponse, error) {
	count, err := s.userRepo.Count(ctx, req)
	if err != nil {
		return nil, err
	}

	return &identityV1.CountUserResponse{
		Count: uint64(count),
	}, nil
}

func (s *UserService) Get(ctx context.Context, req *identityV1.GetUserRequest) (*identityV1.User, error) {
	resp, err := s.userRepo.Get(ctx, req)
	if err != nil {
		return nil, err
	}

	fakeItems := []*identityV1.User{resp}
	_ = s.enrichRelations(ctx, fakeItems)

	return resp, nil
}

func (s *UserService) Create(ctx context.Context, req *identityV1.CreateUserRequest) (*emptypb.Empty, error) {
	if req == nil || req.Data == nil {
		return nil, adminV1.ErrorBadRequest("invalid parameter")
	}

	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 获取操作者的用户信息
	_, err = s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: operator.UserId,
		},
	})
	if err != nil {
		return nil, err
	}

	req.Data.CreatedBy = trans.Ptr(operator.UserId)
	if operator.GetTenantId() > 0 {
		req.Data.TenantId = operator.TenantId
	}

	var roleIds []uint32
	if len(req.Data.GetRoleIds()) > 0 {
		roleIds = req.Data.GetRoleIds()
	}
	if req.Data.RoleId != nil && *req.Data.RoleId > 0 {
		roleIds = append(roleIds, *req.Data.RoleId)
	}
	roleIds = sliceutil.Unique(roleIds)
	if len(roleIds) == 0 {
		s.log.Errorf(ctx, "role_ids is required")
		return nil, adminV1.ErrorBadRequest("role_ids is required")
	}

	var queryString string
	if operator.GetTenantId() > 0 || req.Data.GetTenantId() > 0 {
		queryString = fmt.Sprintf(`{"id__in": "[%s]", "type": "TENANT", "tenant_id": %d}`,
			utils.NumberSliceToString(roleIds),
			req.Data.GetTenantId(),
		)
	} else {
		queryString = fmt.Sprintf(`{"id__in": "[%s]", "type": "SYSTEM"}`,
			utils.NumberSliceToString(roleIds),
		)
	}
	roles, err := s.roleRepo.List(ctx, &paginationV1.PagingRequest{
		NoPaging: trans.Ptr(true),
		FilteringType: &paginationV1.PagingRequest_Query{
			Query: queryString,
		},
	})
	if err != nil {
		s.log.Errorf(ctx, "query roles err: %v", err)
		return nil, err
	}

	if len(roles.Items) != len(roleIds) {
		s.log.Errorf(ctx, "some roles not found, requested role ids: %v", roleIds)
		return nil, adminV1.ErrorBadRequest("some roles not found")
	}
	if len(roles.Items) == 0 {
		s.log.Errorf(ctx, "at least one role is required")
		return nil, adminV1.ErrorBadRequest("at least one role is required")
	}

	req.Data.RoleId = nil
	req.Data.RoleIds = roleIds

	invitation, err := prepareAccountInvitation(ctx, s.notificationChannels, req.GetActivationMode(), req.Data, req.GetPassword())
	if err != nil {
		return nil, err
	}
	if invitation != nil {
		req.Data.TenantId = trans.Ptr(req.Data.GetTenantId())
		err = s.userCredentialRepo.InTransaction(ctx, func(tx *ent.Tx) error {
			invited, err := s.userRepo.CreateWithTx(ctx, tx, req.Data)
			if err != nil {
				return err
			}
			if err = s.userCredentialRepo.CreateInvitationWithTx(ctx, tx, invited.GetId(), invited.GetTenantId(), req.Data.GetUsername(), invitation.email, invitation.token, invitation.expires); err != nil {
				return err
			}
			return invitation.send(ctx, req.Data.GetUsername())
		})
		if err != nil {
			return nil, err
		}
		return &emptypb.Empty{}, nil
	}

	// 创建用户
	var user *identityV1.User
	if user, err = s.userRepo.Create(ctx, req); err != nil {
		return nil, err
	}

	if len(req.GetPassword()) == 0 {
		// 如果没有设置密码，则设置为默认密码。
		req.Password = trans.Ptr(constants.DefaultUserPassword)
	}

	if len(req.GetPassword()) > 0 {
		if err = s.userCredentialRepo.Create(ctx, &authenticationV1.CreateUserCredentialRequest{
			Data: &authenticationV1.UserCredential{
				UserId:   user.Id,
				TenantId: user.TenantId,

				IdentityType: authenticationV1.UserCredential_USERNAME.Enum(),
				Identifier:   req.Data.Username,

				CredentialType: authenticationV1.UserCredential_PASSWORD_HASH.Enum(),
				Credential:     req.Password,

				IsPrimary: trans.Ptr(true),
				Status:    authenticationV1.UserCredential_ENABLED.Enum(),
			},
		}); err != nil {
			return nil, err
		}
	}

	return &emptypb.Empty{}, nil
}

func (s *UserService) Update(ctx context.Context, req *identityV1.UpdateUserRequest) (*emptypb.Empty, error) {
	if req == nil || req.Data == nil {
		return nil, adminV1.ErrorBadRequest("invalid parameter")
	}

	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 获取操作者的用户信息
	_, err = s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: operator.UserId,
		},
	})
	if err != nil {
		return nil, err
	}

	req.Data.UpdatedBy = trans.Ptr(operator.UserId)
	if req.UpdateMask != nil {
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "updated_by")
	}

	req.Data.Id = trans.Ptr(req.GetId())

	if operator.GetTenantId() > 0 {
		req.Data.TenantId = operator.TenantId
	}

	var roleIds []uint32
	if len(req.Data.GetRoleIds()) > 0 {
		roleIds = req.Data.GetRoleIds()
	}
	if req.Data.RoleId != nil && *req.Data.RoleId > 0 {
		roleIds = append(roleIds, *req.Data.RoleId)
	}
	roleIds = sliceutil.Unique(roleIds)
	if len(roleIds) == 0 {
		s.log.Errorf(ctx, "role_ids is required")
		return nil, adminV1.ErrorBadRequest("role_ids is required")
	}

	var queryString string
	if operator.GetTenantId() > 0 || req.Data.GetTenantId() > 0 {
		queryString = fmt.Sprintf(`{"id__in": "[%s]", "type": "TENANT", "tenant_id": %d}`,
			utils.NumberSliceToString(roleIds),
			req.Data.GetTenantId(),
		)
	} else {
		queryString = fmt.Sprintf(`{"id__in": "[%s]", "type": "SYSTEM"}`,
			utils.NumberSliceToString(roleIds),
		)
	}
	roles, err := s.roleRepo.List(ctx, &paginationV1.PagingRequest{
		NoPaging: trans.Ptr(true),
		FilteringType: &paginationV1.PagingRequest_Query{
			Query: queryString,
		},
	})
	if err != nil {
		s.log.Errorf(ctx, "query roles err: %v", err)
		return nil, err
	}

	if len(roles.Items) != len(roleIds) {
		s.log.Errorf(ctx, "some roles not found, requested role ids: %v", roleIds)
		return nil, adminV1.ErrorBadRequest("some roles not found")
	}
	if len(roles.Items) == 0 {
		s.log.Errorf(ctx, "at least one role is required")
		return nil, adminV1.ErrorBadRequest("at least one role is required")
	}

	req.Data.RoleId = nil
	req.Data.RoleIds = roleIds

	// 更新用户
	if err = s.userRepo.Update(ctx, req); err != nil {
		s.log.Error(ctx, err.Error())
		return nil, err
	}

	if req.Data.Status != nil && req.Data.GetStatus() != identityV1.User_NORMAL {
		// Read the persisted status: update_mask may exclude the supplied status.
		updated, getErr := s.userRepo.Get(ctx, &identityV1.GetUserRequest{
			QueryBy: &identityV1.GetUserRequest_Id{Id: req.GetId()},
		})
		if getErr != nil {
			return nil, getErr
		}
		if updated.GetStatus() != identityV1.User_NORMAL {
			if err = s.authenticator.RevokeUserTokenAllClientTypes(ctx, req.GetId()); err != nil {
				s.log.Errorf(ctx, "revoke sessions after disabling user [%d] failed: %v", req.GetId(), err)
				return nil, adminV1.ErrorInternalServerError("user updated, but session revocation failed; retry the status update")
			}
		}
	}

	if len(req.GetPassword()) > 0 {
		// 目标用户一律按 id 从库中取：既校验存在，也保证重置目标与请求 id 严格一致
		// （避免 id=甲 + username=乙 的参数错位实际重置了乙），并以其真实租户限定凭证定位。
		targetUser, gerr := s.userRepo.Get(ctx, &identityV1.GetUserRequest{
			QueryBy: &identityV1.GetUserRequest_Id{Id: req.GetId()},
		})
		if gerr != nil {
			return nil, gerr
		}

		// 跨租户防护：不具备"重置他人密码"能力权限的操作者，重置前必须校验目标用户真实租户。
		// 判据由 IsPlatformAdmin 布尔改为能力权限：多平台角色下布尔无法表达能力差异，
		// 例如"平台只读"不应能重置他人密码，而"平台运维"可以。
		// 注意：此处不能用 req.Data.GetTenantId()，因为上方已将其覆盖为操作者租户
		if !hasPermission(operator, constants.SystemResetOthersCredentialPermissionCode) &&
			targetUser.GetTenantId() != operator.GetTenantId() {
			s.log.Errorf(ctx, "operator [%d] (tenant %d) has no permission to reset password of cross-tenant user [%d] (tenant %d)",
				operator.GetUserId(), operator.GetTenantId(), targetUser.GetId(), targetUser.GetTenantId())
			return nil, adminV1.ErrorForbidden("no permission to reset password of user in other tenant")
		}

		// 防 IDOR：必须用 DB 中已校验的目标用户名，而非请求体里可伪造的 username。
		// 否则攻击者可用 id=本租户用户 + username=他租户用户 绕过上面的租户校验。
		// 凭证再按目标租户限定：平台身份（viewer 租户 0）不限定租户时，
		// 与他租户同名的账号会让凭证定位命中多行。
		targetTenantID := targetUser.GetTenantId()
		if err = s.userCredentialRepo.ResetCredentialForTarget(ctx, &authenticationV1.ResetCredentialRequest{
			IdentityType:  authenticationV1.UserCredential_USERNAME,
			Identifier:    targetUser.GetUsername(),
			NewCredential: req.GetPassword(),
			NeedDecrypt:   true, // 前端 AES 密文传输，解密后才走复杂度/哈希
		}, &targetTenantID); err != nil {
			return nil, err
		}

		// 重置密码成功后吊销目标用户全部令牌，强制其重新登录，
		// 防止凭据已泄露的场景下旧令牌继续可用。
		if err = s.authenticator.RevokeUserTokenAllClientTypes(ctx, req.GetId()); err != nil {
			s.log.Errorf(ctx, "revoke tokens after password reset failed for user [%d]: %v", req.GetId(), err)
		}
	}

	return &emptypb.Empty{}, nil
}

func (s *UserService) Delete(ctx context.Context, req *identityV1.DeleteUserRequest) (*emptypb.Empty, error) {
	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 获取操作者的用户信息
	_, err = s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: operator.UserId,
		},
	})
	if err != nil {
		return nil, err
	}

	// 获取将被删除的用户信息
	target, err := s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: req.GetId(),
		},
	})
	if err != nil {
		return nil, err
	}

	// 禁止删除默认超级管理员：初始化时创建的平台级 admin（恒为 id=1，
	// 即便后续改名也由 id 兜底保护）。误删会导致系统失去超级管理员且无法自动重建。
	if target.GetId() == 1 ||
		(target.GetUsername() == constants.DefaultAdminUserName && target.GetTenantId() == 0) {
		s.log.Errorf(ctx, "operator [%d] attempted to delete default admin user [%d]",
			operator.GetUserId(), target.GetId())
		return nil, adminV1.ErrorBadRequest("default admin cannot be deleted")
	}

	// 禁止删除自己：误删自身账号将导致当前会话立即失去管理能力。
	if target.GetId() == operator.GetUserId() {
		s.log.Errorf(ctx, "operator [%d] attempted to delete self", operator.GetUserId())
		return nil, adminV1.ErrorBadRequest("cannot delete yourself")
	}

	// 删除用户
	if err = s.userRepo.Delete(ctx, req); err != nil {
		return nil, err
	}
	if err = s.authenticator.RevokeUserTokenAllClientTypes(ctx, target.GetId()); err != nil {
		s.log.Errorf(ctx, "revoke sessions after deleting user [%d] failed: %v", target.GetId(), err)
		return nil, adminV1.ErrorInternalServerError("user deleted, but session revocation failed; revoke remaining sessions")
	}

	return &emptypb.Empty{}, nil
}

func (s *UserService) UserExists(ctx context.Context, req *identityV1.UserExistsRequest) (*identityV1.UserExistsResponse, error) {
	return s.userRepo.UserExists(ctx, req)
}

// EditUserPassword 修改用户密码
func (s *UserService) EditUserPassword(ctx context.Context, req *identityV1.EditUserPasswordRequest) (*emptypb.Empty, error) {
	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 获取目标用户信息
	u, err := s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: req.GetUserId(),
		},
	})
	if err != nil {
		return nil, err
	}

	// 跨租户防护：不具备"重置他人密码"能力权限的操作者只能重置本租户用户的密码
	if !hasPermission(operator, constants.SystemResetOthersCredentialPermissionCode) && u.GetTenantId() != operator.GetTenantId() {
		s.log.Errorf(ctx, "operator [%d] (tenant %d) has no permission to reset password of cross-tenant user [%d] (tenant %d)",
			operator.GetUserId(), operator.GetTenantId(), u.GetId(), u.GetTenantId())
		return nil, adminV1.ErrorForbidden("no permission to reset password of user in other tenant")
	}

	// 凭证按目标用户所在租户定位：平台身份（viewer 租户 0）不限定租户时，
	// 与他租户同名的账号（如 POC 里平台 admin 与租户 admin 同名）会让定位命中多行。
	// 租户取自库中读出的目标用户，不信请求体。
	targetTenantID := u.GetTenantId()
	if err = s.userCredentialRepo.ResetCredentialForTarget(ctx, &authenticationV1.ResetCredentialRequest{
		IdentityType:  authenticationV1.UserCredential_USERNAME,
		Identifier:    u.GetUsername(),
		NewCredential: req.GetNewPassword(),
		NeedDecrypt:   true, // 前端 AES 密文传输，解密后才走复杂度/哈希
	}, &targetTenantID); err != nil {
		s.log.Errorf(ctx, "reset user password err: %v", err)
		return nil, err
	}

	// 重置密码成功后吊销目标用户全部令牌，强制其重新登录。
	if err = s.authenticator.RevokeUserTokenAllClientTypes(ctx, u.GetId()); err != nil {
		s.log.Errorf(ctx, "revoke tokens after password reset failed for user [%d]: %v", u.GetId(), err)
	}

	return &emptypb.Empty{}, nil
}
