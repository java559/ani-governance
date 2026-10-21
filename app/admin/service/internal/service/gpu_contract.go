package service

import (
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	GpuPhysicalQuotaCode = commandcontract.GpuPhysicalQuotaCode
	GpuSharedQuotaCode   = commandcontract.GpuSharedQuotaCode
	GpuMeteringVersion   = commandcontract.GpuMeteringVersion
)

type GpuCanonical = commandcontract.GpuCanonical
type GpuQuotaItem = commandcontract.GpuQuotaItem

func gpuCanonicalMessage(m protoreflect.Message) (map[string]any, error) {
	return commandcontract.CanonicalMessage(m)
}
func gpuCanonicalJSON(v any) ([]byte, error)          { return commandcontract.CanonicalJSON(v) }
func gpuHash(v any) (string, error)                   { return commandcontract.Hash(v) }
func gpuSpecDigest(p *acc.GpuProfile) (string, error) { return commandcontract.SpecDigest(p) }
func GpuProjectionDigest(p *acc.GpuUsageProjection) (string, error) {
	return commandcontract.GpuProjectionDigest(p)
}
func GpuResolvedQuota(p *acc.ResolvedGpuRequest, r *acc.GpuRequest) (commandcontract.QuotaItem, error) {
	return commandcontract.GpuResolvedQuota(p, r)
}
func isGpuCanonical(raw []byte) bool { return commandcontract.IsGpuCanonical(raw) }
func DecodeGpuCanonical(raw []byte) (*GpuCanonical, error) {
	return commandcontract.DecodeGpuCanonical(raw)
}
func ValidateGpuCharges(c *GpuCanonical, refs []QuotaChargeRef) error {
	charges := make([]commandcontract.ChargeRef, 0, len(refs))
	for _, q := range refs {
		charges = append(charges, commandcontract.ChargeRef{ChargeID: q.ChargeID, QuotaCode: q.QuotaCode, ChargedUnits: q.ChargedUnits})
	}
	return commandcontract.ValidateGpuCharges(c, charges)
}
