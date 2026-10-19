//go:build quota_pg

package service

import (
	"context"
	"errors"
	"testing"
)

type instanceSchedulingReplayStore struct {
	InstanceSchedulingStore
	reads int
	fail  bool
}

func (s *instanceSchedulingReplayStore) ReadInstanceSchedulingSettings(ctx context.Context, cluster string) ([]byte, error) {
	s.reads++
	if s.fail {
		return nil, errors.New("current settings storage unavailable")
	}
	return s.InstanceSchedulingStore.ReadInstanceSchedulingSettings(ctx, cluster)
}
func TestInstanceSchedulingReplayRetainsSettingsSnapshot(t *testing.T) {
	instanceCurrentAuthorizationOriginalReplayAndOwnerRelease(t, true, true)
}
