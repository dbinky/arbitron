package runnerproto_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scopedRun = "11111111-1111-1111-1111-111111111111"

func scopedWorkspace() runnerproto.WireWorkspace {
	hash := strings.Repeat("b", 64)
	return runnerproto.WireWorkspace{RunID: scopedRun, WorkDir: "/srv/ws/instances/1",
		HomeGeneration: 3, CheckpointVersion: 7, ManifestHash: hash, BaseRef: scopedRun + "@7:" + hash,
		FenceKey: "run:" + scopedRun, FenceOwner: "mac-1", FenceGeneration: 2, FenceToken: "22222222-2222-4222-8222-222222222222",
		PullToken: "tok-pull", PushToken: "tok-push", PartBytes: 64 << 20}
}

func TestWireWorkspace_ScopeRoundTrips(t *testing.T) {
	w := scopedWorkspace()
	raw, err := json.Marshal(runnerproto.Dispatch{Op: "process", Workspace: &w})
	require.NoError(t, err)
	for _, k := range []string{`"baseRef":"`, `"fenceKey":"run:`, `"fenceOwner":"mac-1"`, `"fenceGeneration":2`, `"fenceToken":"`} {
		assert.Contains(t, string(raw), k)
	}
	var back runnerproto.Dispatch
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, &w, back.Workspace)
	assert.NoError(t, back.Workspace.Validate())
}

func TestWireWorkspace_IncompleteScopeIsRejected(t *testing.T) {
	cases := map[string]func(*runnerproto.WireWorkspace){
		"no base":             func(w *runnerproto.WireWorkspace) { w.BaseRef = "" },
		"base of another run": func(w *runnerproto.WireWorkspace) { w.BaseRef = strings.Replace(w.BaseRef, "1111", "3333", 1) },
		"base version drift":  func(w *runnerproto.WireWorkspace) { w.CheckpointVersion = 8 },
		"base hash drift":     func(w *runnerproto.WireWorkspace) { w.ManifestHash = strings.Repeat("c", 64) },
		"no fence token":      func(w *runnerproto.WireWorkspace) { w.FenceToken = "" },
		"zero fence token":    func(w *runnerproto.WireWorkspace) { w.FenceToken = "00000000-0000-0000-0000-000000000000" },
		"fence of other run":  func(w *runnerproto.WireWorkspace) { w.FenceKey = "run:33333333-3333-3333-3333-333333333333" },
		"no fence owner":      func(w *runnerproto.WireWorkspace) { w.FenceOwner = "" },
		"zero generation":     func(w *runnerproto.WireWorkspace) { w.FenceGeneration = 0 },
		"bad run id":          func(w *runnerproto.WireWorkspace) { w.RunID = "nope" },
		"relative work dir":   func(w *runnerproto.WireWorkspace) { w.WorkDir = "ws/1" },
		"no part size":        func(w *runnerproto.WireWorkspace) { w.PartBytes = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := scopedWorkspace()
			mutate(&w)
			assert.Error(t, w.Validate(), "incomplete evidence is refused, never defaulted")
		})
	}
}
