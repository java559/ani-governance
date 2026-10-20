package service

import (
	"context"
	"errors"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
)

type gpuUseEligibilityProbe struct {
	calls        int
	tenant, user uint32
	err          error
}

func (p *gpuUseEligibilityProbe) AuthorizeGpuUse(_ context.Context, tenant, user uint32) error {
	p.calls++
	p.tenant, p.user = tenant, user
	return p.err
}

func TestGpuUseCombinesCurrentBusinessGrantAndIndependentEligibility(t *testing.T) {
	p := &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 11}
	ctx := auth.NewPrincipalContext(context.Background(), p)
	for _, usage := range []compute.Usage{compute.Usage_CONTAINER_INSTANCE, compute.Usage_TRAINING} {
		t.Run(usage.String(), func(t *testing.T) {
			businessCalls := 0
			grant, err := authorizeGpuBusiness(ctx, p, usage, "current-business-action", func() error {
				businessCalls++
				return nil
			})
			require.NoError(t, err)
			eligibility := new(gpuUseEligibilityProbe)
			require.NoError(t, authorizeNewGpuUse(ctx, grant, eligibility))
			require.Equal(t, 1, businessCalls)
			require.Equal(t, 1, eligibility.calls)
			require.Equal(t, p.TenantID, eligibility.tenant)
			require.Equal(t, p.ID, eligibility.user)
			other := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: p.ID, TenantID: p.TenantID + 1})
			err = authorizeNewGpuUse(other, grant, eligibility)
			require.EqualValues(t, 403, kratoserrors.FromError(err).Code)
			require.Equal(t, 1, eligibility.calls, "grant cannot cross tenants")
		})
	}
	denied := errors.New("current business permission withdrawn")
	grant, err := authorizeGpuBusiness(ctx, p, compute.Usage_TRAINING, "train-or-finetune", func() error { return denied })
	require.ErrorIs(t, err, denied)
	eligibility := new(gpuUseEligibilityProbe)
	require.Error(t, authorizeNewGpuUse(ctx, grant, eligibility))
	require.Zero(t, eligibility.calls, "eligibility cannot substitute for a business grant")
	for _, usage := range []compute.Usage{compute.Usage_USAGE_UNSPECIFIED, compute.Usage_INFERENCE, compute.Usage(999)} {
		_, err = authorizeGpuBusiness(ctx, p, usage, "deferred-consumer", func() error {
			t.Fatal("unsupported consumer must not authorize")
			return nil
		})
		require.Error(t, err)
	}
}

func TestGpuUseErrorsAndMissingGateFailClosed(t *testing.T) {
	p := &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 11}
	ctx := auth.NewPrincipalContext(context.Background(), p)
	grant, err := authorizeGpuBusiness(ctx, p, compute.Usage_CONTAINER_INSTANCE, InstanceCreateAction, func() error { return nil })
	require.NoError(t, err)
	for _, tc := range []struct {
		err  error
		code int
	}{
		{data.ErrActionAuthorizationDenied, 403},
		{data.ErrActionAuthorizationUnavailable, 503},
		{errors.New("private database detail"), 503},
	} {
		err = authorizeNewGpuUse(ctx, grant, &gpuUseEligibilityProbe{err: tc.err})
		require.EqualValues(t, tc.code, kratoserrors.FromError(err).Code)
		require.NotContains(t, err.Error(), "private database")
	}
	require.EqualValues(t, 503, kratoserrors.FromError(authorizeNewGpuUse(ctx, grant, nil)).Code)
	require.Error(t, authorizeNewGpuUse(ctx, gpuBusinessGrant{}, &gpuUseEligibilityProbe{}))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	probe := new(gpuUseEligibilityProbe)
	require.ErrorIs(t, authorizeNewGpuUse(canceled, grant, probe), context.Canceled)
	require.Zero(t, probe.calls)
	binding := NewInstanceGpuBinding(nil, nil, "cluster")
	require.Error(t, binding.AuthorizeGpuUse(ctx, p.TenantID, p.ID))
}
