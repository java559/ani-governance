package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/localdeps/kratos-authz/engine"
	"go-wind-admin/pkg/middleware/auth"
)

type supplyBoundaryClient struct {
	acc.AcceleratorAdminServiceClient
	listed     *acc.ListSupplyCandidatesRequest
	registered *acc.RegisterSupplyConfigurationRequest
	fetched    *acc.GetSupplyRegistrationRequest
	candidates *acc.SupplyCandidates
	result     *acc.SupplyRegistrationResult
	failure    error
}

func (client *supplyBoundaryClient) ListSupplyCandidates(_ context.Context, request *acc.ListSupplyCandidatesRequest, _ ...grpc.CallOption) (*acc.SupplyCandidates, error) {
	client.listed = request
	return client.candidates, client.failure
}

func (client *supplyBoundaryClient) RegisterSupplyConfiguration(_ context.Context, request *acc.RegisterSupplyConfigurationRequest, _ ...grpc.CallOption) (*acc.SupplyRegistrationResult, error) {
	client.registered = request
	return client.result, client.failure
}
func (client *supplyBoundaryClient) GetSupplyRegistration(_ context.Context, request *acc.GetSupplyRegistrationRequest, _ ...grpc.CallOption) (*acc.SupplyRegistrationResult, error) {
	client.fetched = request
	return client.result, client.failure
}

func supplyServiceFixture(t *testing.T, actions ...string) (*AcceleratorService, *supplyBoundaryClient, context.Context) {
	t.Helper()
	service, _, ctx := initializationServiceFixture(t, actions...)
	maximum, zero, knownFalse, knownEmpty := ^uint64(0), uint32(0), false, ""
	candidate := &acc.SupplyCandidate{
		CandidateId: "observed-candidate", Node: &acc.NodeRef{Uid: "node-uid", Name: "node"},
		AdapterId: "hygon-hcu-v1", AllocationType: acc.AllocationType_FIXED_PARTITION,
		ResourceKey: "hygon.com/fixed", ModelKey: "approved-model", MemoryCapacityBytes: &maximum,
		MemoryBlockMib: &zero, CoreBudgetAdditive: &knownFalse, RuntimeClassName: &knownEmpty,
		MissingFields: []string{"compute_units_per_device"},
		Sources:       []*acc.SupplyFactSource{{Field: "memory_capacity_bytes", Source: "AUTOMATIC_OBSERVATION", Ref: "official-exporter"}},
	}
	registrationID := uuid.NewString()
	client := &supplyBoundaryClient{
		candidates: &acc.SupplyCandidates{ConnectionRef: "managed", Items: []*acc.SupplyCandidate{candidate}, Total: 1, DiscoveryRevision: "stable-structure", Checks: []*acc.InitializationCheck{{Key: "inventory", Source: "AUTOMATIC_OBSERVATION", State: "UNKNOWN", Reason: "INVENTORY_UNAVAILABLE"}}},
		result: &acc.SupplyRegistrationResult{RegistrationId: registrationID, ConnectionRef: "managed", CreatedAt: "2026-11-11T08:00:00Z", Configurations: []*acc.RegisteredSupplyConfiguration{{
			RegistrationId: registrationID, ClientKey: "fixed", ConnectionRef: "managed", ProviderConfigRef: "saved-config", Version: 1,
			Nodes: []*acc.NodeRef{{Uid: "node-uid", Name: "node"}}, ModelKey: "approved-model", Baseline: &acc.ProviderBaseline{BaselineId: uuid.NewString(), AdapterId: "hygon-hcu-v1"},
			Candidates: []*acc.SupplyCandidate{candidate}, Declarations: []*acc.SupplyDeclaration{{Field: "compute_units_per_device", Value: "30", Source: "approved actual type"}}, ResourceMappingSource: "reviewed plugin mapping",
		}}},
	}
	service.client.Admin = client
	return service, client, ctx
}

func supplyRegistrationRequest() *view.RegisterAcceleratorSupplyConfigurationRequest {
	return &view.RegisterAcceleratorSupplyConfigurationRequest{Data: &view.RegisterAcceleratorSupplyConfigurationData{
		IdempotencyKey: uuid.NewString(), ConnectionRef: "managed", DiscoveryRevision: "stable-structure",
		Registrations: []*view.AcceleratorSupplyRegistrationInput{{
			ClientKey: "fixed", CandidateIds: []string{"observed-candidate"}, DisplayName: "fixed", MaxDevicesPerReplica: 1,
			Declarations:             []*view.AcceleratorSupplyDeclaration{{Field: "compute_units_per_device", Value: "30", Source: "approved actual type"}},
			ResourceMappingConfirmed: true, ResourceMappingSource: "reviewed plugin mapping",
		}},
	}}
}

