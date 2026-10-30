package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
)

func TestSampleSeedLib_RealFilesystem(t *testing.T) {
	samplesDir, err := filepath.Abs(filepath.Join("..", "..", "samples"))
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Join(samplesDir, "_lib", "README.md"))
	require.NoError(t, err)

	ws := t.TempDir()
	repo := filesystem.NewWorkflowRepo(ws)
	lib := filesystem.NewLibraryStore(ws)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	require.NoError(t, seedSampleWorkflows(context.Background(), samplesDir, ws, repo, &recordingPublished{t: t}, nil, nil, nil, lib, nil, owner, logger))

	got, err := os.ReadFile(filepath.Join(ws, "shared", "_lib", "README.md"))
	require.NoError(t, err)
	require.Equal(t, want, got)
}
