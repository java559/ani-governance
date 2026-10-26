package service

import (
	"context"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"google.golang.org/protobuf/proto"
)

func TestVMCreateRejectsInvalidDNSBeforeAdmission(t *testing.T) {
	s := NewVMService(new(GpuAcceptance), nil, nil, nil, nil)
	for _, selection := range []*view.VMDNSSelection{
		{Mode: "UNKNOWN"},
		{Mode: vm.DNSPlatformDefault, Nameservers: []string{"223.5.5.5"}},
		{Mode: vm.DNSCustom},
		{Mode: vm.DNSCustom, Nameservers: []string{"dns.example.test"}},
		{Mode: vm.DNSCustom, Nameservers: []string{"223.5.5.5:53"}},
		{Mode: vm.DNSCustom, Nameservers: []string{"fe80::1%eth0"}},
		{Mode: vm.DNSCustom, Nameservers: []string{"223.5.5.5", "1.1.1.1", "8.8.8.8", "9.9.9.9"}},
	} {
		_, err := s.CreateVM(context.Background(), &view.CreateVMRequest{Data: &view.CreateVMData{Dns: selection}})
		require.Equal(t, 400, kratoserrors.Code(err), selection.String())
		require.Equal(t, "VM_INVALID_REQUEST", kratoserrors.Reason(err))
	}
}

func TestVMDNSFrozenSummaryAndIntentHash(t *testing.T) {
	spec := vmFixtureSpec(t)
	spec.Dns = &vm.FrozenDNS{Mode: vm.DNSPlatformDefault, Nameservers: []string{"223.5.5.5"}}
	out := new(view.VirtualMachine)
	vmSummary(spec, out)
	require.Equal(t, vm.DNSPlatformDefault, out.Dns.Mode)
	require.Equal(t, []string{"223.5.5.5"}, out.Dns.Nameservers)
	spec.Dns.Nameservers[0] = "1.1.1.1"
	require.Equal(t, []string{"223.5.5.5"}, out.Dns.Nameservers, "display is a copy of frozen addresses")
	binding := new(VMBinding)
	omitted, err := binding.IntentHash("tenant", "user", "7", spec.Intent)
	require.NoError(t, err)
	explicit := proto.Clone(spec.Intent).(*vm.CreateVMData)
	explicit.Dns = &vm.DNSSelection{Mode: vm.DNSPlatformDefault}
	same, err := binding.IntentHash("tenant", "user", "7", explicit)
	require.NoError(t, err)
	require.Equal(t, omitted, same, "explicit default and omitted DNS are the same user intent")
	explicit.Dns = &vm.DNSSelection{Mode: vm.DNSCustom, Nameservers: []string{"223.5.5.5"}}
	different, err := binding.IntentHash("tenant", "user", "7", explicit)
	require.NoError(t, err)
	require.NotEqual(t, omitted, different, "custom DNS participates in idempotency")
}
