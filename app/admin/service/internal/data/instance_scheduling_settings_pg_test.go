//go:build quota_pg

package data

import (
	"context"
	"sync"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	configpb "go-wind-admin/api/gen/go/config/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent/sysconfig"
	"go-wind-admin/app/admin/service/tests/testutil"
	pagination "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestInstanceSchedulingSettingsPostgresAtomicAndManagedGuard(t *testing.T) {
	ctx := testutil.NewSystemViewerCtx(context.Background())
	client := testutil.NewQuotaPGClient(t)
	repo := NewConfigRepo(testutil.NewBootstrapContext(nil), client, nil)
	t.Cleanup(repo.Close)
	cluster := uuid.NewString()
	key := instanceSchedulingKeyPrefix + cluster
	t.Cleanup(func() {
		_, err := client.Client().SysConfig.Delete().Where(sysconfig.KeyEQ(key)).Exec(ctx)
		if err != nil {
			t.Errorf("owned config cleanup: %v", err)
		}
	})
	absent, err := repo.ReadInstanceSchedulingSettings(ctx, cluster)
	require.NoError(t, err)
	require.Nil(t, absent)
	initial := []byte(`{"defaults":{},"priority_class_allowlists":[]}`)
	require.NoError(t, repo.SaveInstanceSchedulingSettings(ctx, cluster, 11, initial))
	original, err := client.Client().SysConfig.Query().Where(sysconfig.KeyEQ(key)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, sysconfig.ValueTypeString, *original.ValueType)
	require.True(t, *original.IsBuiltIn)
	// Two whole-record updates share the existing unique key and cannot leave a
	// mixed default/allowlist value or replace creation identity and timestamp.
	first := []byte(`{"defaults":{"pod_placement":"COLOCATE_PREFERRED"},"priority_class_allowlists":[{"tenant_id":1,"priority_class_names":["first"]}]}`)
	second := []byte(`{"defaults":{"pod_placement":"SPREAD_PREFERRED"},"priority_class_allowlists":[{"tenant_id":2,"priority_class_names":["second"]}]}`)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for n, raw := range [][]byte{first, second} {
		n, raw := n, raw
		group.Add(1)
		go func() {
			defer group.Done()
			results <- repo.SaveInstanceSchedulingSettings(ctx, cluster, uint32(20+n), raw)
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	saved, err := client.Client().SysConfig.Query().Where(sysconfig.KeyEQ(key)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, original.ID, saved.ID)
	require.Equal(t, original.CreatedAt, saved.CreatedAt)
	require.EqualValues(t, 11, *saved.CreatedBy)
	require.Contains(t, []string{string(first), string(second)}, *saved.Value)
	expectedActor := uint32(20)
	if *saved.Value == string(second) {
		expectedActor = 21
	}
	require.Equal(t, expectedActor, *saved.UpdatedBy)
	count, err := client.Client().SysConfig.Query().Where(sysconfig.KeyEQ(key)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	read, err := repo.ReadInstanceSchedulingSettings(ctx, cluster)
	require.NoError(t, err)
	require.Equal(t, *saved.Value, string(read))
	// Generic config permissions cannot expose or rewrite the dedicated settings
	// record, even by ID or by renaming another record into its reserved key.
	_, err = repo.Get(ctx, &configpb.GetConfigRequest{QueryBy: &configpb.GetConfigRequest_Id{Id: saved.ID}})
	require.Error(t, err)
	list, err := repo.List(ctx, &pagination.PagingRequest{})
	require.NoError(t, err)
	for _, row := range list.Items {
		require.NotEqual(t, key, row.GetKey())
	}
	err = repo.Create(ctx, &configpb.CreateConfigRequest{Data: &configpb.Config{Key: proto.String(key), Value: proto.String("bypass")}})
	require.Equal(t, 403, errors.Code(err))
	err = repo.Update(ctx, &configpb.UpdateConfigRequest{Id: saved.ID, Data: &configpb.Config{Value: proto.String("bypass")}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"value"}}})
	require.Equal(t, 403, errors.Code(err))
	err = repo.Delete(ctx, &configpb.DeleteConfigRequest{QueryBy: &configpb.DeleteConfigRequest_Id{Id: saved.ID}})
	require.Equal(t, 403, errors.Code(err))
	final, err := repo.ReadInstanceSchedulingSettings(ctx, cluster)
	require.NoError(t, err)
	require.Equal(t, read, final)
}
