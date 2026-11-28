package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runneragent"
)

type rdhHomeItem struct {
	RunID      *string `json:"runId"`
	Hidden     bool    `json:"hidden"`
	Generation int64   `json:"generation"`
	Dirty      bool    `json:"dirty"`
}

type rdhDetail struct {
	HomedWorkspaces int           `json:"homedWorkspaces"`
	Homes           []rdhHomeItem `json:"homes"`
}

func rdhDecode(t *testing.T, raw []byte) rdhDetail {
	t.Helper()
	var d rdhDetail
	require.NoError(t, json.Unmarshal(raw, &d), string(raw))
	return d
}

// A workspace step on a remote home makes that run appear in the runner's
// detail (REST and MCP) with its home generation; a non-owner, non-admin
// user sees the home but not the run. Without `Homes:` in the newRunnerStack
// service literal the list stays [].
func TestRunnerDetailCompositionRoot_HomedWorkspaceListedAndHiddenFromNonViewers(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{"p3-hold": {manifest: p3HoldManifest, files: p3Files(nil)}}, "")
	mac1 := startWorkspaceRunner(t, st, "mac-1", runneragent.OpAgent)
	admin := adminCtxForLocalUser(t, st.ds)
	id, _, err := st.ds.eng.Trigger(admin, "p3-hold", map[string]any{}, "normal")
	require.NoError(t, err)
	// Finish the held run before startWorkspaceRunner stops its agent. Cleanup
	// runs in reverse order; the stack's gate cleanup otherwise runs too late.
	t.Cleanup(func() {
		st.release(t, "hold")
		p3RequireCompleted(t, st.ds, id, 30*time.Second)
	})
	st.awaitFile(t, "hold.started", 30*time.Second)
	require.Eventually(t, func() bool { o, ok := p3HomeOwner(st.ds, id); return ok && o == mac1.id },
		20*time.Second, 50*time.Millisecond, "the held run is homed on mac-1")
	var gen int64
	var dirty int
	require.NoError(t, st.ds.db.QueryRow(`SELECT generation, dirty FROM workspace_homes WHERE run_id = ?`, id.String()).Scan(&gen, &dirty))

	// Admin (local mode) over the served router.
	code, raw := rrDo(t, http.MethodGet, st.srv.URL+"/api/v1/runners/"+mac1.id, nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	d := rdhDecode(t, raw)
	require.Len(t, d.Homes, 1, "SINK: GET /api/v1/runners/{mac-1} lists the run homed there")
	require.NotNil(t, d.Homes[0].RunID)
	assert.Equal(t, id.String(), *d.Homes[0].RunID)
	assert.False(t, d.Homes[0].Hidden)
	assert.Equal(t, gen, d.Homes[0].Generation, "SINK: the home's ownership generation")
	assert.Equal(t, dirty != 0, d.Homes[0].Dirty)
	assert.Equal(t, 1, d.HomedWorkspaces, "the count and the list agree")

	code, raw = rrDo(t, http.MethodGet, st.srv.URL+"/api/v1/runners/builtin", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	for _, h := range rdhDecode(t, raw).Homes {
		if h.RunID != nil {
			assert.NotEqual(t, id.String(), *h.RunID, "a run homed on mac-1 is not listed on the built-in runner")
		}
	}

	isErr, text := mcpToolCallerAs(t, st.ds, admin)("runner_get", map[string]any{"id": mac1.id})
	require.False(t, isErr, text)
	m := rdhDecode(t, []byte(text))
	require.Len(t, m.Homes, 1, "SINK: MCP runner_get lists the same home")
	require.NotNil(t, m.Homes[0].RunID)
	assert.Equal(t, id.String(), *m.Homes[0].RunID)
	assert.Equal(t, d, m, "REST and MCP expose the same home metadata")

	// A non-owner, non-admin user: the home is listed, its run is hidden.
	rdPrivate(t, st.ds, id)
	peer := newRDPeer(t, st.ds, "rd-peer")
	code, raw = peer.get(t, "/api/v1/runners/"+mac1.id)
	require.Equal(t, http.StatusOK, code, string(raw))
	p := rdhDecode(t, raw)
	require.Len(t, p.Homes, 1, "the peer still sees that mac-1 holds a workspace")
	assert.Nil(t, p.Homes[0].RunID, "SINK: the run id is hidden from a non-viewer")
	assert.True(t, p.Homes[0].Hidden)
	assert.Equal(t, gen, p.Homes[0].Generation)
	assert.NotContains(t, string(raw), id.String(), "no part of the detail leaks the hidden run")

	isErr, text = mcpToolCallerAs(t, st.ds, peer.ctx)("runner_get", map[string]any{"id": mac1.id})
	require.False(t, isErr, text)
	pm := rdhDecode(t, []byte(text))
	require.Len(t, pm.Homes, 1)
	assert.True(t, pm.Homes[0].Hidden)
	assert.Nil(t, pm.Homes[0].RunID)
	assert.NotContains(t, text, id.String(), "MCP hides the run the same way")
	assert.Equal(t, p, pm, "REST and MCP redact the same home metadata")

	// Edge: visibility is decided per run, not by blanket hiding: once the run
	// is public the same peer sees it.
	_, err = st.ds.db.Exec(`UPDATE instances SET visibility = 'public' WHERE id = ?`, id.String())
	require.NoError(t, err)
	code, raw = peer.get(t, "/api/v1/runners/"+mac1.id)
	require.Equal(t, http.StatusOK, code, string(raw))
	pub := rdhDecode(t, raw)
	require.Len(t, pub.Homes, 1)
	require.NotNil(t, pub.Homes[0].RunID)
	assert.Equal(t, id.String(), *pub.Homes[0].RunID)
	assert.False(t, pub.Homes[0].Hidden)
}
