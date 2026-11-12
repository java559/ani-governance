package service

import (
	"context"

	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"

	view "go-wind-admin/api/gen/go/catalog/service/v1"
)

func (s *AcceleratorService) ListSupplyCandidates(ctx context.Context, request *view.ListAcceleratorSupplyCandidatesRequest) (*view.AcceleratorSupplyCandidates, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:discovery:list"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.client.Admin == nil {
		return nil, acceleratorUnavailable()
	}
	page, size := uint32(1), uint32(20)
	if request != nil && request.Page != nil {
		page = *request.Page
	}
	if request != nil && request.PageSize != nil {
		size = *request.PageSize
	}
	if page == 0 || size == 0 || size > 200 {
		return nil, acceleratorInvalid()
	}
	result, err := s.client.Admin.ListSupplyCandidates(ctx, &acc.ListSupplyCandidatesRequest{Context: trusted, ConnectionRef: request.GetConnectionRef(), Page: page, PageSize: size})
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	return wireAcceleratorSupplyCandidates(result)
}

func (s *AcceleratorService) RegisterSupplyConfiguration(ctx context.Context, request *view.RegisterAcceleratorSupplyConfigurationRequest) (*view.AcceleratorSupplyRegistrationResult, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:supply:register"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.client.Admin == nil {
		return nil, acceleratorUnavailable()
	}
	input, err := acceleratorSupplyRegistrationInput(request.GetData())
	if err != nil {
		return nil, err
	}
	input.Mutation, err = acceleratorMutation(trusted, request.GetData().IdempotencyKey)
	if err != nil {
		return nil, err
	}
	result, err := s.client.Admin.RegisterSupplyConfiguration(ctx, input)
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	return wireAcceleratorSupplyRegistrationResult(result)
}

func (s *AcceleratorService) GetSupplyRegistration(ctx context.Context, request *view.GetAcceleratorSupplyRegistrationRequest) (*view.AcceleratorSupplyRegistrationResult, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:discovery:list"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.client.Admin == nil {
		return nil, acceleratorUnavailable()
	}
	if !acceleratorInitializationID(request.GetRegistrationId()) {
		return nil, acceleratorInvalid()
	}
	result, err := s.client.Admin.GetSupplyRegistration(ctx, &acc.GetSupplyRegistrationRequest{Context: trusted, RegistrationId: request.RegistrationId})
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	output, err := wireAcceleratorSupplyRegistrationResult(result)
	if err != nil {
		return nil, err
	}
	if output.RegistrationId != request.RegistrationId {
		return nil, acceleratorInvalidResponse()
	}
	return output, nil
}

func acceleratorSupplyRegistrationInput(input *view.RegisterAcceleratorSupplyConfigurationData) (*acc.RegisterSupplyConfigurationRequest, error) {
	if input == nil || input.DiscoveryRevision == "" || len(input.DiscoveryRevision) > 8192 || len(input.Registrations) == 0 || len(input.Registrations) > 32 {
		return nil, acceleratorInvalid()
	}
	output := &acc.RegisterSupplyConfigurationRequest{ConnectionRef: input.ConnectionRef, DiscoveryRevision: input.DiscoveryRevision}
	candidateCount := 0
	for _, entry := range input.Registrations {
		if entry == nil || len(entry.CandidateIds) == 0 || len(entry.Declarations) > 8 {
			return nil, acceleratorInvalid()
		}
		candidateCount += len(entry.CandidateIds)
		if candidateCount > 1024 {
			return nil, acceleratorInvalid()
		}
		mapped := &acc.SupplyRegistrationInput{
			ClientKey: entry.ClientKey, CandidateIds: append([]string(nil), entry.CandidateIds...),
			DisplayName: entry.DisplayName, MaxDevicesPerReplica: entry.MaxDevicesPerReplica, WholeMaxDevicesPerReplica: entry.WholeMaxDevicesPerReplica,
			RuntimeClassName:         entry.RuntimeClassName,
			ResourceMappingConfirmed: entry.ResourceMappingConfirmed, ResourceMappingSource: entry.ResourceMappingSource,
		}
		for _, declaration := range entry.Declarations {
			if declaration == nil {
				return nil, acceleratorInvalid()
			}
			mapped.Declarations = append(mapped.Declarations, &acc.SupplyDeclaration{Field: declaration.Field, Value: declaration.Value, Source: declaration.Source})
		}
		output.Registrations = append(output.Registrations, mapped)
	}
	return output, nil
}

