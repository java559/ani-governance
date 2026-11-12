package service

import (
	"context"
	"regexp"
	"strconv"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
)

type GpuUsageRefResolver interface {
	ResolveGpuUsageRef(context.Context, uint32, string, string) (*GpuUsageRef, error)
}
type AcceleratorQuotaAccounts interface {
	ListTenantAccounts(context.Context, uint32) ([]*quotapb.QuotaAccountView, error)
}

type AcceleratorService struct {
	admin.UnimplementedAcceleratorServiceServer
	admin.UnimplementedAcceleratorAdminServiceServer
	admin.UnimplementedAcceleratorCapacityAdminServiceServer
	admin.UnimplementedQuotaSelfServiceServer
	client           *data.AcceleratorClient
	tenants          ResourceTenantResolver
	refs             GpuUsageRefResolver
	accounts         AcceleratorQuotaAccounts
	actionAuthorizer AcceleratorActionAuthorizer
}

func NewAcceleratorService(client *data.AcceleratorClient, tenants ResourceTenantResolver, refs GpuUsageRefResolver, accounts AcceleratorQuotaAccounts, actionAuthorizer AcceleratorActionAuthorizer) *AcceleratorService {
	return &AcceleratorService{client: client, tenants: tenants, refs: refs, accounts: accounts, actionAuthorizer: actionAuthorizer}
}

func acceleratorPrincipal(ctx context.Context, platform bool) (*auth.Principal, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
	}
	if p.Type != auth.SubjectUser {
		return nil, errors.Forbidden("JWT_REQUIRED", "user JWT required")
	}
	if platform && p.TenantID != 0 {
		return nil, errors.Forbidden("PLATFORM_REQUIRED", "platform permission required")
	}
	if !platform && p.TenantID == 0 {
		return nil, errors.Forbidden("TENANT_REQUIRED", "tenant identity required")
	}
	return p, nil
}
func (s *AcceleratorService) adminContext(ctx context.Context) (*acc.AdminRead, error) {
	p, err := acceleratorPrincipal(ctx, true)
	if err != nil {
		return nil, err
	}
	if s.client == nil {
		return nil, acceleratorUnavailable()
	}
	return &acc.AdminRead{RequestId: uuid.NewString(), Actor: &acc.Actor{Type: string(p.Type), Id: strconv.FormatUint(uint64(p.ID), 10)}}, nil
}
func (s *AcceleratorService) tenantContext(ctx context.Context) (*acc.TenantContext, *auth.Principal, error) {
	p, err := acceleratorPrincipal(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	if s.client == nil || s.tenants == nil {
		return nil, nil, acceleratorUnavailable()
	}
	tenant, err := s.tenants.ResourceTenantID(ctx, p.TenantID)
	if err != nil {
		return nil, nil, err
	}
	id, err := uuid.Parse(tenant)
	if err != nil || id == uuid.Nil || id.String() != tenant {
		return nil, nil, errors.ServiceUnavailable("TENANT_MAPPING_INVALID", "tenant mapping unavailable")
	}
	return &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant, Actor: &acc.Actor{Type: string(p.Type), Id: strconv.FormatUint(uint64(p.ID), 10)}}, p, nil
}
func acceleratorUnavailable() error {
	return errors.ServiceUnavailable("ACCELERATOR_UNAVAILABLE", "accelerator access unavailable")
}
func acceleratorInvalid() error {
	return errors.BadRequest("INVALID_ACCELERATOR_REQUEST", "invalid accelerator request")
}
func acceleratorInvalidResponse() error {
	return errors.ServiceUnavailable("ACCELERATOR_INVALID_RESPONSE", "invalid accelerator response")
}
func acceleratorPage(size uint32, token string) (*acc.Page, error) {
	if size > 200 || len(token) > 8192 {
		return nil, acceleratorInvalid()
	}
	return &acc.Page{Size: size, Token: token}, nil
}
func acceleratorMutation(c *acc.AdminRead, key string) (*acc.AdminMutation, error) {
	id, e := uuid.Parse(key)
	if e != nil || id == uuid.Nil || id.String() != key {
		return nil, acceleratorInvalid()
	}
	return &acc.AdminMutation{Context: c, IdempotencyKey: key}, nil
}

