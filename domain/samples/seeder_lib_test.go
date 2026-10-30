package samples_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
)

// fakeLibStore is an in-memory LibraryStore (shared scope only) that logs
// writes into an optional shared call-order log.
type fakeLibStore struct {
	files    map[string][]byte
	writeErr error
	log      *[]string
	writes   int
}

func newFakeLibStore() *fakeLibStore { return &fakeLibStore{files: map[string][]byte{}} }

func (l *fakeLibStore) ListFiles(context.Context, workflow.Scope) ([]string, error) {
	return nil, nil
}
func (l *fakeLibStore) ReadFile(_ context.Context, s workflow.Scope, rel string) ([]byte, error) {
	if s != workflow.Shared() {
		return nil, errors.New("unexpected scope")
	}
	b, ok := l.files[rel]
	if !ok {
		return nil, workflow.ErrFileNotFound
	}
	return b, nil
}
func (l *fakeLibStore) WriteFile(_ context.Context, s workflow.Scope, rel string, c []byte) error {
	if l.writeErr != nil {
		return l.writeErr
	}
	if s != workflow.Shared() {
		return errors.New("unexpected scope")
	}
	l.files[rel] = c
	l.writes++
	if l.log != nil {
		*l.log = append(*l.log, "lib:"+rel)
	}
	return nil
}
func (l *fakeLibStore) DeleteFile(_ context.Context, _ workflow.Scope, rel string) error {
	delete(l.files, rel)
	return nil
}

// orderRepo logs workflow creates into the shared call-order log.
type orderRepo struct {
	*fakeRepo
	log *[]string
}

func (r orderRepo) Create(ctx context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	*r.log = append(*r.log, "create:"+name)
	return r.fakeRepo.Create(ctx, s, name, wf)
}

func libSeeder(src *fakeSource, store *fakeLibStore, led *fakeLedger) *samples.Seeder {
	s := samples.NewSeeder(src, &fakeRepo{}, &fakePublished{}, led, uuid.New(), discardLogger())
	if store != nil {
		s.SetLibraryStore(store)
	}
	return s
}

func TestSeederLib_SeedsBeforeWorkflows(t *testing.T) {
	var log []string
	store := newFakeLibStore()
	store.log = &log
	src := &fakeSource{
		names: []string{"pr-review"},
		wf:    map[string]*workflow.Workflow{"pr-review": {Name: "pr-review"}},
		lib:   map[string][]byte{"README.md": []byte("lib")},
	}
	led := &fakeLedger{}
	s := samples.NewSeeder(src, orderRepo{&fakeRepo{}, &log}, &fakePublished{}, led, uuid.New(), discardLogger())
	s.SetLibraryStore(store)

	require.NoError(t, s.Seed(context.Background()))
	require.Equal(t, []string{"lib:README.md", "create:pr-review"}, log)
	require.Contains(t, led.recorded, "@lib/README.md")
	require.Equal(t, []byte("lib"), store.files["README.md"])
}

func TestSeederLib_SecondBootNoop(t *testing.T) {
	store := newFakeLibStore()
	src := &fakeSource{lib: map[string][]byte{"README.md": []byte("lib")}}
	led := &fakeLedger{}
	s := libSeeder(src, store, led)
	require.NoError(t, s.Seed(context.Background()))

	led.seeded = map[string]bool{"@lib/README.md": true}
	require.NoError(t, s.Seed(context.Background()))
	require.Equal(t, 1, store.writes)
	require.Equal(t, []string{"@lib/README.md"}, led.recorded)
}

func TestSeederLib_NewFileInLaterVersionSeeds(t *testing.T) {
	store := newFakeLibStore()
	store.files["README.md"] = []byte("old")
	led := &fakeLedger{seeded: map[string]bool{"@lib/README.md": true}}
	src := &fakeSource{lib: map[string][]byte{"README.md": []byte("new"), "extra.md": []byte("e")}}

	require.NoError(t, libSeeder(src, store, led).Seed(context.Background()))
	require.Equal(t, []byte("e"), store.files["extra.md"])
	require.Equal(t, []byte("old"), store.files["README.md"])
}

