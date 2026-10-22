package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

// SEAM (paired row 5): the shipped renderer launches Chromium inside the stdio
// MCP process, so the browser and its helpers must never reach protocol stdout
// or consume protocol stdin. The no-browser stdio tests cannot see that. Every
// stdout line is decoded as JSON-RPC by startLayoutStdio/finish through EOF;
// a browser holding the inherited stdout would also stop that EOF.
func TestLayoutStdioProtocol_RealBrowserKeepsStdoutJSONRPC(t *testing.T) {
	browser := requireBrowserE2E(t)
	binary := buildLayoutStdioBinary(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	useLayoutE2EBrowser(t, browser) // the child inherits the production loader's env keys
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(e.root, "layout-artifacts"))
	manifest, err := os.ReadFile(filepath.Join("..", "..", "samples", "pr-review", "workflow.json"))
	require.NoError(t, err)
	p := startLayoutStdioWithin(t, binary, e, 20*time.Minute, "mcp")
	p.initialize(t)
	created := p.call(t, 1, "workflow_create", map[string]any{"manifest": string(manifest)})
	require.False(t, created.IsError, mcpE2EText(t, created))
	saved := layoutE2EManifest(t, e.privatePath("pr-review"))
	db, err := adaptsqlite.Open(e.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	store := adaptsqlite.NewLayoutRepo(db)
	var row layout.Record
	require.Eventually(t, func() bool {
		got, err := store.Get(context.Background(), saved.SavedRevision())
		row = got
		return err == nil && got.State.Terminal()
	}, 15*time.Minute, 250*time.Millisecond)
	require.Equal(t, layout.StatePassed, row.State, "failure stage %q reason %q", row.FailureStage, row.FailureReason)
	res, err := store.Result(context.Background(), saved.SavedRevision(), row.ResultGeneration)
	require.NoError(t, err)
	require.NotNil(t, res.Review)
	require.True(t, layout.HasRenderedEvidence(res.Geometry, res.Review), "the browser must actually have rendered the candidate")
	// The browser is still running: the stream must keep serving framed responses.
	got := p.call(t, 2, "workflow_get", map[string]any{"name": "pr-review"})
	require.False(t, got.IsError, mcpE2EText(t, got))
	var wire struct {
		Arrangement layout.View `json:"arrangement"`
	}
	require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, got)), &wire))
	require.Equal(t, layout.DisplayComplete, wire.Arrangement.DisplayStatus)
	stderr := p.finish(t, true)
	require.Contains(t, stderr, "layout renderer started headless browser", "the lazy browser launch must have happened in this process")
}
