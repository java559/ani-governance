package service

import (
	"context"
	"strings"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	operationview "go-wind-admin/api/gen/go/inference/view/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *GpuAcceptance) inferenceOriginal(ctx context.Context, resource string) (*ent.QuotaOperation, *commandcontract.InferenceCanonical, error) {
	p, e := gpuPrincipal(ctx)
	if e != nil {
		return nil, nil, e
	}
	op, e := s.ledger.GetCreateOperationByResource(ctx, p.TenantID, commandcontract.InferenceOwner, resource)
	if e != nil {
		return nil, nil, e
	}
	if _, e = s.ledger.GetChargesForOperation(ctx, p.TenantID, op.OperationID); e != nil {
		return nil, nil, e
	}
	c, e := commandcontract.DecodeInferenceCanonical([]byte(op.CanonicalRequest))
	return op, c, e
}
func (s *GpuAcceptance) inferenceOperation(ctx context.Context, id string) (*ent.QuotaOperation, string, error) {
	p, e := gpuPrincipal(ctx)
	if e != nil {
		return nil, "", e
	}
	op, e := s.ledger.GetOperationForUser(ctx, p.TenantID, id)
	if e != nil {
		return nil, "", e
	}
	if op.OwnerService != commandcontract.InferenceOwner {
		return nil, "", commandcontract.Invalid("inference operation not found")
	}
	createID := op.OperationID
	if op.CreateOperationID != nil {
		createID = *op.CreateOperationID
	}
	charges, e := s.ledger.GetChargesForOperation(ctx, p.TenantID, createID)
	if e != nil {
		return nil, "", e
	}
	refund := "NONE"
	if len(charges) > 0 {
		refund = "PENDING"
		all := true
		for _, q := range charges {
			all = all && q.OriginalUnits == q.ReleasedUnits
		}
		if all {
			refund = "APPLIED"
		}
	}
	return op, refund, nil
}
func acceptedInferenceView(op *ent.QuotaOperation, c *commandcontract.InferenceCanonical) *view.InferenceView {
	out := &view.InferenceView{Id: op.ResourceID, Phase: string(op.DispatchState)}
	if business, e := runtimev1.DecodeBusinessPayload(c.BusinessPayload); e == nil {
		out.Name = business.Name
		out.ModelVersionId = business.ModelVersionId
		out.ServedModelName = business.ServedModelName
	}
	applyInferenceSource(out, c)
	return out
}
func applyInferenceSource(out *view.InferenceView, c *commandcontract.InferenceCanonical) {
	if out == nil || c == nil || c.Selection == nil {
		return
	}
	out.PresetId = c.Selection.PresetId
	out.PresetCode = c.Selection.Code
	out.CpuMillis = c.Selection.CpuMillis
	out.MemoryMib = c.Selection.MemoryMib
	out.SelectionDigest = c.Selection.SelectionDigest
}
func acceptedInferenceOperation(op *ent.QuotaOperation, refund string) *operationview.Operation {
	kind := "CREATE"
	if op.CreateOperationID != nil {
		kind = "DELETE"
	}
	out := &operationview.Operation{Id: op.OperationID, ServiceId: op.ResourceID, Kind: kind, Phase: string(op.DispatchState), GovDispatchState: string(op.DispatchState), RefundState: refund}
	if op.CreatedAt != nil {
		out.CreatedAt = timestamppb.New(*op.CreatedAt)
	}
	if op.LastErrorCode != nil {
		out.ErrorCode = *op.LastErrorCode
	}
	if strings.EqualFold(string(op.DispatchState), "CANCELED_UNSENT") {
		out.Step = "CANCELED_BEFORE_DISPATCH"
	}
	return out
}