func wireAcceleratorSupplyCandidates(input *acc.SupplyCandidates) (*view.AcceleratorSupplyCandidates, error) {
	output := &view.AcceleratorSupplyCandidates{}
	if err := acceleratorInitializationView(input, output); err != nil {
		return nil, err
	}
	if uint64(len(output.Items)) > output.Total {
		return nil, acceleratorInvalidResponse()
	}
	for _, check := range output.Checks {
		if check == nil || check.Key == "" || !acceleratorInitializationReason(check.Reason) {
			return nil, acceleratorInvalidResponse()
		}
	}
	for _, candidate := range output.Items {
		if !validAcceleratorSupplyCandidate(candidate) {
			return nil, acceleratorInvalidResponse()
		}
	}
	return output, nil
}

func validAcceleratorSupplyCandidate(candidate *view.AcceleratorSupplyCandidate) bool {
	if candidate == nil || candidate.CandidateId == "" || candidate.Node == nil || candidate.Node.Uid == "" || candidate.Node.Name == "" {
		return false
	}
	if _, valid := acceleratorPublicReasons(candidate.BlockingReasons); !valid {
		return false
	}
	for _, source := range candidate.Sources {
		if source == nil || source.Field == "" || source.Source != "AUTOMATIC_OBSERVATION" && source.Source != "MANUAL_DECLARATION" {
			return false
		}
	}
	return true
}

func wireAcceleratorSupplyRegistrationResult(input *acc.SupplyRegistrationResult) (*view.AcceleratorSupplyRegistrationResult, error) {
	output := &view.AcceleratorSupplyRegistrationResult{}
	if err := acceleratorInitializationView(input, output); err != nil {
		return nil, err
	}
	if !acceleratorInitializationID(output.RegistrationId) || output.ConnectionRef == "" || len(output.Configurations) == 0 {
		return nil, acceleratorInvalidResponse()
	}
	for _, configuration := range output.Configurations {
		if configuration == nil || configuration.RegistrationId != output.RegistrationId || configuration.ConnectionRef != output.ConnectionRef || configuration.ProviderConfigRef == "" || configuration.Version == 0 || configuration.Baseline == nil {
			return nil, acceleratorInvalidResponse()
		}
		for _, candidate := range configuration.Candidates {
			if !validAcceleratorSupplyCandidate(candidate) {
				return nil, acceleratorInvalidResponse()
			}
		}
	}
	return output, nil
}

func (s *AcceleratorService) GetSupplyConfiguration(ctx context.Context, request *view.GetAcceleratorSupplyConfigurationRequest) (*view.AcceleratorRegisteredSupplyConfiguration, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:discovery:list"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.client.Admin == nil {
		return nil, acceleratorUnavailable()
	}
	if request.GetConnectionRef() == "" || request.GetProviderConfigRef() == "" || len(request.GetConnectionRef()) > 256 || len(request.GetProviderConfigRef()) > 256 {
		return nil, acceleratorInvalid()
	}
	result, err := s.client.Admin.GetSupplyConfiguration(ctx, &acc.GetSupplyConfigurationRequest{Context: trusted, ConnectionRef: request.ConnectionRef, ProviderConfigRef: request.ProviderConfigRef})
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	output := &view.AcceleratorRegisteredSupplyConfiguration{}
	if err := acceleratorInitializationView(result, output); err != nil {
		return nil, err
	}
	if output.ConnectionRef != request.ConnectionRef || output.ProviderConfigRef != request.ProviderConfigRef || output.Version == 0 || output.Baseline == nil {
		return nil, acceleratorInvalidResponse()
	}
	for _, candidate := range output.Candidates {
		if !validAcceleratorSupplyCandidate(candidate) {
			return nil, acceleratorInvalidResponse()
		}
	}
	return output, nil
}

