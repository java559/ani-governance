package service

import "context"

// The production InstanceAuthorizationRepo supplies this independent gate.
// A missing gate fails closed for new GPU admission, without changing CPU or
// the current business-authorization check required for original replay.
func (b *InstanceGpuBinding) AuthorizeGpuUse(ctx context.Context, tenantID, userID uint32) error {
	if b == nil || b.authorization == nil {
		return gpuUseUnavailable()
	}
	eligibility, ok := b.authorization.(GpuUseEligibility)
	if !ok {
		return gpuUseUnavailable()
	}
	return eligibility.AuthorizeGpuUse(ctx, tenantID, userID)
}
