package data

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstanceSchedulerReviewedScoringChanges(t *testing.T) {
	protected, err := instanceSchedulerProtectedProjection(schedulerTestYAML)
	require.NoError(t, err)
	for _, document := range []string{
		strings.Replace(schedulerTestYAML, "nodeaffinity.weight: 2", "nodeaffinity.weight: 0", 1),
		strings.Replace(schedulerTestYAML, "  - name: nodeorder\n    arguments:\n      nodeaffinity.weight: 2\n", "", 1),
		schedulerTestYAML + "  - name: binpack\n    enableNodeOrder: false\n    arguments:\n      binpack.weight: 0\n      binpack.resources: hygon.com/hcu\n      binpack.resources.hygon.com/hcu: 3\n",
		strings.Replace(schedulerTestYAML, "deviceshare.VGPUEnable: true", "deviceshare.VGPUEnable: true\n      deviceshare.SchedulePolicy: binpack\n      deviceshare.ScheduleWeight: 3", 1),
		strings.Replace(schedulerTestYAML, "deviceshare.VGPUEnable: true", "deviceshare.VGPUEnable: on", 1),
	} {
		projected, err := instanceSchedulerProtectedProjection(document)
		require.NoError(t, err)
		require.Equal(t, protected, projected)
	}
}

func TestInstanceSchedulerNonScoringContentRemainsProtected(t *testing.T) {
	for _, test := range []struct{ current, next string }{
		{schedulerTestYAML, strings.Replace(schedulerTestYAML, "deviceshare.VGPUEnable: true", "deviceshare.VGPUEnable: false", 1)},
		{schedulerTestYAML, strings.Replace(schedulerTestYAML, "enqueue, allocate, backfill", "allocate, enqueue, backfill", 1)},
		{schedulerTestYAML, schedulerTestYAML + "unknown: preserved\n"},
		{schedulerTestYAML, strings.Replace(schedulerTestYAML, "nodeaffinity.weight", "unreviewed.weight", 1)},
		{schedulerTestYAML + "unknown: !custom preserved\n", schedulerTestYAML + "unknown: preserved\n"},
	} {
		current, err := instanceSchedulerProtectedProjection(test.current)
		require.NoError(t, err)
		next, err := instanceSchedulerProtectedProjection(test.next)
		require.NoError(t, err)
		require.NotEqual(t, current, next)
	}
}

func TestInstanceSchedulerYAMLStructureAndScalarSafety(t *testing.T) {
	for _, document := range []string{
		"", "[]", "tiers: [", "{}\n---\n{}\n",
		"actions: first\nactions: second\n",
		"1: value\n", "loop: &loop {next: *loop}\n",
		strings.Replace(schedulerTestYAML, "nodeaffinity.weight: 2", "nodeaffinity.weight: \"2\"", 1),
		strings.Replace(schedulerTestYAML, "nodeaffinity.weight: 2", "nodeaffinity.weight: 18446744073709551616", 1),
		strings.Replace(schedulerTestYAML, "deviceshare.VGPUEnable: true", "deviceshare.VGPUEnable: \"true\"", 1),
		strings.Replace(schedulerTestYAML, "deviceshare.VGPUEnable: true", "deviceshare.VGPUEnable: \"on\"", 1),
	} {
		_, err := instanceSchedulerProtectedProjection(document)
		require.Error(t, err)
	}
	const merged = "defaults: &defaults\n  deviceshare.VGPUEnable: true\nactions: enqueue\ntiers:\n- plugins:\n  - name: deviceshare\n    arguments:\n      <<: *defaults\n"
	const inline = "defaults: &defaults\n  deviceshare.VGPUEnable: true\nactions: enqueue\ntiers:\n- plugins:\n  - name: deviceshare\n    arguments:\n      deviceshare.VGPUEnable: true\n"
	left, err := instanceSchedulerProtectedProjection(merged)
	require.NoError(t, err)
	right, err := instanceSchedulerProtectedProjection(inline)
	require.NoError(t, err)
	require.Equal(t, left, right)
}