var acceleratorReason = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,95}$`)

func mapAcceleratorError(err error) error {
	if err == nil {
		return nil
	}
	code := status.Code(err)
	reason := "ACCELERATOR_ERROR"
	if st, ok := status.FromError(err); ok {
		for _, d := range st.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok && acceleratorReason.MatchString(info.Reason) {
				reason = info.Reason
				break
			}
		}
	}
	httpCode := 503
	switch code {
	case codes.InvalidArgument:
		httpCode = 400
	case codes.Unauthenticated, codes.PermissionDenied:
		if code == codes.PermissionDenied && reason == "CLUSTER_REGISTRATION_NOT_AUTHORIZED" {
			return errors.Forbidden(reason, "cluster registration permission required")
		}
		// User permissions were already checked by Governance. A rejected
		// trusted service call is a dependency failure, not a user grant.
		httpCode = 503
		reason = "ACCELERATOR_SERVICE_ACCESS_DENIED"
	case codes.NotFound:
		httpCode = 404
	case codes.AlreadyExists, codes.Aborted:
		httpCode = 409
	case codes.FailedPrecondition, httpPreconditionCode:
		httpCode = 412
	case codes.DeadlineExceeded:
		httpCode = 504
	}
	return errors.New(httpCode, reason, "accelerator request failed")
}

// OutOfRange is a known request precondition (e.g. provider integer bounds).
const httpPreconditionCode = codes.OutOfRange

func (s *AcceleratorService) ListClusters(ctx context.Context, r *view.AcceleratorPageRequest) (*view.AcceleratorClusters, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	p, e := acceleratorPage(r.GetPageSize(), r.GetPageToken())
	if e != nil {
		return nil, e
	}
	v, e := s.client.Admin.ListClusters(ctx, &acc.ListClustersRequest{Context: c, Page: p})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	out := &view.AcceleratorClusters{NextPageToken: v.NextToken}
	for _, x := range v.Items {
		if x == nil {
			return nil, acceleratorInvalidResponse()
		}
		out.Items = append(out.Items, wireAcceleratorCluster(x))
	}
	return out, nil
}
func (s *AcceleratorService) RegisterCluster(ctx context.Context, r *view.RegisterAcceleratorClusterRequest) (*view.AcceleratorCluster, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	d := r.GetData()
	if d == nil {
		return nil, acceleratorInvalid()
	}
	m, e := acceleratorMutation(c, d.IdempotencyKey)
	if e != nil {
		return nil, e
	}
	v, e := s.client.Admin.RegisterCluster(ctx, &acc.RegisterClusterRequest{Mutation: m, DisplayName: d.DisplayName, ConnectionRef: d.ConnectionRef})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	return wireAcceleratorCluster(v), nil
}
func (s *AcceleratorService) ListPools(ctx context.Context, r *view.AcceleratorPageRequest) (*view.AcceleratorPools, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	p, e := acceleratorPage(r.GetPageSize(), r.GetPageToken())
	if e != nil {
		return nil, e
	}
	v, e := s.client.Admin.ListPools(ctx, &acc.ListPoolsRequest{Context: c, ClusterId: r.GetClusterId(), Page: p})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	out := &view.AcceleratorPools{NextPageToken: v.NextToken}
	for _, x := range v.Items {
		if x == nil {
			return nil, acceleratorInvalidResponse()
		}
		out.Items = append(out.Items, wireAcceleratorPool(x))
	}
	return out, nil
}
func (s *AcceleratorService) CreatePool(ctx context.Context, r *view.CreateAcceleratorPoolRequest) (*view.AcceleratorPool, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	d := r.GetData()
	if d == nil {
		return nil, acceleratorInvalid()
	}
	m, e := acceleratorMutation(c, d.IdempotencyKey)
	if e != nil {
		return nil, e
	}
	v, e := s.client.Admin.CreatePool(ctx, &acc.CreatePoolRequest{Mutation: m, DisplayName: d.DisplayName, ClusterId: d.ClusterId})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	return wireAcceleratorPool(v), nil
}
func (s *AcceleratorService) ListSupplyGroups(ctx context.Context, r *view.AcceleratorPageRequest) (*view.AcceleratorSupplies, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	p, e := acceleratorPage(r.GetPageSize(), r.GetPageToken())
	if e != nil {
		return nil, e
	}
	v, e := s.client.Admin.ListSupplyGroups(ctx, &acc.ListSupplyGroupsRequest{Context: c, PoolId: r.GetPoolId(), Page: p})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	out := &view.AcceleratorSupplies{NextPageToken: v.NextToken}
	for _, x := range v.Items {
		if x == nil {
			return nil, acceleratorInvalidResponse()
		}
		out.Items = append(out.Items, wireAcceleratorSupply(x))
	}
	return out, nil
}
func (s *AcceleratorService) AdoptSupplyGroup(ctx context.Context, r *view.AdoptAcceleratorSupplyRequest) (*view.AcceleratorSupply, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	d := r.GetData()
	if d == nil {
		return nil, acceleratorInvalid()
	}
	m, e := acceleratorMutation(c, d.IdempotencyKey)
	if e != nil {
		return nil, e
	}
	var baseline *acc.ProviderBaseline
	if d.Baseline != nil {
		baseline, e = acceleratorBaselineInput(d.Baseline)
		if e != nil {
			return nil, e
		}
	}
	policy, e := acceleratorGroupPolicyInput(d.Policy)
	if e != nil {
		return nil, e
	}
	if d.Baseline != nil && (d.Policy != nil || d.ProviderConfigRef != "" || d.ProviderConfigVersion != 0 || d.DiscoveryRevision != "") {
		return nil, acceleratorInvalid()
	}
	req := &acc.AdoptSupplyGroupRequest{Mutation: m, DisplayName: d.DisplayName, PoolId: d.PoolId, ModelKey: d.ModelKey, Baseline: baseline, QueueName: d.QueueName, ProviderConfigRef: d.ProviderConfigRef, ProviderConfigVersion: d.ProviderConfigVersion, DiscoveryRevision: d.DiscoveryRevision, Policy: policy}
	for _, n := range d.Nodes {
		if n == nil {
			return nil, acceleratorInvalid()
		}
		req.Nodes = append(req.Nodes, &acc.NodeRef{Uid: n.Uid, Name: n.Name})
	}
	v, e := s.client.Admin.AdoptSupplyGroup(ctx, req)
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	return wireAcceleratorSupply(v), nil
}
func (s *AcceleratorService) SetAdmission(ctx context.Context, r *view.SetAcceleratorAdmissionRequest) (*view.AcceleratorSupply, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	d := r.GetData()
	if d == nil {
		return nil, acceleratorInvalid()
	}
	m, e := acceleratorMutation(c, d.IdempotencyKey)
	if e != nil {
		return nil, e
	}
	desired, ok := acc.AdmissionState_value[d.Desired]
	if !ok || desired == 0 {
		return nil, acceleratorInvalid()
	}
	v, e := s.client.Admin.SetAdmission(ctx, &acc.SetAdmissionRequest{Mutation: m, GroupId: r.GetGroupId(), ExpectedVersion: d.ExpectedVersion, Desired: acc.AdmissionState(desired), Reason: d.Reason})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	return wireAcceleratorSupply(v), nil
}
func (s *AcceleratorService) CreateProfile(ctx context.Context, r *view.CreateAcceleratorProfileRequest) (*view.AcceleratorProfile, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	d := r.GetData()
	if d == nil || d.Spec == nil && d.GroupId == "" || d.Spec != nil && d.GroupId != "" {
		return nil, acceleratorInvalid()
	}
	m, e := acceleratorMutation(c, d.IdempotencyKey)
	if e != nil {
		return nil, e
	}
	var spec *acc.ProfileSpec
	if d.Spec != nil {
		spec, e = acceleratorSpecInput(d.Spec)
		if e != nil {
			return nil, e
		}
	}
	v, e := s.client.Admin.CreateProfile(ctx, &acc.CreateProfileRequest{Mutation: m, DisplayName: d.DisplayName, Spec: spec, ExpectedGroupVersion: d.ExpectedGroupVersion, GroupId: d.GroupId})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	return wireAcceleratorProfile(v), nil
}
func (s *AcceleratorService) AdminListProfiles(ctx context.Context, r *view.AcceleratorPageRequest) (*view.AcceleratorProfiles, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	p, e := acceleratorPage(r.GetPageSize(), r.GetPageToken())
	if e != nil {
		return nil, e
	}
	v, e := s.client.Admin.AdminListProfiles(ctx, &acc.AdminListProfilesRequest{Context: c, ClusterId: r.GetClusterId(), Page: p})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	return wireAcceleratorProfiles(v)
}
func (s *AcceleratorService) ListDevices(ctx context.Context, r *view.AcceleratorPageRequest) (*view.AcceleratorDevices, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	p, e := acceleratorPage(r.GetPageSize(), r.GetPageToken())
	if e != nil {
		return nil, e
	}
	v, e := s.client.Admin.ListDevices(ctx, &acc.ListDevicesRequest{Context: c, ClusterId: r.GetClusterId(), GroupId: r.GetGroupId(), Page: p})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	out := &view.AcceleratorDevices{NextPageToken: v.NextToken}
	for _, x := range v.Items {
		if x == nil {
			return nil, acceleratorInvalidResponse()
		}
		out.Items = append(out.Items, wireAcceleratorDevice(x))
	}
	return out, nil
}
func (s *AcceleratorService) ListProfiles(ctx context.Context, r *view.AcceleratorTenantPageRequest) (*view.AcceleratorProfiles, error) {
	c, _, e := s.tenantContext(ctx)
	if e != nil {
		return nil, e
	}
	p, e := acceleratorPage(r.GetPageSize(), r.GetPageToken())
	if e != nil {
		return nil, e
	}
	mode := int32(0)
	if r.GetMode() != "" {
		var ok bool
		mode, ok = acc.SupplyMode_value[r.GetMode()]
		if !ok {
			return nil, acceleratorInvalid()
		}
	}
	v, e := s.client.Catalog.ListProfiles(ctx, &acc.ListProfilesRequest{Context: c, ClusterId: r.GetClusterId(), Mode: acc.SupplyMode(mode), Page: p})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	return wireAcceleratorProfiles(v)
}
func (s *AcceleratorService) GetProfile(ctx context.Context, r *view.AcceleratorTenantVersionRequest) (*view.AcceleratorProfile, error) {
	c, _, e := s.tenantContext(ctx)
	if e != nil {
		return nil, e
	}
	v, e := s.client.Catalog.GetProfile(ctx, &acc.GetProfileRequest{Context: c, ProfileId: r.GetProfileId(), ProfileVersion: r.GetProfileVersion()})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	return wireAcceleratorProfile(v), nil
}
func (s *AcceleratorService) GetMyQuotaAccounts(ctx context.Context, _ *emptypb.Empty) (*admin.ListTenantQuotaAccountsResponse, error) {
	p, e := acceleratorPrincipal(ctx, false)
	if e != nil {
		return nil, e
	}
	if s.accounts == nil {
		return nil, errors.ServiceUnavailable("QUOTA_READ_UNAVAILABLE", "quota accounts unavailable")
	}
	items, err := s.accounts.ListTenantAccounts(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	return &admin.ListTenantQuotaAccountsResponse{TenantId: p.TenantID, Items: items}, nil
}

func (s *AcceleratorService) PreviewProfile(ctx context.Context, r *view.PreviewAcceleratorProfileRequest) (*view.AcceleratorProfilePreview, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, acceleratorInvalid()
	}
	v, e := s.client.Admin.PreviewProfile(ctx, &acc.PreviewProfileRequest{Context: c, GroupId: r.GroupId, Parts: r.Parts})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	return &view.AcceleratorProfilePreview{Spec: wireAcceleratorSpec(v.Spec), UnusedMemoryMib: v.UnusedMemoryMib, UnusedCoreUnits: v.UnusedCoreUnits}, nil
}
func (s *AcceleratorService) SetProfileState(ctx context.Context, r *view.SetAcceleratorProfileStateRequest) (*view.AcceleratorProfile, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	if r == nil || r.Data == nil {
		return nil, acceleratorInvalid()
	}
	m, e := acceleratorMutation(c, r.Data.IdempotencyKey)
	if e != nil {
		return nil, e
	}
	desired, ok := acc.ProfileState_value[r.Data.Desired]
	if !ok || (acc.ProfileState(desired) != acc.ProfileState_PROFILE_ACTIVE && acc.ProfileState(desired) != acc.ProfileState_PROFILE_DISABLED) {
		return nil, acceleratorInvalid()
	}
	v, e := s.client.Admin.SetProfileState(ctx, &acc.SetProfileStateRequest{Mutation: m, ProfileId: r.ProfileId, ExpectedRevision: r.Data.ExpectedRevision, Desired: acc.ProfileState(desired)})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	return wireAcceleratorProfile(v), nil
}
func (s *AcceleratorService) DeleteProfile(ctx context.Context, r *view.DeleteAcceleratorProfileRequest) (*view.AcceleratorProfile, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	if r == nil || r.Data == nil {
		return nil, acceleratorInvalid()
	}
	m, e := acceleratorMutation(c, r.Data.IdempotencyKey)
	if e != nil {
		return nil, e
	}
	v, e := s.client.Admin.DeleteProfile(ctx, &acc.DeleteProfileRequest{Mutation: m, ProfileId: r.ProfileId, ExpectedRevision: r.Data.ExpectedRevision})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	return wireAcceleratorProfile(v), nil
}

func validAcceleratorCapacityKinds(allocation acc.AllocationType, basis acc.CapacityBasis, granularity acc.CapacityGranularity) bool {
	return allocation >= acc.AllocationType_PHYSICAL && allocation <= acc.AllocationType_FIXED_PARTITION && (basis == acc.CapacityBasis_NATIVE_ALLOCATION || basis == acc.CapacityBasis_BOUND_POD_REQUEST) && (granularity == acc.CapacityGranularity_DEVICE || granularity == acc.CapacityGranularity_NODE_RESOURCE)
}
func (s *AcceleratorService) GetProfileCapacity(ctx context.Context, r *view.GetAcceleratorProfileCapacityRequest) (*view.AcceleratorProfileCapacity, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	if r == nil || !acceleratorInitializationID(r.ProfileId) || r.ProfileVersion == 0 {
		return nil, acceleratorInvalid()
	}
	v, e := s.client.Admin.GetProfileCapacity(ctx, &acc.GetProfileCapacityRequest{Context: c, ProfileId: r.ProfileId, ProfileVersion: r.ProfileVersion, IncludeDetails: r.IncludeDetails})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil || v.ProfileId != r.ProfileId || v.ProfileVersion != r.ProfileVersion || !validAcceleratorCapacityKinds(v.AllocationType, v.Basis, v.Granularity) {
		return nil, acceleratorInvalidResponse()
	}
	for _, row := range v.Details {
		if row == nil {
			return nil, acceleratorInvalidResponse()
		}
	}
	return wireAcceleratorProfileCapacity(v), nil
}
func (s *AcceleratorService) GetSupplyGroupCapacity(ctx context.Context, r *view.GetAcceleratorSupplyGroupCapacityRequest) (*view.AcceleratorSupplyGroupCapacity, error) {
	c, e := s.adminContext(ctx)
	if e != nil {
		return nil, e
	}
	if r == nil || !acceleratorInitializationID(r.GroupId) {
		return nil, acceleratorInvalid()
	}
	v, e := s.client.Admin.GetSupplyGroupCapacity(ctx, &acc.GetSupplyGroupCapacityRequest{Context: c, GroupId: r.GroupId, IncludeDetails: r.IncludeDetails})
	if e != nil {
		return nil, mapAcceleratorError(e)
	}
	if v == nil || v.GroupId != r.GroupId {
		return nil, acceleratorInvalidResponse()
	}
	for _, kind := range v.ResourceTypes {
		if kind == nil || !validAcceleratorCapacityKinds(kind.AllocationType, kind.Basis, kind.Granularity) {
			return nil, acceleratorInvalidResponse()
		}
		for _, row := range kind.Details {
			if row == nil {
				return nil, acceleratorInvalidResponse()
			}
		}
	}
	return wireAcceleratorSupplyGroupCapacity(v), nil
}
