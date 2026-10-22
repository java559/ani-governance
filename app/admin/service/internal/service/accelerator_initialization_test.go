package service

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"

	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/localdeps/kratos-authz/engine"
	"go-wind-admin/pkg/localdeps/kratos-authz/engine/casbin"
	"go-wind-admin/pkg/middleware/auth"
)

type initializationBoundaryClient struct {
	acc.AcceleratorAdminServiceClient
	registered bool
	operation  *acc.InitializationOperation
	created    *acc.CreateInitializationRequest
	retried    *acc.RetryInitializationOperationRequest
	listed     *acc.ListDiscoveredNodesRequest
	failure    error
}

func (client *initializationBoundaryClient) GetInitialization(context.Context, *acc.GetInitializationRequest, ...grpc.CallOption) (*acc.Initialization, error) {
	return &acc.Initialization{Registered: client.registered, ConfigurationState: "READY"}, client.failure
}

func (client *initializationBoundaryClient) ListDiscoveredNodes(_ context.Context, request *acc.ListDiscoveredNodesRequest, _ ...grpc.CallOption) (*acc.DiscoveredNodes, error) {
	client.listed = request
	return &acc.DiscoveredNodes{Total: 9007199254740993, DiscoveryRevision: "stable-structure"}, client.failure
}

func (client *initializationBoundaryClient) CreateInitialization(_ context.Context, request *acc.CreateInitializationRequest, _ ...grpc.CallOption) (*acc.InitializationOperation, error) {
	client.created = request
	return client.operation, client.failure
}

func (client *initializationBoundaryClient) GetInitializationOperation(context.Context, *acc.GetInitializationOperationRequest, ...grpc.CallOption) (*acc.InitializationOperation, error) {
	return client.operation, client.failure
}

func (client *initializationBoundaryClient) RetryInitializationOperation(_ context.Context, request *acc.RetryInitializationOperationRequest, _ ...grpc.CallOption) (*acc.InitializationOperation, error) {
	client.retried = request
	return client.operation, client.failure
}

func initializationServiceFixture(t *testing.T, actions ...string) (*AcceleratorService, *initializationBoundaryClient, context.Context) {
	t.Helper()
	policy, err := casbin.NewEngine(context.Background())
	require.NoError(t, err)
	rules := []casbin.PolicyRule{}
	for index, action := range actions {
		route := acceleratorInitializationActions[action]
		role := "reader"
		if index%2 == 1 {
			role = "writer"
		}
		rules = append(rules, casbin.PolicyRule{PType: "p", V0: role, V1: route.path, V2: route.method, V3: "0"})
	}
	require.NoError(t, policy.SetPolicies(context.Background(), engine.PolicyMap{"policies": rules, "projects": engine.MakeProjects("0")}, nil))
	client := &initializationBoundaryClient{registered: true, operation: &acc.InitializationOperation{OperationId: uuid.NewString(), Version: 1, State: "ACCEPTED", RequiredActions: []string{"accelerator:pool:create"}}}
	service := NewAcceleratorService(&data.AcceleratorClient{Admin: client}, nil, nil, nil, nil, NewQuotaAdapterRegistry(), policy)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 42})
	project := engine.Project("0")
	subjects := []string{"reader", "writer"}
	ctx = engine.ContextWithAuthClaims(ctx, &engine.AuthClaims{Subjects: &subjects, Project: &project})
	return service, client, ctx
}

func initializationPoolRequest() *view.CreateAcceleratorInitializationRequest {
	return &view.CreateAcceleratorInitializationRequest{Data: &view.CreateAcceleratorInitializationData{IdempotencyKey: uuid.NewString(), DiscoveryRevision: "stable-structure", Pools: []*view.AcceleratorInitializationPoolInput{{ClientKey: "pool-main", Pool: &view.AcceleratorInitializationPoolInput_NewPool{NewPool: &view.AcceleratorInitializationNewPool{DisplayName: "pool"}}}}}}
}