func TestAcceleratorSupplyCandidatesPreservePresenceAndTrustedIdentity(t *testing.T) {
	service, client, ctx := supplyServiceFixture(t, "accelerator:discovery:list")
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-actor-id", "forged", "x-ani-tenant-id", "forged", "authorization", "Bearer forged"))
	result, err := service.ListSupplyCandidates(ctx, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, client.listed.Page)
	require.EqualValues(t, 20, client.listed.PageSize)
	require.Equal(t, "42", client.listed.Context.Actor.Id)
	require.Equal(t, "user", client.listed.Context.Actor.Type)
	require.NotEmpty(t, client.listed.Context.RequestId)
	require.Nil(t, client.registered, "a candidate read must not invoke registration")
	item := result.Items[0]
	require.Equal(t, ^uint64(0), *item.MemoryCapacityBytes)
	require.NotNil(t, item.MemoryBlockMib)
	require.Zero(t, *item.MemoryBlockMib)
	require.NotNil(t, item.CoreBudgetAdditive)
	require.False(t, *item.CoreBudgetAdditive)
	require.NotNil(t, item.RuntimeClassName)
	require.Empty(t, *item.RuntimeClassName)
	require.Nil(t, item.ComputeUnitsPerDevice)
	require.Equal(t, []string{"compute_units_per_device"}, item.MissingFields)
	require.Equal(t, "UNKNOWN", result.Checks[0].State)
	encoded, err := protojson.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"memory_capacity_bytes":"18446744073709551615"`)
	page, size := uint32(3), uint32(200)
	_, err = service.ListSupplyCandidates(ctx, &view.ListAcceleratorSupplyCandidatesRequest{ConnectionRef: "managed", Page: &page, PageSize: &size})
	require.NoError(t, err)
	require.EqualValues(t, 3, client.listed.Page)
	require.EqualValues(t, 200, client.listed.PageSize)
	page = 0
	_, err = service.ListSupplyCandidates(ctx, &view.ListAcceleratorSupplyCandidatesRequest{Page: &page})
	require.EqualValues(t, 400, errors.FromError(err).Code)
}

func TestAcceleratorSupplyRegistrationForwardsDeclarationsAndStableIntent(t *testing.T) {
	service, client, ctx := supplyServiceFixture(t, "accelerator:supply:register")
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-actor-id", "forged", "x-ani-action", "other"))
	request := supplyRegistrationRequest()
	result, err := service.RegisterSupplyConfiguration(ctx, request)
	require.NoError(t, err)
	require.Nil(t, client.listed, "Gov must not reimplement ACC candidate revalidation")
	require.Equal(t, request.Data.IdempotencyKey, client.registered.Mutation.IdempotencyKey)
	require.Equal(t, "42", client.registered.Mutation.Context.Actor.Id)
	require.Equal(t, "stable-structure", client.registered.DiscoveryRevision)
	entry := client.registered.Registrations[0]
	require.Equal(t, []string{"observed-candidate"}, entry.CandidateIds)
	require.Equal(t, "compute_units_per_device", entry.Declarations[0].Field)
	require.Equal(t, "30", entry.Declarations[0].Value)
	require.Equal(t, "approved actual type", entry.Declarations[0].Source)
	require.True(t, entry.ResourceMappingConfirmed)
	require.Equal(t, "reviewed plugin mapping", entry.ResourceMappingSource)
	require.Equal(t, "saved-config", result.Configurations[0].ProviderConfigRef)
	require.Equal(t, "approved actual type", result.Configurations[0].Declarations[0].Source)
	encoded, err := protojson.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"actor"`)
	_, err = service.RegisterSupplyConfiguration(ctx, request)
	require.NoError(t, err)
	require.Equal(t, request.Data.IdempotencyKey, client.registered.Mutation.IdempotencyKey)
	request.Data.Registrations[0].Declarations = []*view.AcceleratorSupplyDeclaration{{Field: "memory_capacity_bytes", Value: "18446744073709551615", Source: "reviewed exact bytes"}}
	mapped, err := acceleratorSupplyRegistrationInput(request.Data)
	require.NoError(t, err)
	require.Equal(t, "18446744073709551615", mapped.Registrations[0].Declarations[0].Value)
}

