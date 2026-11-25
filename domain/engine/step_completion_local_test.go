package engine_test

import (
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestStepCompletionSink_LocalRefOnlyWithNonPortable(t *testing.T) {
	run := uuid.New()
	res := engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: "builtin", Generation: 1, Token: uuid.New()}
	job := engine.JobFenceRef{JobID: uuid.New(), RunnerID: "builtin", Generation: 1}
	local := "local:" + run.String() + "@1:" + strings.Repeat("a", 64)
	catalog := run.String() + "@2:" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name string
		w    engine.WorkspaceCompletion
		ok   bool
	}{
		{"non-portable with its local checkpoint", engine.WorkspaceCompletion{NonPortable: true, LocalRef: local, Resource: res}, true},
		{"non-portable without a local checkpoint is not done", engine.WorkspaceCompletion{NonPortable: true, Resource: res}, false},
		{"portable receipt cannot carry a local checkpoint", engine.WorkspaceCompletion{Ref: catalog, LocalRef: local, Resource: res}, false},
		{"non-portable receipt cannot carry a catalog ref", engine.WorkspaceCompletion{NonPortable: true, Ref: catalog, LocalRef: local, Resource: res}, false},
		{"portable receipt unchanged", engine.WorkspaceCompletion{Ref: catalog, Resource: res}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.w
			err := engine.NewStepCompletionSink().Record(engine.StepCompletion{JobFence: job, Workspace: &w})
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, engine.ErrInvalidStepCompletion)
		})
	}
}