func (s *AcceleratorService) PreviewSupplyGroupPolicy(ctx context.Context, request *view.PreviewAcceleratorSupplyGroupPolicyRequest) (*view.AcceleratorSupplyGroupPolicyPreview, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:discovery:list"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.client.Admin == nil {
		return nil, acceleratorUnavailable()
	}
	input := request.GetData()
	if input == nil || input.ConnectionRef == "" || input.ProviderConfigRef == "" || input.ProviderConfigVersion == 0 || input.DiscoveryRevision == "" || len(input.DiscoveryRevision) > 8192 || len(input.Nodes) < 1 || len(input.Nodes) > 1024 {
		return nil, acceleratorInvalid()
	}
	query := &acc.PreviewSupplyGroupPolicyRequest{Context: trusted, ConnectionRef: input.ConnectionRef, ProviderConfigRef: input.ProviderConfigRef, ProviderConfigVersion: input.ProviderConfigVersion, DiscoveryRevision: input.DiscoveryRevision}
	uids, names := map[string]bool{}, map[string]bool{}
	for _, node := range input.Nodes {
		if node == nil || node.Uid == "" || node.Name == "" || uids[node.Uid] || names[node.Name] {
			return nil, acceleratorInvalid()
		}
		uids[node.Uid], names[node.Name] = true, true
		query.Nodes = append(query.Nodes, &acc.NodeRef{Uid: node.Uid, Name: node.Name})
	}
	result, err := s.client.Admin.PreviewSupplyGroupPolicy(ctx, query)
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	output := &view.AcceleratorSupplyGroupPolicyPreview{}
	if err := acceleratorInitializationView(result, output); err != nil {
		return nil, err
	}
	if output.ConnectionRef != input.ConnectionRef || output.ProviderConfigRef != input.ProviderConfigRef || output.ProviderConfigVersion != input.ProviderConfigVersion || output.DiscoveryRevision != input.DiscoveryRevision || len(output.Options) == 0 {
		return nil, acceleratorInvalidResponse()
	}
	for _, option := range output.Options {
		if option == nil || option.Policy == nil || option.Selectable != (option.Shape != nil) {
			return nil, acceleratorInvalidResponse()
		}
		if _, err := acceleratorGroupPolicyInput(option.Policy); err != nil {
			return nil, acceleratorInvalidResponse()
		}
		if _, valid := acceleratorPublicReasons(option.BlockingReasons); !valid {
			return nil, acceleratorInvalidResponse()
		}
		if option.Selectable && len(option.BlockingReasons) != 0 {
			return nil, acceleratorInvalidResponse()
		}
	}
	return output, nil
}

// nil is retained so an already accepted legacy request can reach ACC's
// receipt lookup. ACC rejects an unaccepted legacy create after that lookup.
func acceleratorGroupPolicyInput(input *view.AcceleratorSupplyGroupPolicy) (*acc.SupplyGroupPolicy, error) {
	if input == nil {
		return nil, nil
	}
	output := &acc.SupplyGroupPolicy{Parts: input.Parts, ResourceKey: input.ResourceKey}
	switch input.PolicyKind {
	case "WHOLE":
		if input.Parts != nil || input.ResourceKey != nil || input.NativeAllocationType != nil {
			return nil, acceleratorInvalid()
		}
		output.PolicyKind = acc.SupplyGroupPolicyKind_WHOLE
	case "EQUAL_SHARE":
		if input.Parts == nil || (*input.Parts != 2 && *input.Parts != 4 && *input.Parts != 8) || input.ResourceKey != nil || input.NativeAllocationType != nil {
			return nil, acceleratorInvalid()
		}
		output.PolicyKind = acc.SupplyGroupPolicyKind_EQUAL_SHARE
	case "NATIVE_RESOURCE":
		if input.Parts != nil || input.ResourceKey == nil || *input.ResourceKey == "" || input.NativeAllocationType == nil {
			return nil, acceleratorInvalid()
		}
		kind, ok := acc.AllocationType_value[*input.NativeAllocationType]
		if !ok || acc.AllocationType(kind) != acc.AllocationType_PHYSICAL && acc.AllocationType(kind) != acc.AllocationType_FIXED_PARTITION {
			return nil, acceleratorInvalid()
		}
		value := acc.AllocationType(kind)
		output.PolicyKind, output.NativeAllocationType = acc.SupplyGroupPolicyKind_NATIVE_RESOURCE, &value
	default:
		return nil, acceleratorInvalid()
	}
	return output, nil
}
