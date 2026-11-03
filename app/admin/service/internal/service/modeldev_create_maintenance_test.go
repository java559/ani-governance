package service

import (
	"context"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	"go-wind-admin/pkg/middleware/auth"
)

func TestModelDevCreateMaintenanceRejectsAdmissionAndPublisherBeforeDependencies(t *testing.T) {
	gate := NewModelDevCreateMaintenance(true)
	s := NewModelDevService(nil, nil, nil, nil, nil)
	s.SetCreateMaintenance(gate)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 1, TenantID: 1})
	reply, err := s.CreateExecution(ctx, ModelDevCreateInput{})
	require.Nil(t, reply)
	require.Equal(t, "MODELDEV_CREATE_MAINTENANCE", kerrors.FromError(err).Reason)
	worker := NewModelDevDispatchWorker(nil, nil, nil)
	worker.SetCreateMaintenance(gate)
	require.False(t, worker.deliverNext(ctx), "paused publisher must not dereference or claim the repository")
	_, err = s.GetTenantExecutionEnvironment(ctx)
	require.Equal(t, "UPSTREAM_UNAVAILABLE", kerrors.FromError(err).Reason, "environment reads do not enter the Create maintenance gate")
}
func TestModelDevCreateMaintenanceDrainPreservesCurrentWorkAndStopsNewClaims(t *testing.T) {
	gate := NewModelDevCreateMaintenance(false)
	finish, allowed := gate.begin()
	require.True(t, allowed)
	worker := &ModelDevDispatchWorker{createMaintenance: gate, done: make(chan struct{})}
	cancelled := make(chan struct{})
	worker.cancel = func() { close(cancelled); close(worker.done) }
	stopped := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { stopped <- worker.Stop(ctx) }()
	require.Eventually(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return gate.paused }, time.Second, time.Millisecond)
	select {
	case <-cancelled:
		t.Fatal("publisher canceled current work before drain")
	default:
	}
	_, allowed = gate.begin()
	require.False(t, allowed, "new claim entered after pause")
	finish()
	finish()
	require.NoError(t, <-stopped)
	select {
	case <-cancelled:
	default:
		t.Fatal("drained publisher lifecycle was not stopped")
	}
}
func TestModelDevCreateMaintenanceCanceledDrainCannotReportSuccess(t *testing.T) {
	gate := NewModelDevCreateMaintenance(false)
	finish, allowed := gate.begin()
	require.True(t, allowed)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, gate.pauseAndDrain(ctx), context.Canceled)
	_, allowed = gate.begin()
	require.False(t, allowed)
	finish()
	require.NoError(t, gate.pauseAndDrain(context.Background()))
}
