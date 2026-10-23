package main

import (
	"bytes"
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestModelDevReleaseProductCommandsAreRejected(t *testing.T) {
	for _, name := range []string{"modeldev-pause", "modeldev-enable", "modeldev-import-release"} {
		var output bytes.Buffer
		err := runAdmin(context.Background(), []string{name}, &output)
		require.Error(t, err)
		require.Empty(t, output.String())
	}
}
