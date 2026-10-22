package filesystem_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/stretchr/testify/require"
)

func TestSampleSourceReadErrorClassification(t *testing.T) {
	root := t.TempDir()
	writeSample(t, root, "broken", `{"name":`, nil)
	writeSample(t, root, "mismatched", `{"name":"elsewhere"}`, nil)
	source := filesystem.NewSampleSource(root)
	for _, name := range []string{"broken", "mismatched"} {
		_, _, err := source.Read(context.Background(), name)
		require.ErrorIs(t, err, samples.ErrMalformedSample)
	}
	_, _, err := source.Read(context.Background(), "missing")
	require.Error(t, err)
	require.False(t, errors.Is(err, samples.ErrMalformedSample))
}
