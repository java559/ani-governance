package service

import (
	"context"

	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/protobuf/types/known/emptypb"

	view "go-wind-admin/api/gen/go/catalog/service/v1"
)

func (s *AcceleratorService) GetInitialization(ctx context.Context, _ *emptypb.Empty) (*view.AcceleratorInitialization, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:initialization:read"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.client.Admin.GetInitialization(ctx, &acc.GetInitializationRequest{Context: trusted})
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	return wireAcceleratorInitialization(result)
}

func (s *AcceleratorService) ListDiscoveredNodes(ctx context.Context, request *view.ListAcceleratorDiscoveredNodesRequest) (*view.AcceleratorDiscoveredNodes, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:discovery:list"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
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
	result, err := s.client.Admin.ListDiscoveredNodes(ctx, &acc.ListDiscoveredNodesRequest{Context: trusted, Page: page, PageSize: size})
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	return wireAcceleratorDiscoveredNodes(result)
}

func (s *AcceleratorService) CreateInitialization(ctx context.Context, request *view.CreateAcceleratorInitializationRequest) (*view.AcceleratorInitializationOperation, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:initialization:create"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	input, actions, err := acceleratorInitializationInput(request.GetData())
	if err != nil {
		return nil, err
	}
	input.Mutation, err = acceleratorMutation(trusted, request.GetData().IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeInitializationActions(ctx, actions...); err != nil {
		return nil, err
	}
	initialization, err := s.client.Admin.GetInitialization(ctx, &acc.GetInitializationRequest{Context: trusted})
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	if initialization == nil {
		return nil, acceleratorInvalidResponse()
	}
	if !initialization.Registered {
		if err := s.authorizeInitializationActions(ctx, "accelerator:cluster:register"); err != nil {
			return nil, err
		}
	}
	result, err := s.client.Admin.CreateInitialization(ctx, input)
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	return wireAcceleratorInitializationOperation(result)
}

func (s *AcceleratorService) GetInitializationOperation(ctx context.Context, request *view.GetAcceleratorInitializationOperationRequest) (*view.AcceleratorInitializationOperation, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:initialization:read"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	if !acceleratorInitializationID(request.GetOperationId()) {
		return nil, acceleratorInvalid()
	}
	result, err := s.client.Admin.GetInitializationOperation(ctx, &acc.GetInitializationOperationRequest{Context: trusted, OperationId: request.OperationId})
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	return wireAcceleratorInitializationOperation(result)
}

func (s *AcceleratorService) RetryInitializationOperation(ctx context.Context, request *view.RetryAcceleratorInitializationOperationRequest) (*view.AcceleratorInitializationOperation, error) {
	if err := s.authorizeInitializationActions(ctx, "accelerator:initialization:retry"); err != nil {
		return nil, err
	}
	trusted, err := s.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	input := request.GetData()
	if input == nil || input.ExpectedVersion == 0 || !acceleratorInitializationID(request.GetOperationId()) {
		return nil, acceleratorInvalid()
	}
	mutation, err := acceleratorMutation(trusted, input.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	operation, err := s.client.Admin.GetInitializationOperation(ctx, &acc.GetInitializationOperationRequest{Context: trusted, OperationId: request.OperationId})
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	if operation == nil || operation.OperationId != request.OperationId {
		return nil, acceleratorInvalidResponse()
	}
	for _, action := range operation.RequiredActions {
		if _, known := acceleratorInitializationActions[action]; !known {
			return nil, acceleratorInvalidResponse()
		}
	}
	if err := s.authorizeInitializationActions(ctx, operation.RequiredActions...); err != nil {
		return nil, err
	}
	result, err := s.client.Admin.RetryInitializationOperation(ctx, &acc.RetryInitializationOperationRequest{Mutation: mutation, OperationId: request.OperationId, ExpectedVersion: input.ExpectedVersion})
	if err != nil {
		return nil, mapAcceleratorError(err)
	}
	return wireAcceleratorInitializationOperation(result)
}

func acceleratorInitializationID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
