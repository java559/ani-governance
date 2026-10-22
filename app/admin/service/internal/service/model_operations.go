package service

import (
	"context"
	"strings"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
)

type modelOperationReader interface {
	ListModelOperations(context.Context, string, string, *modelv1.ListModelOperationsRequest) (*modelv1.ListModelOperationsResponse, error)
}

func (s *ModelService) ListModelOperations(ctx context.Context, in *catalogv1.ListModelOperationsRequest) (*catalogv1.ListModelOperationsResponse, error) {
	if in == nil || strings.TrimSpace(in.ModelId) == "" {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_REQUEST", "model_id is required")
	}
	if (in.Page != nil && (*in.Page < 1 || *in.Page > 100)) || (in.PageSize != nil && (*in.PageSize < 1 || *in.PageSize > 100)) {
		return nil, kratoserrors.BadRequest("INVALID_MODEL_REQUEST", "page and pageSize must be between 1 and 100")
	}
	tenant, actor, err := s.trustedModelOperator(ctx)
	if err != nil {
		return nil, err
	}
	reader, ok := s.client.(modelOperationReader)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("MODEL_UNAVAILABLE", "Model operation history is not configured")
	}
	// Resolve either the public selector or internal ID within the trusted tenant;
	// verify all returned events belong to that canonical model before exposing them.
	model, err := s.client.GetModel(ctx, tenant, actor, &modelv1.GetModelRequest{ModelId: in.ModelId})
	if err != nil {
		return nil, mapModelError(err)
	}
	if err := validateModelReply(model, tenant, in.ModelId); err != nil {
		return nil, err
	}
	reply, err := reader.ListModelOperations(ctx, tenant, actor, &modelv1.ListModelOperationsRequest{ModelId: model.Id, Page: in.Page, PageSize: in.PageSize})
	if err != nil {
		return nil, mapModelError(err)
	}
	limit := int32(20)
	if in.PageSize != nil {
		limit = *in.PageSize
	}
	if reply == nil || len(reply.Items) > int(limit) || reply.Total < int64(len(reply.Items)) {
		return nil, modelInvalidReply()
	}
	out := &catalogv1.ListModelOperationsResponse{Items: make([]*catalogv1.ModelOperation, 0, len(reply.Items)), Total: reply.Total}
	for _, event := range reply.Items {
		if event == nil || event.Id == "" || event.ModelId != model.Id || event.Action == "" || event.CreatedAt == nil || event.CreatedAt.CheckValid() != nil {
			return nil, modelInvalidReply()
		}
		switch event.Result {
		case "accepted", "succeeded", "failed":
		default:
			return nil, modelInvalidReply()
		}
		out.Items = append(out.Items, &catalogv1.ModelOperation{Id: event.Id, ModelId: event.ModelId, ModelVersionId: event.ModelVersionId, Action: event.Action, Actor: event.Actor, Result: event.Result, BeforeState: event.BeforeState, AfterState: event.AfterState, ErrorClass: event.ErrorClass, RequestId: event.RequestId, CreatedAt: event.CreatedAt, TaskId: event.TaskId})
	}
	return out, nil
}
