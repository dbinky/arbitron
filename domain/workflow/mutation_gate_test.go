package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingGate counts leases and can refuse them, standing in for the shared
// ExecutionGate that the root injects.
type recordingGate struct {
	leases int
	err    error
}

func (g *recordingGate) WithMutation(ctx context.Context, fn func(context.Context) error) error {
	if g.err != nil {
		return g.err
	}
	g.leases++
	return fn(ctx)
}

// TestService_ExecutionAffectingWritesRunUnderMutationGate drives every write
// that can change which model a workflow resolves to and asserts each takes the
// exclusive lease exactly once.
func TestService_ExecutionAffectingWritesRunUnderMutationGate(t *testing.T) {
	owner := uuid.New()
	svc, _, _, ctx, _ := renameSvc(t, owner)
	svc.SetActiveInstanceCounter(newFakeActiveCounter())
	gate := &recordingGate{}
	svc.SetMutationGate(gate)

	ops := []struct {
		name string
		do   func() error
	}{
		{"create", func() error { return svc.Create(ctx, "beta", wf("beta")) }},
		{"update", func() error { return svc.Update(ctx, "alpha", wf("alpha")) }},
		{"write file", func() error { return svc.WriteFile(ctx, "alpha", "prompts/a.md", []byte("x")) }},
		{"delete file", func() error { return svc.DeleteFile(ctx, "alpha", "prompts/a.md") }},
		{"rename", func() error { return svc.Rename(ctx, "beta", "gamma") }},
		{"duplicate", func() error { return svc.Duplicate(ctx, "alpha", "alpha-copy") }},
		{"transfer", func() error { return svc.TransferOwnership(ctx, "alpha-copy", uuid.New()) }},
		{"delete", func() error { return svc.Delete(ctx, "gamma") }},
	}
	for _, op := range ops {
		before := gate.leases
		require.NoError(t, op.do(), op.name)
		assert.Equal(t, before+1, gate.leases, "%s must take exactly one mutation lease", op.name)
	}
}

// TestService_WriteRefusedWhenGateRefuses proves a gate failure aborts the write
// before any repository call (the workflow is not created).
func TestService_WriteRefusedWhenGateRefuses(t *testing.T) {
	owner := uuid.New()
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(newFakeRegistry())
	gateErr := errors.New("gate closed")
	svc.SetMutationGate(&recordingGate{err: gateErr})

	err := svc.Create(ctxWith(owner, true), "alpha", wf("alpha"))
	require.ErrorIs(t, err, gateErr)
	_, ok := repo.bucket(workflow.Private(owner))["alpha"]
	assert.False(t, ok, "a refused lease must not reach the repository")
}

// TestService_PublicationAndArchiveWritesRunUnderMutationGate proves the
// writes that add or remove a runnable workflow scope (publish, unpublish, copy)
// or change triggerability (archive, unarchive) take the lease. A refusing gate
// must win before any collaborator is consulted, so no publisher is configured.
func TestService_PublicationAndArchiveWritesRunUnderMutationGate(t *testing.T) {
	owner := uuid.New()
	svc, _, _, ctx, _ := renameSvc(t, owner)
	gateErr := errors.New("gate closed")
	svc.SetMutationGate(&recordingGate{err: gateErr})

	ops := map[string]func() error{
		"publish":   func() error { return svc.Publish(ctx, "alpha", "alpha", workflow.VisibilityPublic, "") },
		"unpublish": func() error { return svc.Unpublish(ctx, "alpha") },
		"copy":      func() error { return svc.Copy(ctx, "alpha", "alpha-private") },
		"archive":   func() error { return svc.Archive(ctx, "alpha") },
		"unarchive": func() error { return svc.Unarchive(ctx, "alpha") },
	}
	for name, do := range ops {
		require.ErrorIs(t, do(), gateErr, "%s must be refused by the gate before any other work", name)
	}

	gate := &recordingGate{}
	svc.SetMutationGate(gate)
	require.NoError(t, svc.Archive(ctx, "alpha"))
	require.NoError(t, svc.Unarchive(ctx, "alpha"))
	assert.Equal(t, 2, gate.leases, "archive and unarchive each take exactly one lease")
}

// SECURITY — admin-only writes authorize BEFORE taking the exclusive execution
// lease, so a forbidden caller never queues behind (or blocks) run starts and
// model refreshes.
func TestService_AdminOnlyWritesRefuseNonAdminBeforeLease(t *testing.T) {
	owner := uuid.New()
	svc, _, _, _, _ := renameSvc(t, owner)
	svc.SetActiveInstanceCounter(newFakeActiveCounter())
	gate := &recordingGate{}
	svc.SetMutationGate(gate)
	user := ctxWith(owner, false)

	ops := map[string]func() error{
		"rename":    func() error { return svc.Rename(user, "alpha", "gamma") },
		"duplicate": func() error { return svc.Duplicate(user, "alpha", "alpha-copy") },
		"transfer":  func() error { return svc.TransferOwnership(user, "alpha", uuid.New()) },
		"delete":    func() error { return svc.Delete(user, "alpha") },
		"unpublish": func() error { return svc.Unpublish(user, "alpha") },
		"archive":   func() error { return svc.Archive(user, "alpha") },
		"unarchive": func() error { return svc.Unarchive(user, "alpha") },
	}
	for name, do := range ops {
		require.ErrorIs(t, do(), workflow.ErrForbidden, name)
	}
	assert.Zero(t, gate.leases, "no forbidden admin call may acquire the mutation lease")
}