func TestAcceleratorInitializationComponentAuthorizationAndTrustedIdentity(t *testing.T) {
	service, client, ctx := initializationServiceFixture(t, "accelerator:initialization:create", "accelerator:pool:create")
	request := initializationPoolRequest()
	result, err := service.CreateInitialization(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ACCEPTED", result.State)
	require.Equal(t, client.operation.OperationId, result.OperationId)
	require.Equal(t, "42", client.created.Mutation.Context.Actor.Id)
	require.Equal(t, "user", client.created.Mutation.Context.Actor.Type)
	require.Equal(t, request.Data.IdempotencyKey, client.created.Mutation.IdempotencyKey)
	require.Equal(t, "pool", client.created.Pools[0].GetNewPool().DisplayName)
	client.created = nil
	client.registered = false
	_, err = service.CreateInitialization(ctx, request)
	require.EqualValues(t, 403, errors.FromError(err).Code)
	require.Nil(t, client.created)

	service, client, ctx = initializationServiceFixture(t, "accelerator:initialization:create")
	_, err = service.CreateInitialization(ctx, initializationPoolRequest())
	require.EqualValues(t, 403, errors.FromError(err).Code)
	require.Nil(t, client.created)
}

func TestAcceleratorInitializationNewProfileRequiresAutoOpenPermission(t *testing.T) {
	request := initializationPoolRequest()
	request.Data.Groups = []*view.AcceleratorInitializationGroupInput{{ClientKey: "group-main", Group: &view.AcceleratorInitializationGroupInput_NewGroup{NewGroup: &view.AcceleratorInitializationNewGroup{PoolKey: "pool-main", DisplayName: "new group", BaselineRef: "managed-baseline", QueueName: "gpu", Nodes: []*view.AcceleratorNode{{Uid: "node-uid", Name: "node"}}}}}}
	request.Data.Profiles = []*view.AcceleratorInitializationProfileInput{{ClientKey: "profile-main", GroupKey: "group-main", DisplayName: "8GiB", MemoryMibPerDevice: 8192, CoreLimitPercent: 25, MaxDevicesPerReplica: 1}}
	actions := []string{"accelerator:initialization:create", "accelerator:pool:create", "accelerator:supply-group:adopt", "accelerator:profile:publish"}
	service, client, ctx := initializationServiceFixture(t, actions...)
	_, err := service.CreateInitialization(ctx, request)
	require.EqualValues(t, 403, errors.FromError(err).Code)
	require.Nil(t, client.created)
	service, client, ctx = initializationServiceFixture(t, append(actions, "accelerator:supply-group:set-admission")...)
	_, err = service.CreateInitialization(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 8192, client.created.Profiles[0].MemoryMibPerDevice)
	request.Data.Profiles = nil
	service, client, ctx = initializationServiceFixture(t, actions...)
	_, err = service.CreateInitialization(ctx, request)
	require.NoError(t, err)
	require.Empty(t, client.created.Profiles)
}

func TestAcceleratorInitializationRetryRechecksFrozenActions(t *testing.T) {
	service, client, ctx := initializationServiceFixture(t, "accelerator:initialization:retry")
	request := &view.RetryAcceleratorInitializationOperationRequest{OperationId: client.operation.OperationId, Data: &view.RetryAcceleratorInitializationOperationData{IdempotencyKey: uuid.NewString(), ExpectedVersion: 1}}
	_, err := service.RetryInitializationOperation(ctx, request)
	require.EqualValues(t, 403, errors.FromError(err).Code)
	require.Nil(t, client.retried)
	service, client, ctx = initializationServiceFixture(t, "accelerator:initialization:retry", "accelerator:pool:create")
	request.OperationId = client.operation.OperationId
	_, err = service.RetryInitializationOperation(ctx, request)
	require.NoError(t, err)
	require.Equal(t, request.Data.IdempotencyKey, client.retried.Mutation.IdempotencyKey)
	require.Equal(t, request.Data.ExpectedVersion, client.retried.ExpectedVersion)
	client.retried = nil
	client.operation.RequiredActions = []string{"POST /arbitrary/route"}
	_, err = service.RetryInitializationOperation(ctx, request)
	require.EqualValues(t, 503, errors.FromError(err).Code)
	require.Equal(t, "ACCELERATOR_INVALID_RESPONSE", errors.FromError(err).Reason)
	require.Nil(t, client.retried)
	service, client, ctx = initializationServiceFixture(t, "accelerator:initialization:retry")
	client.operation.RequiredActions = nil
	request.OperationId = client.operation.OperationId
	_, err = service.RetryInitializationOperation(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, client.retried)
}

func TestAcceleratorInitializationDiscoveryPageAndErrorAttribution(t *testing.T) {
	service, client, ctx := initializationServiceFixture(t, "accelerator:discovery:list", "accelerator:initialization:read")
	result, err := service.ListDiscoveredNodes(ctx, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, client.listed.Page)
	require.EqualValues(t, 20, client.listed.PageSize)
	encoded, err := protojson.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"9007199254740993"`)
	zero, maximum := uint32(0), uint32(200)
	_, err = service.ListDiscoveredNodes(ctx, &view.ListAcceleratorDiscoveredNodesRequest{Page: &zero, PageSize: &maximum})
	require.EqualValues(t, 400, errors.FromError(err).Code)
	client.failure = status.Error(codes.PermissionDenied, "private-downstream-identity")
	_, err = service.GetInitialization(ctx, &emptypb.Empty{})
	require.EqualValues(t, 503, errors.FromError(err).Code)
	require.Equal(t, "ACCELERATOR_SERVICE_ACCESS_DENIED", errors.FromError(err).Reason)
	require.NotContains(t, err.Error(), "private-downstream")
}

func TestAcceleratorInitializationRejectsUntrustedPlatformClaims(t *testing.T) {
	service, _, ctx := initializationServiceFixture(t, "accelerator:initialization:read")
	for _, invalid := range []context.Context{
		context.Background(),
		auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 42}),
		auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectAPIKey, ID: 42}),
		auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 42, TenantID: 1}),
		engine.ContextWithAuthClaims(ctx, &engine.AuthClaims{}),
	} {
		_, err := service.GetInitialization(invalid, &emptypb.Empty{})
		require.Error(t, err)
	}
}