func TestAcceleratorSupplyReadDoesNotGrantRegistration(t *testing.T) {
	service, client, ctx := supplyServiceFixture(t, "accelerator:discovery:list")
	_, err := service.ListSupplyCandidates(ctx, nil)
	require.NoError(t, err)
	_, err = service.RegisterSupplyConfiguration(ctx, supplyRegistrationRequest())
	require.EqualValues(t, 403, errors.FromError(err).Code)
	require.Nil(t, client.registered)
	service, client, ctx = supplyServiceFixture(t, "accelerator:supply:register")
	_, err = service.ListSupplyCandidates(ctx, nil)
	require.EqualValues(t, 403, errors.FromError(err).Code)
	require.Nil(t, client.listed)
	_, err = service.GetSupplyRegistration(ctx, &view.GetAcceleratorSupplyRegistrationRequest{RegistrationId: client.result.RegistrationId})
	require.EqualValues(t, 403, errors.FromError(err).Code)
	require.Nil(t, client.fetched)
}

func TestAcceleratorSupplyRegistrationReadUsesSavedResult(t *testing.T) {
	service, client, ctx := supplyServiceFixture(t, "accelerator:discovery:list")
	registrationID := client.result.RegistrationId
	client.candidates = nil
	result, err := service.GetSupplyRegistration(ctx, &view.GetAcceleratorSupplyRegistrationRequest{RegistrationId: registrationID})
	require.NoError(t, err)
	require.Equal(t, "saved-config", result.Configurations[0].ProviderConfigRef)
	require.Equal(t, registrationID, client.fetched.RegistrationId)
	require.Equal(t, "42", client.fetched.Context.Actor.Id)
	require.Nil(t, client.listed)
	require.Nil(t, client.registered)
	_, err = service.GetSupplyRegistration(ctx, nil)
	require.EqualValues(t, 400, errors.FromError(err).Code)
	client.failure = status.Error(codes.NotFound, "private lookup details")
	_, err = service.GetSupplyRegistration(ctx, &view.GetAcceleratorSupplyRegistrationRequest{RegistrationId: registrationID})
	require.EqualValues(t, 404, errors.FromError(err).Code)
	client.failure = nil
	_, err = service.GetSupplyRegistration(ctx, &view.GetAcceleratorSupplyRegistrationRequest{RegistrationId: uuid.NewString()})
	require.EqualValues(t, 503, errors.FromError(err).Code)
}

func TestAcceleratorSupplyRejectsUntrustedPlatformIdentity(t *testing.T) {
	service, client, ctx := supplyServiceFixture(t, "accelerator:discovery:list", "accelerator:supply:register")
	for _, invalid := range []context.Context{
		context.Background(),
		auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 42}),
		auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectAPIKey, ID: 42}),
		auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 42, TenantID: 1}),
		engine.ContextWithAuthClaims(ctx, &engine.AuthClaims{}),
	} {
		_, err := service.ListSupplyCandidates(invalid, nil)
		require.Error(t, err)
		_, err = service.RegisterSupplyConfiguration(invalid, supplyRegistrationRequest())
		require.Error(t, err)
		_, err = service.GetSupplyRegistration(invalid, &view.GetAcceleratorSupplyRegistrationRequest{RegistrationId: client.result.RegistrationId})
		require.Error(t, err)
	}
	require.Nil(t, client.listed)
	require.Nil(t, client.registered)
	require.Nil(t, client.fetched)
}

func TestAcceleratorSupplyInvalidInputAndDependencyErrors(t *testing.T) {
	service, client, ctx := supplyServiceFixture(t, "accelerator:discovery:list", "accelerator:supply:register")
	for _, request := range []*view.RegisterAcceleratorSupplyConfigurationRequest{nil, {}, {Data: &view.RegisterAcceleratorSupplyConfigurationData{}}} {
		_, err := service.RegisterSupplyConfiguration(ctx, request)
		require.EqualValues(t, 400, errors.FromError(err).Code)
	}
	require.Nil(t, client.registered)
	client.failure = status.Error(codes.PermissionDenied, "private downstream identity")
	_, err := service.ListSupplyCandidates(ctx, nil)
	require.EqualValues(t, 503, errors.FromError(err).Code)
	require.Equal(t, "ACCELERATOR_SERVICE_ACCESS_DENIED", errors.FromError(err).Reason)
	require.NotContains(t, err.Error(), "private downstream")
	conflict, err := status.New(codes.Aborted, "private details").WithDetails(&errdetails.ErrorInfo{Reason: "SUPPLY_DISCOVERY_CONFLICT"})
	require.NoError(t, err)
	client.failure = conflict.Err()
	_, err = service.RegisterSupplyConfiguration(ctx, supplyRegistrationRequest())
	require.EqualValues(t, 409, errors.FromError(err).Code)
	require.Equal(t, "SUPPLY_DISCOVERY_CONFLICT", errors.FromError(err).Reason)
	client.failure = nil
	client.candidates = nil
	_, err = service.ListSupplyCandidates(ctx, nil)
	require.EqualValues(t, 503, errors.FromError(err).Code)
	client.result.Configurations[0].ConnectionRef = "other-managed-connection"
	_, err = service.RegisterSupplyConfiguration(ctx, supplyRegistrationRequest())
	require.EqualValues(t, 503, errors.FromError(err).Code)
}

