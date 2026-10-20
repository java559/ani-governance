package service

import (
	"context"

	"github.com/google/uuid"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	quota "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/protobuf/encoding/protojson"
)

type instanceConfigurationClient interface {
	MountablePVCs(context.Context, string, string, *res.ListMountablePVCsRequest) (*res.ListMountablePVCsResponse, error)
	Configuration(context.Context, string, string, string) (*res.InstanceConfiguration, error)
}

func (s *InstanceService) ListMountablePVCs(ctx context.Context, request *view.ListMountablePVCsRequest) (*view.ListMountablePVCsResponse, error) {
	if request == nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "request required")
	}
	// Candidates permit the same current create authority as the eventual request.
	tenant, actor, err := s.scope(ctx, data.InstanceCreatePath, "POST")
	if err != nil {
		return nil, err
	}
	page, size, err := resolveComputePage(request.Page, request.PageSize)
	if err != nil {
		return nil, err
	}
	client, ok := s.binding.client.(instanceConfigurationClient)
	if !ok {
		return nil, quota.ErrorQuotaAdapterUnavailable("%s", "instance owner unavailable")
	}
	result, err := client.MountablePVCs(ctx, tenant, actor, &res.ListMountablePVCsRequest{TenantId: tenant, Page: page, PageSize: size, Name: request.Name})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance candidates")
	}
	raw, err := protojson.Marshal(result)
	if err != nil {
		return nil, err
	}
	out := new(view.ListMountablePVCsResponse)
	if err = protojson.Unmarshal(raw, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *InstanceService) GetInstanceConfiguration(ctx context.Context, request *view.GetInstanceRequest) (*view.InstanceConfiguration, error) {
	if request == nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "request required")
	}
	if _, err := uuid.Parse(request.InstanceId); err != nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance id")
	}
	// Instance read grants literal env access; values never enter lists or diagnostics.
	tenant, actor, err := s.scope(ctx, data.InstanceGetPath, "GET")
	if err != nil {
		return nil, err
	}
	client, ok := s.binding.client.(instanceConfigurationClient)
	if !ok {
		return nil, quota.ErrorQuotaAdapterUnavailable("%s", "instance owner unavailable")
	}
	result, err := client.Configuration(ctx, tenant, actor, request.InstanceId)
	if err != nil {
		return nil, err
	}
	if result == nil || result.InstanceId != request.InstanceId || result.InstanceContractVersion != data.CurrentInstanceContractVersion {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance configuration")
	}
	raw, err := protojson.Marshal(result)
	if err != nil {
		return nil, err
	}
	out := new(view.InstanceConfiguration)
	if err = protojson.Unmarshal(raw, out); err != nil {
		return nil, err
	}
	return out, nil
}
