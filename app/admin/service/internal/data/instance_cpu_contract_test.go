package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
)

func cpuConfigurationBusiness() *res.CreateSpec {
	return &res.CreateSpec{WorkspaceMountPath: "/workspace", Name: "workspace", ImageId: "img_10000000000040008000000000000001", ImageScope: "platform", PresetId: "10000000-0000-4000-8000-000000000002", ImageReference: "registry.invalid/cpu@sha256:" + strings.Repeat("a", 64), InstanceKind: "container", ComputeType: "cpu", ClusterId: "10000000-0000-4000-8000-000000000001", Namespace: "workspaces", Cpu: "1000m", Memory: "2048Mi", ServiceAccount: "workspace", StorageClass: "cephfs", WorkspaceSize: "1Gi", Shell: "/bin/sh", Uid: 1000, Gid: 1000, Scheduling: &res.SchedulingOptions{PodPlacement: res.SchedulingOptions_NONE.Enum()}}
}

func frozenCPUConfiguration(t *testing.T) []byte {
	t.Helper()
	payload, err := res.CanonicalBusinessPayload(cpuConfigurationBusiness())
	require.NoError(t, err)
	sum := sha256.Sum256(payload)
	raw, err := MarshalInstanceCPUCanonical(&InstanceCPUCanonical{Schema: InstanceCPUCanonicalSchema, BusinessPayload: payload, BusinessPayloadDigest: hex.EncodeToString(sum[:])})
	require.NoError(t, err)
	return raw
}

func TestInstanceCPUFrozenRejectsConfigurationVersionField(t *testing.T) {
	original := frozenCPUConfiguration(t)
	for _, version := range []string{"0", "1", "2", "3"} {
		raw := append(append([]byte(nil), original[:len(original)-1]...), []byte(`,"instance_contract_version":`+version+"}")...)
		_, err := DecodeInstanceCPUCanonical(raw)
		require.Error(t, err, "a retired configuration version is an unknown field")
		require.False(t, AllowsInstanceCPUCommand(instanceCPUOwner, instanceCPUCreateAction, string(raw)))
	}
}

func TestInstanceCPUFrozenPreservesOriginalBytes(t *testing.T) {
	raw := frozenCPUConfiguration(t)
	command, err := DecodeInstanceCPUCanonical(raw)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "instance_contract_version")
	require.True(t, AllowsInstanceCPUCommand(instanceCPUOwner, instanceCPUCreateAction, string(raw)))
	encoded, err := MarshalInstanceCPUCanonical(command)
	require.NoError(t, err)
	require.Equal(t, raw, encoded)
}

func TestInstanceCPUFrozenRejectsNoncanonicalPayloadAndTampering(t *testing.T) {
	var original InstanceCPUCanonical
	require.NoError(t, json.Unmarshal(frozenCPUConfiguration(t), &original))
	for _, injected := range []string{`,"unexpected":"private"`, `,"name":"workspace"`} {
		payload := append(append([]byte(nil), original.BusinessPayload[:len(original.BusinessPayload)-1]...), []byte(injected+"}")...)
		sum := sha256.Sum256(payload)
		changed := original
		changed.BusinessPayload, changed.BusinessPayloadDigest = payload, hex.EncodeToString(sum[:])
		raw, err := MarshalInstanceCPUCanonical(&changed)
		require.NoError(t, err)
		_, err = DecodeInstanceCPUCanonical(raw)
		require.Error(t, err, "rehashing cannot authorize unknown or duplicate fields")
		require.NotContains(t, err.Error(), "private")
	}
	changed := original
	changed.BusinessPayloadDigest = strings.Repeat("0", 64)
	raw, err := MarshalInstanceCPUCanonical(&changed)
	require.NoError(t, err)
	_, err = DecodeInstanceCPUCanonical(raw)
	require.Error(t, err, "payload digest must match the stored literal bytes")
	raw = append(frozenCPUConfiguration(t), []byte(" {}")...)
	_, err = DecodeInstanceCPUCanonical(raw)
	require.Error(t, err, "trailing envelope data is invalid")
}

func TestInstanceCPUCurrentLiteralEnvironmentBytesSurviveEnvelopeAndDelete(t *testing.T) {
	business := cpuConfigurationBusiness()
	business.WorkspaceMountPath = "/work"
	const literal = "  研究 <tag>& $(LATER)\n第二行  "
	business.Env = []*res.InstanceEnvVar{{Name: "FIRST", Value: literal}, {Name: "LATER", Value: "unchanged"}}
	payload, err := res.CanonicalBusinessPayload(business)
	require.NoError(t, err)
	sum := sha256.Sum256(payload)
	command := InstanceCPUCanonical{Schema: InstanceCPUCanonicalSchema, BusinessPayload: payload, BusinessPayloadDigest: hex.EncodeToString(sum[:])}
	var expected bytes.Buffer
	encoder := json.NewEncoder(&expected)
	encoder.SetEscapeHTML(false)
	require.NoError(t, encoder.Encode(command))
	raw := bytes.TrimSuffix(expected.Bytes(), []byte{'\n'})
	escaped, err := json.Marshal(command)
	require.NoError(t, err)
	_, err = DecodeInstanceCPUCanonical(escaped)
	require.Error(t, err, "an envelope cannot rewrite signed literal payload bytes")
	decoded, err := DecodeInstanceCPUCanonical(raw)
	require.NoError(t, err)
	require.Equal(t, payload, []byte(decoded.BusinessPayload))
	effective, err := res.DecodeBusinessPayload(decoded.BusinessPayload)
	require.NoError(t, err)
	require.Equal(t, literal, effective.Env[0].Value)
	encoded, err := MarshalInstanceCPUCanonical(decoded)
	require.NoError(t, err)
	require.Equal(t, raw, encoded)
	decoded.DeleteExpectedVersion = 4
	deleted, err := MarshalInstanceCPUCanonical(decoded)
	require.NoError(t, err)
	expected.Reset()
	encoder = json.NewEncoder(&expected)
	encoder.SetEscapeHTML(false)
	require.NoError(t, encoder.Encode(decoded))
	require.Equal(t, bytes.TrimSuffix(expected.Bytes(), []byte{'\n'}), deleted)
	_, err = DecodeInstanceCPUCanonical(deleted)
	require.NoError(t, err)
}
