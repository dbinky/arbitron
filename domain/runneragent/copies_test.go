package runneragent_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestReuseLocalCopy(t *testing.T) {
	run := uuid.New()
	base := runneragent.CopyState{RunID: run, HomeGeneration: 3, CheckpointVersion: 9, ManifestHash: "m9"}
	tgt := runneragent.PullTarget{RunID: run, HomeGeneration: 3, CheckpointVersion: 9, ManifestHash: "m9"}
	assert.True(t, runneragent.ReuseLocalCopy(base, tgt))

	dirty := base
	dirty.Dirty = true
	assert.False(t, runneragent.ReuseLocalCopy(dirty, tgt), "an interrupted attempt dirtied the copy")
	otherGen := tgt
	otherGen.HomeGeneration = 4
	assert.False(t, runneragent.ReuseLocalCopy(base, otherGen), "home generation must match")
	otherVer := tgt
	otherVer.CheckpointVersion = 10
	assert.False(t, runneragent.ReuseLocalCopy(base, otherVer), "checkpoint version must match")
	otherHash := tgt
	otherHash.ManifestHash = "x"
	assert.False(t, runneragent.ReuseLocalCopy(base, otherHash))
	otherRun := tgt
	otherRun.RunID = uuid.New()
	assert.False(t, runneragent.ReuseLocalCopy(base, otherRun))
}

func TestDispatchCarriesWorkspaceJobs(t *testing.T) {
	d := runneragent.Dispatch{Op: runneragent.OpProcess,
		Process:   &runneragent.ProcessJob{Kind: "tool_call", Command: "sh"},
		Workspace: &runneragent.WorkspaceJob{RunID: uuid.NewString(), HomeGeneration: 1}}
	r := runneragent.Result{OK: true, Process: &runneragent.ProcessResult{ExitCode: 0}, Agent: &runneragent.AgentResult{Summary: "s"}}
	assert.Equal(t, "sh", d.Process.Command)
	assert.Equal(t, int64(1), d.Workspace.HomeGeneration)
	assert.Equal(t, "s", r.Agent.Summary)
	assert.Equal(t, "agent", runneragent.OpAgent)
}
