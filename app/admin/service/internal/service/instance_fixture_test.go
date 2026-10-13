//go:build quota_pg

package service

import (
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/tests/testutil"
	"testing"
)

func instanceResourceProcess(t *testing.T, gpu *acc.GpuRequest) *data.InstanceClient {
	testutil.StartInstanceResourcePeer(t, gpu)
	client, closeClient, e := data.NewInstanceClientFromEnv()
	require.NoError(t, e)
	t.Cleanup(closeClient)
	return client
}