func TestSeederLib_ChangedFileNotOverwritten(t *testing.T) {
	store := newFakeLibStore()
	store.files["README.md"] = []byte("admin edit")
	led := &fakeLedger{seeded: map[string]bool{"@lib/README.md": true}}
	src := &fakeSource{lib: map[string][]byte{"README.md": []byte("shipped v2")}}

	require.NoError(t, libSeeder(src, store, led).Seed(context.Background()))
	require.Equal(t, []byte("admin edit"), store.files["README.md"])
	require.Zero(t, store.writes)
}

func TestSeederLib_PreexistingSharedFileRecordedNotOverwritten(t *testing.T) {
	store := newFakeLibStore()
	store.files["README.md"] = []byte("already here")
	led := &fakeLedger{}
	src := &fakeSource{lib: map[string][]byte{"README.md": []byte("shipped")}}

	require.NoError(t, libSeeder(src, store, led).Seed(context.Background()))
	require.Equal(t, []byte("already here"), store.files["README.md"])
	require.Zero(t, store.writes)
	require.Equal(t, []string{"@lib/README.md"}, led.recorded)
}

func TestSeederLib_DeletedSeededFileNotRecreated(t *testing.T) {
	store := newFakeLibStore() // file absent: an admin deleted it
	led := &fakeLedger{seeded: map[string]bool{"@lib/README.md": true}}
	src := &fakeSource{lib: map[string][]byte{"README.md": []byte("shipped")}}

	require.NoError(t, libSeeder(src, store, led).Seed(context.Background()))
	require.Zero(t, store.writes)
	require.Empty(t, led.recorded)
}

func TestSeederLib_StoreWriteErrorAborts(t *testing.T) {
	store := newFakeLibStore()
	store.writeErr = errors.New("disk full")
	led := &fakeLedger{}
	repo := &fakeRepo{}
	src := &fakeSource{
		names: []string{"pr-review"},
		wf:    map[string]*workflow.Workflow{"pr-review": {Name: "pr-review"}},
		lib:   map[string][]byte{"README.md": []byte("x")},
	}
	s := samples.NewSeeder(src, repo, &fakePublished{}, led, uuid.New(), discardLogger())
	s.SetLibraryStore(store)

	err := s.Seed(context.Background())
	require.ErrorContains(t, err, "disk full")
	require.Empty(t, led.recorded)
	require.Empty(t, repo.created, "no workflow is seeded after a library failure")
}

func TestSeederLib_InvalidNameSkippedNotRecorded(t *testing.T) {
	store := newFakeLibStore()
	led := &fakeLedger{}
	src := &fakeSource{lib: map[string][]byte{"workflow.json": []byte("{}"), "ok.md": []byte("ok")}}

	require.NoError(t, libSeeder(src, store, led).Seed(context.Background()))
	require.NotContains(t, store.files, "workflow.json")
	require.Equal(t, []string{"@lib/ok.md"}, led.recorded)
}

func TestSeederLib_NoLibFolderSkipsSilently(t *testing.T) {
	store := newFakeLibStore()
	led := &fakeLedger{}
	require.NoError(t, libSeeder(&fakeSource{}, store, led).Seed(context.Background()))
	require.Zero(t, store.writes)
	require.Empty(t, led.recorded)
}

func TestSeederLib_NoStoreConfiguredSkipsLibPass(t *testing.T) {
	led := &fakeLedger{}
	src := &fakeSource{lib: map[string][]byte{"README.md": []byte("x")}}
	require.NoError(t, libSeeder(src, nil, led).Seed(context.Background()))
	require.Empty(t, led.recorded)
}

func TestSeederLib_ReadLibraryErrorAborts(t *testing.T) {
	src := &fakeSource{libErr: errors.New("boom")}
	err := libSeeder(src, newFakeLibStore(), &fakeLedger{}).Seed(context.Background())
	require.ErrorContains(t, err, "boom")
}
