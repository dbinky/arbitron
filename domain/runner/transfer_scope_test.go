package runner_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestTransferToken_ScopeIsExactlyWhatWasIssued(t *testing.T) {
	run, job := uuid.New(), uuid.New()
	res := engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: "mac-1", Generation: 3, Token: uuid.New()}
	tok := runner.TransferToken{Token: "art_x", JobID: job, Generation: 4, RunnerID: "mac-1", RunID: run, Direction: runner.TransferPull,
		BaseRef: run.String() + "@2:ab", Resource: res, BundleHash: "", ExpiresAt: time.Unix(10, 0)}
	assert.Equal(t, runner.TransferScope{Fence: runner.Fence{JobID: job, RunnerID: "mac-1", Generation: 4}, RunID: run,
		Direction: runner.TransferPull, BaseRef: run.String() + "@2:ab", Resource: res}, tok.Scope())
	bundle := tok
	bundle.Direction, bundle.BundleHash = runner.TransferBundle, "sha256:bundle"
	wantBundle := tok.Scope()
	wantBundle.Direction, wantBundle.BundleHash = runner.TransferBundle, "sha256:bundle"
	assert.Equal(t, wantBundle, bundle.Scope(), "bundle authority must not be lost")

	maint := runner.TransferToken{Token: "art_y", MaintenanceOperationID: "drain-op-1", RunnerID: "mac-2", RunID: run,
		Direction: runner.TransferPull, BaseRef: "b", Resource: res}
	assert.Equal(t, runner.TransferScope{Fence: runner.Fence{RunnerID: "mac-2"}, MaintenanceOperationID: "drain-op-1", RunID: run,
		Direction: runner.TransferPull, BaseRef: "b", Resource: res}, maint.Scope())
}

func TestTransferDirections_MatchTheSessionVocabulary(t *testing.T) {
	assert.Equal(t, []string{"pull", "push", "bundle"}, []string{runner.TransferPull, runner.TransferPush, runner.TransferBundle})
	assert.Equal(t, 15*time.Minute, runner.DefaultTransferTokenTTL)
}
