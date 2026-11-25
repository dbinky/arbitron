package runneragent_test

import (
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
)

const scopeRun = "11111111-1111-1111-1111-111111111111"

func scopedJob() runneragent.WorkspaceJob {
	hash := strings.Repeat("b", 64)
	return runneragent.WorkspaceJob{RunID: scopeRun, WorkDir: "/srv/ws/instances/1", HomeGeneration: 3, CheckpointVersion: 7,
		ManifestHash: hash, BaseRef: scopeRun + "@7:" + hash, FenceKey: "run:" + scopeRun, FenceOwner: "mac-1", FenceGeneration: 2,
		FenceToken: "22222222-2222-4222-8222-222222222222", PullToken: "p", PushToken: "u", PartBytes: 64 << 20}
}

func TestWorkspaceJob_CompleteScopeValidates(t *testing.T) {
	j := scopedJob()
	assert.NoError(t, j.Validate())
}

// TestWorkspaceExecutor_MissingResourceClaimRejectedBeforeAccept's domain half:
// a job without its claim or pinned base is refused before any accept, pull or
// workspace mutation; nothing is defaulted.
func TestWorkspaceJob_IncompleteScopeIsRejected(t *testing.T) {
	cases := map[string]func(*runneragent.WorkspaceJob){
		"no base":            func(j *runneragent.WorkspaceJob) { j.BaseRef = "" },
		"base version drift": func(j *runneragent.WorkspaceJob) { j.CheckpointVersion = 8 },
		"base of other run":  func(j *runneragent.WorkspaceJob) { j.BaseRef = strings.Replace(j.BaseRef, "1111", "3333", 1) },
		"no claim token":     func(j *runneragent.WorkspaceJob) { j.FenceToken = "" },
		"zero claim token":   func(j *runneragent.WorkspaceJob) { j.FenceToken = "00000000-0000-0000-0000-000000000000" },
		"claim of other run": func(j *runneragent.WorkspaceJob) { j.FenceKey = "run:33333333-3333-3333-3333-333333333333" },
		"no claim owner":     func(j *runneragent.WorkspaceJob) { j.FenceOwner = "" },
		"zero generation":    func(j *runneragent.WorkspaceJob) { j.FenceGeneration = 0 },
		"bad run id":         func(j *runneragent.WorkspaceJob) { j.RunID = "nope" },
		"relative work dir":  func(j *runneragent.WorkspaceJob) { j.WorkDir = "ws" },
		"no push token":      func(j *runneragent.WorkspaceJob) { j.PushToken = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			j := scopedJob()
			mutate(&j)
			assert.Error(t, j.Validate())
		})
	}
}
