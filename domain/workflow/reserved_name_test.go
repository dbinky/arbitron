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

// reservedCallRepo counts every mutating call that reaches the repository.
type reservedCallRepo struct {
	*fakeRepo
	writes int
}

func (r *reservedCallRepo) Create(ctx context.Context, s workflow.Scope, n string, w *workflow.Workflow) error {
	r.writes++
	return r.fakeRepo.Create(ctx, s, n, w)
}

func (r *reservedCallRepo) Update(ctx context.Context, s workflow.Scope, n string, w *workflow.Workflow) error {
	r.writes++
	return r.fakeRepo.Update(ctx, s, n, w)
}

func (r *reservedCallRepo) CopyTree(ctx context.Context, src workflow.Scope, sn string, dst workflow.Scope, dn string, overwrite bool) error {
	r.writes++
	return r.fakeRepo.CopyTree(ctx, src, sn, dst, dn, overwrite)
}

// --- Error/failure ---

func TestService_ReservedNameRejected(t *testing.T) {
	owner := uuid.New()
	cases := []struct {
		name string
		call func(svc *workflow.Service, ctx context.Context) error
	}{
		{"create _lib", func(s *workflow.Service, c context.Context) error { return s.Create(c, "_lib", wf("_lib")) }},
		{"create _Lib", func(s *workflow.Service, c context.Context) error { return s.Create(c, "_Lib", wf("_Lib")) }},
		{"create @lib", func(s *workflow.Service, c context.Context) error { return s.Create(c, "@lib", wf("@lib")) }},
		{"rename", func(s *workflow.Service, c context.Context) error { return s.Rename(c, "alpha", "_lib") }},
		{"publish", func(s *workflow.Service, c context.Context) error {
			return s.Publish(c, "alpha", "_lib", workflow.VisibilityPublic, "")
		}},
		{"copy", func(s *workflow.Service, c context.Context) error { return s.Copy(c, "alpha", "_lib") }},
		{"duplicate", func(s *workflow.Service, c context.Context) error { return s.Duplicate(c, "alpha", "_lib") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &reservedCallRepo{fakeRepo: newFakeRepo()}
			svc := workflow.NewWorkflowService(repo, newFakePublishers())
			svc.SetRegistry(newFakeRegistry())
			ctx := ctxWith(owner, true)
			require.NoError(t, svc.Create(ctx, "alpha", wf("alpha")))
			before := repo.writes

			err := tc.call(svc, ctx)

			require.Error(t, err)
			assert.True(t, errors.Is(err, workflow.ErrInvalidName), "got %v", err)
			assert.Contains(t, err.Error(), "use @lib")
			assert.Equal(t, before, repo.writes, "the repository must never be written")
		})
	}
}

func TestRevisionStamper_ReservedDestinationRejected(t *testing.T) {
	for _, name := range []string{"_lib", "_LIB", "@lib"} {
		t.Run(name, func(t *testing.T) {
			s, j, r, _, _ := stamperFixture(t)
			_, err := s.Stamp(context.Background(), workflow.Shared(), name, stampWorkflow(), nil, workflow.SaveReasonCreate)
			require.Error(t, err)
			assert.True(t, errors.Is(err, workflow.ErrInvalidName))
			assert.Empty(t, j.entries)
			assert.Empty(t, r.requests)
		})
	}
}
