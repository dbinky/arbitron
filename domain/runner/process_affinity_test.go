package runner

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessAffinity(t *testing.T) {
	bundle := &engine.ScriptBundleRef{Hash: "h", Entrypoint: "r.sh"}
	for name, tc := range map[string]struct {
		spec engine.ProcessSpec
		want Affinity
	}{
		"portable bundled script":   {engine.ProcessSpec{Kind: engine.ProcessScript, Portable: true, WorkDir: "/w", Bundle: bundle}, AffinityWorkspace},
		"unflagged script":          {engine.ProcessSpec{Kind: engine.ProcessScript, WorkDir: "/w", Bundle: bundle}, AffinityBuiltIn},
		"portable unbundled script": {engine.ProcessSpec{Kind: engine.ProcessScript, Portable: true, WorkDir: "/w"}, AffinityBuiltIn},
		"script without workspace":  {engine.ProcessSpec{Kind: engine.ProcessScript, Portable: true, Bundle: bundle}, AffinityBuiltIn},
		"portable tool":             {engine.ProcessSpec{Kind: engine.ProcessToolCall, Portable: true, WorkDir: "/w"}, AffinityWorkspace},
		"unflagged tool":            {engine.ProcessSpec{Kind: engine.ProcessToolCall, WorkDir: "/w"}, AffinityBuiltIn},
		"tool without workspace":    {engine.ProcessSpec{Kind: engine.ProcessToolCall, Portable: true}, AffinityBuiltIn},
		"unknown kind":              {engine.ProcessSpec{Portable: true, WorkDir: "/w", Bundle: bundle}, AffinityBuiltIn},
	} {
		t.Run(name, func(t *testing.T) { assert.Equal(t, tc.want, ProcessAffinity(tc.spec)) })
	}
}

func TestRoutedProcessRunner_PortableAffinityReachesDispatch(t *testing.T) {
	for _, kind := range []engine.ProcessKind{engine.ProcessScript, engine.ProcessToolCall} {
		t.Run(string(kind), func(t *testing.T) {
			d := &recordingDispatcher{out: map[OpType]StepOutput{OpProcess: {Process: &engine.ProcessResult{Stdout: "executed"}}}}
			spec := engine.ProcessSpec{Kind: kind, Portable: true, WorkDir: "/workspace", Command: "./run.sh", Requires: []string{"jq"}}
			if kind == engine.ProcessScript {
				spec.Bundle = &engine.ScriptBundleRef{Hash: "hash", Entrypoint: "run.sh"}
			}
			got, err := newRoutedProcessRunner(d).RunProcess(stepCtx(uuid.New()), spec)
			require.NoError(t, err)
			assert.Equal(t, "executed", got.Stdout)
			require.Len(t, d.reqs, 1)
			assert.Equal(t, AffinityWorkspace, d.reqs[0].Affinity)
			require.Len(t, d.jobs, 1)
			require.NotNil(t, d.jobs[0].Process)
			assert.Equal(t, spec, *d.jobs[0].Process)
		})
	}
}
