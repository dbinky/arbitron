package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLibStore is an in-memory LibraryStore keyed by scope then rel. errFor
// lets a test force a per-scope read error.
type fakeLibStore struct {
	files   map[workflow.Scope]map[string][]byte
	readErr map[workflow.Scope]error
}

func newFakeLibStore() *fakeLibStore {
	return &fakeLibStore{files: map[workflow.Scope]map[string][]byte{}, readErr: map[workflow.Scope]error{}}
}

func (f *fakeLibStore) bucket(s workflow.Scope) map[string][]byte {
	if f.files[s] == nil {
		f.files[s] = map[string][]byte{}
	}
	return f.files[s]
}

func (f *fakeLibStore) ListFiles(_ context.Context, s workflow.Scope) ([]string, error) {
	out := []string{}
	for k := range f.files[s] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeLibStore) ReadFile(_ context.Context, s workflow.Scope, rel string) ([]byte, error) {
	if err := f.readErr[s]; err != nil {
		return nil, err
	}
	b, ok := f.files[s][rel]
	if !ok {
		return nil, workflow.ErrFileNotFound
	}
	return b, nil
}

func (f *fakeLibStore) WriteFile(_ context.Context, s workflow.Scope, rel string, c []byte) error {
	f.bucket(s)[rel] = c
	return nil
}

func (f *fakeLibStore) DeleteFile(_ context.Context, s workflow.Scope, rel string) error {
	if _, ok := f.files[s][rel]; !ok {
		return workflow.ErrFileNotFound
	}
	delete(f.files[s], rel)
	return nil
}

func libSvc(t *testing.T) (*workflow.Service, *fakeLibStore, *recordingStore) {
	t.Helper()
	st := newFakeLibStore()
	rec := &recordingStore{}
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)
	svc.SetLibraryStore(st)
	svc.SetAudit(audit.NewService(rec))
	return svc, st, rec
}

func TestLibraryService_PrivateWriteThenEffectiveRead(t *testing.T) {
	svc, _, _ := libSvc(t)
	u := uuid.New()
	ctx := ctxWith(u, false)
	require.NoError(t, svc.LibraryWrite(ctx, workflow.Private(u), "a.md", []byte("hi")))
	got, err := svc.LibraryRead(ctx, nil, "a.md")
	require.NoError(t, err)
	assert.Equal(t, "hi", string(got))
}

func TestLibraryService_AdminSharedWriteAudited(t *testing.T) {
	svc, st, rec := libSvc(t)
	ctx := ctxWith(uuid.New(), true)
	require.NoError(t, svc.LibraryWrite(ctx, workflow.Shared(), "a.md", []byte("x")))
	assert.Equal(t, "x", string(st.files[workflow.Shared()]["a.md"]))
	require.Len(t, rec.events, 1)
	assert.Equal(t, audit.ActionLibEditShared, rec.events[0].Action)
	assert.Equal(t, "lib.edit_shared", rec.events[0].Action)
	assert.Equal(t, "@lib", rec.events[0].Target)
	assert.JSONEq(t, `{"path":"a.md","op":"write"}`, rec.events[0].Detail)

	require.NoError(t, svc.LibraryDelete(ctx, workflow.Shared(), "a.md"))
	require.Len(t, rec.events, 2)
	assert.JSONEq(t, `{"path":"a.md","op":"delete"}`, rec.events[1].Detail)
}

func TestLibraryService_ListBothScopes(t *testing.T) {
	svc, st, _ := libSvc(t)
	u := uuid.New()
	st.bucket(workflow.Private(u))["p.md"] = []byte("p")
	st.bucket(workflow.Shared())["s.md"] = []byte("s")
	got, err := svc.LibraryList(ctxWith(u, false))
	require.NoError(t, err)
	assert.Equal(t, []string{"p.md"}, got.Private)
	assert.Equal(t, []string{"s.md"}, got.Shared)
}

func TestLibraryService_EffectivePrefersPrivate(t *testing.T) {
	svc, st, _ := libSvc(t)
	u := uuid.New()
	st.bucket(workflow.Private(u))["a.md"] = []byte("priv")
	st.bucket(workflow.Shared())["a.md"] = []byte("shared")
	got, err := svc.LibraryRead(ctxWith(u, false), nil, "a.md")
	require.NoError(t, err)
	assert.Equal(t, "priv", string(got))
}