// Gov must not turn deprecating a declaration into a different registration
// intent. ACC owns new-registration defaults and historical receipt matching.
func TestAcceleratorSupplyPluginCoreDeclarationOptionalAndLegacyReplayUnchanged(t *testing.T) {
	for _, value := range []string{"", "true", "false"} {
		name := value
		if name == "" {
			name = "omitted"
		}
		t.Run(name, func(t *testing.T) {
			service, client, ctx := supplyServiceFixture(t, "accelerator:supply:register")
			request := supplyRegistrationRequest()
			entry := request.Data.Registrations[0]
			maximum := uint32(2)
			entry.MaxDevicesPerReplica = 0
			entry.WholeMaxDevicesPerReplica = &maximum
			entry.Declarations = nil
			if value != "" {
				entry.Declarations = []*view.AcceleratorSupplyDeclaration{{Field: "core_budget_additive", Value: value, Source: "historical explicit administrator declaration"}}
			}
			original, err := protojson.Marshal(request.Data)
			require.NoError(t, err)
			_, err = service.RegisterSupplyConfiguration(ctx, request)
			require.NoError(t, err)
			first := proto.Clone(client.registered.Registrations[0]).(*acc.SupplyRegistrationInput)
			require.Equal(t, request.Data.IdempotencyKey, client.registered.Mutation.IdempotencyKey)
			require.EqualValues(t, 2, first.GetWholeMaxDevicesPerReplica())
			require.Zero(t, first.MaxDevicesPerReplica)
			if value == "" {
				require.Empty(t, first.Declarations, "omission must not synthesize a true/false declaration")
			} else {
				require.Len(t, first.Declarations, 1)
				require.Equal(t, "core_budget_additive", first.Declarations[0].Field)
				require.Equal(t, value, first.Declarations[0].Value)
				require.Equal(t, entry.Declarations[0].Source, first.Declarations[0].Source)
			}
			_, err = service.RegisterSupplyConfiguration(ctx, request)
			require.NoError(t, err)
			require.True(t, proto.Equal(first, client.registered.Registrations[0]))
			require.Equal(t, request.Data.IdempotencyKey, client.registered.Mutation.IdempotencyKey)
			after, err := protojson.Marshal(request.Data)
			require.NoError(t, err)
			require.Equal(t, string(original), string(after), "BFF must preserve the caller's original business intent")
			require.Nil(t, client.listed, "receipt replay must not pre-read live candidate discovery")
		})
	}
}

func TestAcceleratorSupplyBaselineCoreBudgetPresenceRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name  string
		value *bool
	}{
		{"new_omitted", nil}, {"legacy_false", proto.Bool(false)}, {"legacy_true", proto.Bool(true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline := &acc.ProviderBaseline{
				BaselineId: uuid.NewString(), AdapterId: "volcano-hami-mib-v1", MemoryBlockMib: 1, SplitCount: 10, RuntimeClassName: "nvidia", CoreBudgetAdditive: test.value,
				ResourceTypes: []*acc.ResourceTypeConfig{{AllocationType: acc.AllocationType_DYNAMIC_SHARED, ResourceKey: "volcano.sh/vgpu-number", MaxDevicesPerReplica: 1, MaxMemoryMibPerDevice: 15360}},
			}
			output := wireAcceleratorBaseline(baseline)
			require.Equal(t, test.value, output.CoreBudgetAdditive)
			encoded, err := (protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: true}).Marshal(output)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &fields))
			if test.value == nil {
				require.NotContains(t, fields, "core_budget_additive", "new omission must remain omitted even with the public codec's EmitUnpopulated option")
			} else {
				require.Contains(t, fields, "core_budget_additive")
				var value bool
				require.NoError(t, json.Unmarshal(fields["core_budget_additive"], &value))
				require.Equal(t, *test.value, value)
			}
			input, err := acceleratorBaselineInput(output)
			require.NoError(t, err)
			require.Equal(t, test.value, input.CoreBudgetAdditive, "legacy write replay must retain explicit false and true")

			_, client, _ := supplyServiceFixture(t)
			client.result.Configurations[0].Baseline = baseline
			registration, err := wireAcceleratorSupplyRegistrationResult(client.result)
			require.NoError(t, err)
			require.Equal(t, test.value, registration.Configurations[0].Baseline.CoreBudgetAdditive, "the registration receipt's protojson bridge must preserve the same presence")
		})
	}
}
