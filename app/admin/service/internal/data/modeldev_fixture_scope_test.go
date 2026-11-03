package data

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// These are synthetic external ModelDev responses. Keep every tenant-bound
// v4 fact coherent when a Gov test assigns its own tenant/execution identity;
// the actual contract validator still decides whether admission is valid.
func bindModelDevFixtureScope(t *testing.T, s *cpup01.Snapshot) {
	t.Helper()
	tenant, execution := s.IdentityRef.TenantID, s.IdentityRef.ExecutionID
	s.Environment.NamespaceName = "tenant-" + tenant
	s.RuntimePlan.Namespace = s.Environment.NamespaceName
	s.RuntimePlan.Name = cpup01.RuntimeName(s.Environment.NamespaceUID, execution)
	s.DispatchOwner.PipelineRoot = "s3://kfp-artifacts/artifacts/" + tenant + "/" + execution
	s.PublicationScope.ApprovedPrefix = "executions/" + tenant
	parts := strings.SplitN(s.Input.Object.Key, "/", 3)
	require.Len(t, parts, 3)
	s.Input.Object.Key = "inputs/" + tenant + "/" + parts[2]
	manifest, err := cpup01.BuildDatasetManifest(s.Input)
	require.NoError(t, err)
	digest, err := manifest.Digest()
	require.NoError(t, err)
	s.Program.ResolvedArgs = append([]string(nil), s.Program.ResolvedArgs...)
	for i := 0; i+1 < len(s.Program.ResolvedArgs); i++ {
		if s.Program.ResolvedArgs[i] == "--expected-dataset-manifest-sha256" {
			s.Program.ResolvedArgs[i+1] = digest
		}
	}
}