func TestLibraryService_EffectiveFallsThroughToSharedOnCleanNotFound(t *testing.T) {
	svc, st, _ := libSvc(t)
	st.bucket(workflow.Shared())["a.md"] = []byte("shared")
	got, err := svc.LibraryRead(ctxWith(uuid.New(), false), nil, "a.md")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(got))
}

func TestLibraryService_EffectiveReadDoesNotFallThroughOnPrivateFault(t *testing.T) {
	for name, fault := range map[string]error{
		"invalid path":  fmt.Errorf("%w: lstat /srv/ws/users/x/_lib/a.md: symlink escapes", workflow.ErrInvalidLibPath),
		"other failure": errors.New("lstat /srv/ws/users/x/_lib: permission denied"),
	} {
		t.Run(name, func(t *testing.T) {
			svc, st, _ := libSvc(t)
			u := uuid.New()
			st.bucket(workflow.Shared())["a.md"] = []byte("shared")
			st.readErr[workflow.Private(u)] = fault
			got, err := svc.LibraryRead(ctxWith(u, false), nil, "a.md")
			require.Error(t, err)
			assert.Nil(t, got)
			assert.NotContains(t, err.Error(), "/srv/ws")
		})
	}
}

func TestLibraryService_EffectiveReadNotFoundEverywhere(t *testing.T) {
	svc, _, _ := libSvc(t)
	_, err := svc.LibraryRead(ctxWith(uuid.New(), false), nil, "a.md")
	require.ErrorIs(t, err, workflow.ErrFileNotFound)
}

func TestLibraryService_ExactSharedIgnoresPrivateShadow(t *testing.T) {
	svc, st, _ := libSvc(t)
	u := uuid.New()
	st.bucket(workflow.Private(u))["a.md"] = []byte("priv")
	st.bucket(workflow.Shared())["a.md"] = []byte("shared")
	sh := workflow.Shared()
	got, err := svc.LibraryRead(ctxWith(u, false), &sh, "a.md")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(got))
}

func TestLibraryService_DeletePrivateReexposesShared(t *testing.T) {
	svc, st, _ := libSvc(t)
	u := uuid.New()
	ctx := ctxWith(u, false)
	st.bucket(workflow.Private(u))["a.md"] = []byte("priv")
	st.bucket(workflow.Shared())["a.md"] = []byte("shared")
	require.NoError(t, svc.LibraryDelete(ctx, workflow.Private(u), "a.md"))
	got, err := svc.LibraryRead(ctx, nil, "a.md")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(got))
}

func TestLibraryService_NonAdminSharedWriteUnauthorized(t *testing.T) {
	svc, st, rec := libSvc(t)
	err := svc.LibraryWrite(ctxWith(uuid.New(), false), workflow.Shared(), "a.md", []byte("x"))
	require.ErrorIs(t, err, workflow.ErrUnauthorized)
	assert.Empty(t, st.files[workflow.Shared()])
	assert.Empty(t, rec.events)
}

func TestLibraryService_NonAdminSharedDeleteUnauthorized(t *testing.T) {
	svc, st, rec := libSvc(t)
	st.bucket(workflow.Shared())["a.md"] = []byte("s")
	err := svc.LibraryDelete(ctxWith(uuid.New(), false), workflow.Shared(), "a.md")
	require.ErrorIs(t, err, workflow.ErrUnauthorized)
	assert.Contains(t, st.files[workflow.Shared()], "a.md")
	assert.Empty(t, rec.events)
}

func TestLibraryService_NoIdentityUnauthenticated(t *testing.T) {
	svc, _, _ := libSvc(t)
	ctx := context.Background()
	_, err := svc.LibraryList(ctx)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = svc.LibraryRead(ctx, nil, "a.md")
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	require.ErrorIs(t, svc.LibraryWrite(ctx, workflow.Shared(), "a.md", nil), identity.ErrUnauthenticated)
	require.ErrorIs(t, svc.LibraryDelete(ctx, workflow.Shared(), "a.md"), identity.ErrUnauthenticated)
}

func TestLibraryService_PrivateScopeOfAnotherUserUnauthorized(t *testing.T) {
	svc, st, _ := libSvc(t)
	other := uuid.New()
	st.bucket(workflow.Private(other))["a.md"] = []byte("o")
	for _, admin := range []bool{false, true} {
		ctx := ctxWith(uuid.New(), admin)
		sc := workflow.Private(other)
		_, err := svc.LibraryRead(ctx, &sc, "a.md")
		require.ErrorIs(t, err, workflow.ErrUnauthorized)
		require.ErrorIs(t, svc.LibraryWrite(ctx, sc, "a.md", []byte("x")), workflow.ErrUnauthorized)
		require.ErrorIs(t, svc.LibraryDelete(ctx, sc, "a.md"), workflow.ErrUnauthorized)
	}
}

func TestLibraryService_NoStoreConfigured(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)
	ctx := ctxWith(uuid.New(), true)
	_, err := svc.LibraryList(ctx)
	require.ErrorIs(t, err, workflow.ErrLibraryUnavailable)
	_, err = svc.LibraryRead(ctx, nil, "a.md")
	require.ErrorIs(t, err, workflow.ErrLibraryUnavailable)
	require.ErrorIs(t, svc.LibraryWrite(ctx, workflow.Shared(), "a.md", nil), workflow.ErrLibraryUnavailable)
	require.ErrorIs(t, svc.LibraryDelete(ctx, workflow.Shared(), "a.md"), workflow.ErrLibraryUnavailable)
}

func TestLibraryService_InvalidRelRejectedBeforeStore(t *testing.T) {
	svc, st, rec := libSvc(t)
	ctx := ctxWith(uuid.New(), true)
	err := svc.LibraryWrite(ctx, workflow.Shared(), "../x", []byte("x"))
	require.ErrorIs(t, err, workflow.ErrInvalidLibPath)
	assert.Empty(t, st.files)
	assert.Empty(t, rec.events)
}

// errLibStore fails every call with a raw error naming an absolute path.
type errLibStore struct{ err error }

func (e errLibStore) ListFiles(context.Context, workflow.Scope) ([]string, error) { return nil, e.err }
func (e errLibStore) ReadFile(context.Context, workflow.Scope, string) ([]byte, error) {
	return nil, e.err
}
func (e errLibStore) WriteFile(context.Context, workflow.Scope, string, []byte) error { return e.err }
func (e errLibStore) DeleteFile(context.Context, workflow.Scope, string) error        { return e.err }

func TestLibraryService_ErrorsKeepSentinelButHideAbsolutePaths(t *testing.T) {
	const abs = "/srv/arbitron/ws/users/abc/_lib/a.md"
	cases := map[string]struct {
		raw      error
		sentinel error
	}{
		"invalid": {fmt.Errorf("%w: lstat %s: symlink", workflow.ErrInvalidLibPath, abs), workflow.ErrInvalidLibPath},
		"missing": {fmt.Errorf("%w: %s", workflow.ErrFileNotFound, abs), workflow.ErrFileNotFound},
		"other":   {errors.New("open " + abs + ": permission denied"), nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			svc := workflow.NewWorkflowService(newFakeRepo(), nil)
			svc.SetLibraryStore(errLibStore{err: c.raw})
			u := uuid.New()
			ctx := ctxWith(u, true)
			sc := workflow.Private(u)
			sh := workflow.Shared()
			_, e1 := svc.LibraryList(ctx)
			_, e2 := svc.LibraryRead(ctx, &sc, "a.md")
			_, e3 := svc.LibraryRead(ctx, &sh, "a.md")
			e4 := svc.LibraryWrite(ctx, sc, "a.md", nil)
			e5 := svc.LibraryDelete(ctx, sc, "a.md")
			for _, err := range []error{e1, e2, e3, e4, e5} {
				require.Error(t, err)
				assert.False(t, strings.Contains(err.Error(), "/srv/"), err.Error())
				if c.sentinel != nil {
					assert.ErrorIs(t, err, c.sentinel)
				}
			}
		})
	}
}

// blockingGate would hang forever if a library method entered it.
type blockingGate struct{ entered bool }

func (g *blockingGate) WithMutation(ctx context.Context, _ func(context.Context) error) error {
	g.entered = true
	<-ctx.Done()
	return ctx.Err()
}

func TestLibraryService_DoesNotTakeExecutionGate(t *testing.T) {
	svc, _, _ := libSvc(t)
	g := &blockingGate{}
	svc.SetMutationGate(g)
	u := uuid.New()
	ctx := ctxWith(u, true)
	require.NoError(t, svc.LibraryWrite(ctx, workflow.Private(u), "a.md", []byte("x")))
	require.NoError(t, svc.LibraryWrite(ctx, workflow.Shared(), "a.md", []byte("x")))
	require.NoError(t, svc.LibraryDelete(ctx, workflow.Shared(), "a.md"))
	assert.False(t, g.entered)
}
