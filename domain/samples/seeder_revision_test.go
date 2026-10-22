package samples_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These adapters exercise the save protocol against real manifest/supporting
// bytes. They are not substitutes for P2T7's durable, cross-process adapters.
type seedLockKey struct{}
type seedLocks struct{ mu sync.Mutex }

func (m *seedLocks) WithMutations(ctx context.Context, targets []workflow.MutationTarget, fn func(context.Context) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(context.WithValue(ctx, seedLockKey{}, append([]workflow.MutationTarget(nil), targets...)))
}
func seedHeld(ctx context.Context, scope workflow.Scope, name string) error {
	targets, _ := ctx.Value(seedLockKey{}).([]workflow.MutationTarget)
	for _, d := range targets {
		if d.Scope == scope && d.Name == name {
			return nil
		}
	}
	return errors.New("seed destination not locked")
}

type seedJournal struct {
	entries map[string]workflow.JournalEntry
	events  *[]string
	fail    map[string]error
}

func (j *seedJournal) Record(ctx context.Context, r workflow.ArrangementRequest) error {
	*j.events = append(*j.events, "prepare")
	if err := seedHeld(ctx, r.Scope, r.Name); err != nil {
		return err
	}
	if err := j.fail["journal"]; err != nil {
		return err
	}
	r.Manifest = append([]byte(nil), r.Manifest...)
	state := workflow.JournalPrepared
	if e, ok := j.entries[r.Revision]; ok {
		state = e.State
	}
	j.entries[r.Revision] = workflow.JournalEntry{Request: r, State: state}
	return nil
}
func (j *seedJournal) MarkCommitted(ctx context.Context, revision string) error {
	*j.events = append(*j.events, "receipt")
	if err := j.fail["receipt"]; err != nil {
		return err
	}
	e, ok := j.entries[revision]
	if !ok {
		return workflow.ErrNotFound
	}
	if err := seedHeld(ctx, e.Request.Scope, e.Request.Name); err != nil {
		return err
	}
	e.State = workflow.JournalCommitted
	j.entries[revision] = e
	return nil
}
func (j *seedJournal) Remove(_ context.Context, revision string) error {
	*j.events = append(*j.events, "remove")
	if err := j.fail["remove"]; err != nil {
		return err
	}
	delete(j.entries, revision)
	return nil
}
func (j *seedJournal) Quarantine(ctx context.Context, revision, reason string) error {
	*j.events = append(*j.events, "quarantine:"+reason)
	return j.Remove(ctx, revision)
}
func (j *seedJournal) Entries(context.Context) ([]workflow.JournalEntry, error) {
	if err := j.fail["entries"]; err != nil {
		return nil, err
	}
	var out []workflow.JournalEntry
	for _, e := range j.entries {
		out = append(out, e)
	}
	return out, nil
}
func (j *seedJournal) List(ctx context.Context) ([]workflow.ArrangementRequest, error) {
	es, err := j.Entries(ctx)
	var out []workflow.ArrangementRequest
	for _, e := range es {
		out = append(out, e.Request)
	}
	return out, err
}
func (*seedJournal) BeginRelocation(context.Context, workflow.Scope, string, workflow.Scope, string, uuid.UUID) (workflow.RelocationIntent, error) {
	return workflow.RelocationIntent{}, errors.New("unexpected seed relocation")
}
func (*seedJournal) RelocationIntentsAt(context.Context, workflow.Scope, string) ([]workflow.RelocationIntent, error) {
	return nil, nil
}
func (*seedJournal) RelocationIntents(context.Context) ([]workflow.RelocationIntent, error) {
	return nil, nil
}
func (*seedJournal) Relocate(context.Context, workflow.Scope, string, workflow.Scope, string) error {
	return errors.New("unexpected seed relocation")
}
func (*seedJournal) EndRelocation(context.Context, string) error {
	return errors.New("unexpected seed relocation")
}

type seedRepo struct {
	*filesystem.WorkflowRepo
	events  *[]string
	fail    map[string]error
	commits map[string]workflow.TreeCommit
	backups map[string]workflow.Scope
}

func (r *seedRepo) Create(ctx context.Context, s workflow.Scope, n string, w *workflow.Workflow) error {
	if err := seedHeld(ctx, s, n); err != nil {
		return err
	}
	*r.events = append(*r.events, "tree")
	if err := r.fail["tree"]; err != nil {
		return err
	}
	return r.WorkflowRepo.Create(ctx, s, n, w)
}
func (r *seedRepo) WriteFile(ctx context.Context, s workflow.Scope, n, p string, b []byte) error {
	if err := seedHeld(ctx, s, n); err != nil {
		return err
	}
	*r.events = append(*r.events, "file:"+p)
	if err := r.fail["file"]; err != nil {
		return err
	}
	return r.WorkflowRepo.WriteFile(ctx, s, n, p, b)
}
func (r *seedRepo) PrepareTreeCommit(ctx context.Context, d workflow.MutationTarget, rev string) (workflow.TreeCommit, error) {
	if err := seedHeld(ctx, d.Scope, d.Name); err != nil {
		return workflow.TreeCommit{}, err
	}
	*r.events = append(*r.events, "backup")
	if err := r.fail["backup"]; err != nil {
		return workflow.TreeCommit{}, err
	}
	old, err := r.Load(ctx, d.Scope, d.Name)
	if err != nil && !errors.Is(err, workflow.ErrNotFound) {
		return workflow.TreeCommit{}, err
	}
	c := workflow.TreeCommit{ID: uuid.NewString(), Destination: d, Revision: rev, HadPrior: err == nil}
	if c.HadPrior {
		c.PriorRevision = old.SavedRevision()
		backup := workflow.Private(uuid.New())
		if err := r.WorkflowRepo.CopyTree(ctx, d.Scope, d.Name, backup, d.Name, false); err != nil {
			return c, err
		}
		r.backups[c.ID] = backup
	}
	r.commits[c.ID] = c
	return c, nil
}
func (r *seedRepo) FinishTreeCommit(ctx context.Context, id string) error {
	if err := r.fail["finish"]; err != nil {
		return err
	}
	c, ok := r.commits[id]
	if !ok {
		return nil
	}
	if b, ok := r.backups[id]; ok {
		if err := r.WorkflowRepo.Delete(ctx, b, c.Destination.Name); err != nil {
			return err
		}
	}
	delete(r.commits, id)
	delete(r.backups, id)
	return nil
}
func (r *seedRepo) RollbackTreeCommit(ctx context.Context, id string) error {
	*r.events = append(*r.events, "rollback")
	if err := r.fail["rollback"]; err != nil {
		return err
	}
	c, ok := r.commits[id]
	if !ok {
		return nil
	}
	if err := seedHeld(ctx, c.Destination.Scope, c.Destination.Name); err != nil {
		return err
	}
	exists, err := r.Exists(ctx, c.Destination.Scope, c.Destination.Name)
	if err != nil {
		return err
	}
	if exists {
		if err := r.WorkflowRepo.Delete(ctx, c.Destination.Scope, c.Destination.Name); err != nil {
			return err
		}
	}
	if c.HadPrior {
		if err := r.WorkflowRepo.CopyTree(ctx, r.backups[id], c.Destination.Name, c.Destination.Scope, c.Destination.Name, false); err != nil {
			return err
		}
	}
	return r.FinishTreeCommit(ctx, id)
}
func (r *seedRepo) PendingTreeCommits(context.Context) ([]workflow.TreeCommit, error) {
	var out []workflow.TreeCommit
	for _, c := range r.commits {
		out = append(out, c)
	}
	return out, nil
}

type seedPublications struct {
	workflow.PublishedStore
	pubs   map[string]workflow.Publication
	events *[]string
	fail   map[string]error
}

func (p *seedPublications) PublicationOf(_ context.Context, n string) (workflow.Publication, bool, error) {
	v, ok := p.pubs[n]
	return v, ok, p.fail["publication-read"]
}
func (p *seedPublications) PublisherOf(ctx context.Context, n string) (uuid.UUID, bool, error) {
	v, ok, err := p.PublicationOf(ctx, n)
	return v.Publisher, ok, err
}
func (p *seedPublications) CommitPublication(ctx context.Context, n string, v workflow.Publication) error {
	if err := seedHeld(ctx, workflow.Shared(), n); err != nil {
		return err
	}
	*p.events = append(*p.events, "publication")
	if err := p.fail["publication"]; err != nil {
		return err
	}
	p.pubs[n] = v
	return nil
}
func (p *seedPublications) RenamePublication(context.Context, uuid.UUID, string, string, workflow.Publication) error {
	return errors.New("unexpected seed rename")
}

type seedRequester struct {
	rows   map[string]workflow.ArrangementRequest
	events *[]string
	fail   map[string]error
	repo   workflow.Repository
	pubs   *seedPublications
}

func (r *seedRequester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error {
	if err := seedHeld(ctx, req.Scope, req.Name); err != nil {
		return err
	}
	*r.events = append(*r.events, "request")
	saved, err := r.repo.Load(ctx, req.Scope, req.Name)
	if err != nil {
		return err
	}
	b, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	var a, c any
	if json.Unmarshal(req.Manifest, &a) != nil || json.Unmarshal(b, &c) != nil {
		return errors.New("invalid request manifest")
	}
	if !reflect.DeepEqual(a, c) || r.pubs.pubs[req.Name].Revision != req.Revision || r.pubs.pubs[req.Name].WorkflowID != req.WorkflowID || saved.ID != req.WorkflowID {
		return errors.New("request does not match exact saved publication")
	}
	if err := r.fail["enqueue"]; err != nil {
		return err
	}
	r.rows[req.Revision] = req
	return nil
}
func (*seedRequester) RelocateArrangements(context.Context, workflow.Scope, string, workflow.Scope, string) error {
	return errors.New("unexpected seed relocation")
}

type seedSettler struct {
	events *[]string
	fail   map[string]error
	settle func(context.Context, workflow.Scope, string) error
}

func (s *seedSettler) Settle(ctx context.Context, scope workflow.Scope, name string) error {
	if err := seedHeld(ctx, scope, name); err != nil {
		return err
	}
	*s.events = append(*s.events, "settle")
	if err := s.fail["settle"]; err != nil {
		return err
	}
	if s.settle != nil {
		return s.settle(ctx, scope, name)
	}
	return nil
}

type revisionLedger struct {
	mu     sync.Mutex
	names  map[string]bool
	events *[]string
	fail   map[string]error
}

func (l *revisionLedger) Seeded(context.Context) (map[string]bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]bool{}
	for n, v := range l.names {
		out[n] = v
	}
	return out, l.fail["ledger-read"]
}
func (l *revisionLedger) Record(_ context.Context, n string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.events = append(*l.events, "ledger")
	if err := l.fail["ledger"]; err != nil {
		return err
	}
	l.names[n] = true
	return nil
}

type seedRegistry struct {
	workflow.Registry
	entries map[string]workflow.RegistryEntry
	fail    map[string]error
}

func (r *seedRegistry) Resolve(_ context.Context, n string) (workflow.RegistryEntry, error) {
	if err := r.fail["identity-read"]; err != nil {
		return workflow.RegistryEntry{}, err
	}
	v, ok := r.entries[n]
	if !ok {
		return v, workflow.ErrNotFound
	}
	return v, nil
}
func (r *seedRegistry) Register(_ context.Context, e workflow.RegistryEntry) error {
	if err := r.fail["identity"]; err != nil {
		return err
	}
	if _, ok := r.entries[e.Name]; ok {
		return workflow.ErrAlreadyExists
	}
	r.entries[e.Name] = e
	return nil
}
func (r *seedRegistry) Delete(_ context.Context, id uuid.UUID) error {
	if err := r.fail["identity-rollback"]; err != nil {
		return err
	}
	for n, e := range r.entries {
		if e.ID == id {
			delete(r.entries, n)
			return nil
		}
	}
	return workflow.ErrNotFound
}

type seedFixture struct {
	s            *samples.Seeder
	source       *fakeSource
	repo         *seedRepo
	journal      *seedJournal
	requester    *seedRequester
	publications *seedPublications
	ledger       *revisionLedger
	registry     *seedRegistry
	locks        *seedLocks
	settler      *seedSettler
	stamper      *workflow.RevisionStamper
	events       []string
	fail         map[string]error
	owner        uuid.UUID
}

func newSeedFixture(t *testing.T, registered bool) *seedFixture {
	t.Helper()
	f := &seedFixture{fail: map[string]error{}, owner: uuid.New(), locks: &seedLocks{}}
	f.source = &fakeSource{names: []string{"sample"}, wf: map[string]*workflow.Workflow{"sample": {Name: "source", ID: uuid.New(), Nodes: []workflow.Node{{ID: "a", Position: &workflow.NodePosition{X: 12, Y: 34}}, {ID: "b"}}, Edges: []workflow.Edge{{Source: "a", Target: "b"}}, Presentation: &workflow.Presentation{Revision: "untrusted"}}}, files: map[string]map[string][]byte{"sample": {"prompts/main.md": []byte("real supporting input")}}}
	f.repo = &seedRepo{WorkflowRepo: filesystem.NewWorkflowRepo(t.TempDir()), events: &f.events, fail: f.fail, commits: map[string]workflow.TreeCommit{}, backups: map[string]workflow.Scope{}}
	f.journal = &seedJournal{entries: map[string]workflow.JournalEntry{}, events: &f.events, fail: f.fail}
	f.publications = &seedPublications{pubs: map[string]workflow.Publication{}, events: &f.events, fail: f.fail}
	f.requester = &seedRequester{rows: map[string]workflow.ArrangementRequest{}, events: &f.events, fail: f.fail, repo: f.repo, pubs: f.publications}
	f.ledger = &revisionLedger{names: map[string]bool{}, events: &f.events, fail: f.fail}
	f.settler = &seedSettler{events: &f.events, fail: f.fail}
	var err error
	f.stamper, err = workflow.NewRevisionStamper(f.repo, workflow.StamperDeps{Journal: f.journal, Requester: f.requester, Settler: f.settler, Logger: discardLogger()})
	require.NoError(t, err)
	f.s = samples.NewSeeder(f.source, f.repo, f.publications, f.ledger, f.owner, discardLogger())
	f.s.SetRevisionStamper(f.stamper)
	f.s.SetMutationCoordinator(f.locks)
	if registered {
		f.registry = &seedRegistry{entries: map[string]workflow.RegistryEntry{}, fail: f.fail}
		f.s.SetRegistry(f.registry)
	}
	return f
}
func TestSeedRevisionFreshCommit(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint(registered), func(t *testing.T) {
			f := newSeedFixture(t, registered)
			f.source.wf["sample"].ID = uuid.Nil
			require.NoError(t, f.s.Seed(context.Background()))
			saved, err := f.repo.Load(context.Background(), workflow.Shared(), "sample")
			require.NoError(t, err)
			require.Len(t, f.requester.rows, 1)
			req := f.requester.rows[saved.SavedRevision()]
			require.NotEmpty(t, req.Revision)
			assert.NotEqual(t, "untrusted", req.Revision)
			assert.Equal(t, workflow.Shared(), req.Scope)
			assert.Equal(t, "sample", req.Name)
			assert.Equal(t, "sample", saved.Name)
			assert.Equal(t, saved.ID, req.WorkflowID)
			assert.Equal(t, workflow.SaveReasonSeed, req.Reason)
			assert.Equal(t, f.source.wf["sample"].Nodes, saved.Nodes)
			assert.Equal(t, f.source.wf["sample"].Edges, saved.Edges)
			b, err := json.Marshal(saved)
			require.NoError(t, err)
			assert.JSONEq(t, string(b), string(req.Manifest))
			assert.Equal(t, workflow.RetainedFromManifest, req.Retained.Source)
			assert.Equal(t, workflow.NodePosition{X: 12, Y: 34}, req.Retained.Positions["a"])
			assert.Equal(t, req.Revision, f.publications.pubs["sample"].Revision)
			assert.Equal(t, saved.ID, f.publications.pubs["sample"].WorkflowID)
			if registered {
				assert.Equal(t, saved.ID, f.registry.entries["sample"].ID)
				assert.Equal(t, workflow.ScopeShared, f.registry.entries["sample"].Scope)
			}
			files, err := f.repo.ListFiles(context.Background(), workflow.Shared(), "sample")
			require.NoError(t, err)
			assert.Contains(t, files, "prompts/main.md")
			assert.True(t, f.ledger.names["sample"])
			assert.Subset(t, f.events, []string{"prepare", "backup", "tree", "file:prompts/main.md", "publication", "receipt", "request", "remove", "ledger"})
			index := func(s string) int {
				for i, e := range f.events {
					if e == s {
						return i
					}
				}
				return -1
			}
			for _, pair := range [][2]string{{"prepare", "backup"}, {"backup", "tree"}, {"tree", "file:prompts/main.md"}, {"file:prompts/main.md", "publication"}, {"publication", "receipt"}, {"receipt", "request"}, {"request", "remove"}, {"publication", "ledger"}, {"request", "ledger"}} {
				assert.Less(t, index(pair[0]), index(pair[1]), "events: %v", f.events)
			}
		})
	}
}

func TestSeedRevisionOnceAndResume(t *testing.T) {
	t.Run("already seeded legacy stays untouched", func(t *testing.T) {
		f := newSeedFixture(t, true)
		legacy := &workflow.Workflow{Name: "sample"}
		require.NoError(t, f.repo.WorkflowRepo.Create(context.Background(), workflow.Shared(), "sample", legacy))
		f.ledger.names["sample"] = true
		require.NoError(t, f.s.Seed(context.Background()))
		assert.Empty(t, f.events)
		assert.Empty(t, f.requester.rows)
		saved, err := f.repo.Load(context.Background(), workflow.Shared(), "sample")
		require.NoError(t, err)
		assert.Nil(t, saved.Presentation)
	})
	t.Run("ledger failure with retained receipt resumes immutable input", func(t *testing.T) {
		f := newSeedFixture(t, true)
		f.fail["ledger"] = errors.New("ledger unavailable")
		f.fail["remove"] = errors.New("cleanup unavailable")
		require.Error(t, f.s.Seed(context.Background()))
		before, err := f.repo.Load(context.Background(), workflow.Shared(), "sample")
		require.NoError(t, err)
		require.Len(t, f.requester.rows, 1)
		req := f.requester.rows[before.SavedRevision()]
		f.source.wf["sample"] = &workflow.Workflow{Name: "changed", Nodes: []workflow.Node{{ID: "not the committed graph"}}}
		delete(f.fail, "ledger")
		delete(f.fail, "remove")
		f.events = nil
		require.NoError(t, f.s.Seed(context.Background()))
		after, err := f.repo.Load(context.Background(), workflow.Shared(), "sample")
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assert.Equal(t, req, f.requester.rows[before.SavedRevision()])
		assert.Len(t, f.requester.rows, 1)
		assert.NotContains(t, f.events, "prepare")
		assert.NotContains(t, f.events, "tree")
		assert.True(t, f.ledger.names["sample"])
	})
	t.Run("two seeders share destination lock", func(t *testing.T) {
		f := newSeedFixture(t, true)
		other := samples.NewSeeder(f.source, f.repo, f.publications, f.ledger, f.owner, discardLogger())
		other.SetRevisionStamper(f.stamper)
		other.SetMutationCoordinator(f.locks)
		other.SetRegistry(f.registry)
		errs := make(chan error, 2)
		go func() { errs <- f.s.Seed(context.Background()) }()
		go func() { errs <- other.Seed(context.Background()) }()
		require.NoError(t, <-errs)
		require.NoError(t, <-errs)
		assert.Len(t, f.requester.rows, 1)
		assert.Len(t, f.publications.pubs, 1)
		count := 0
		for _, e := range f.events {
			if e == "prepare" {
				count++
			}
		}
		assert.Equal(t, 1, count)
	})
	for _, mode := range []string{"orphan", "foreign-presentation", "older-publication", "pre-feature", "acknowledged-without-journal"} {
		t.Run(mode, func(t *testing.T) {
			f := newSeedFixture(t, false)
			if mode == "acknowledged-without-journal" {
				f.fail["ledger"] = errors.New("ledger unavailable")
				require.Error(t, f.s.Seed(context.Background()))
				delete(f.fail, "ledger")
			} else {
				wf := &workflow.Workflow{Name: "sample", Description: "unrelated"}
				if mode == "foreign-presentation" {
					wf.Presentation = &workflow.Presentation{Revision: uuid.Must(uuid.NewV7()).String(), Name: "somewhere-else"}
				}
				require.NoError(t, f.repo.WorkflowRepo.Create(context.Background(), workflow.Shared(), "sample", wf))
				if mode == "older-publication" {
					f.publications.pubs["sample"] = workflow.Publication{Publisher: f.owner, Revision: uuid.Must(uuid.NewV7()).String(), WorkflowID: uuid.New()}
				}
			}
			before, err := f.repo.Load(context.Background(), workflow.Shared(), "sample")
			require.NoError(t, err)
			f.events = nil
			count := len(f.requester.rows)
			require.Error(t, f.s.Seed(context.Background()))
			after, err := f.repo.Load(context.Background(), workflow.Shared(), "sample")
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Len(t, f.requester.rows, count)
			assert.NotContains(t, f.events, "prepare")
			assert.False(t, f.ledger.names["sample"])
		})
	}
}

func TestSeed_LegacyOrphanIsNotAdopted(t *testing.T) {
	src := &fakeSource{names: []string{"sample"}, wf: map[string]*workflow.Workflow{"sample": {Name: "sample"}}, files: map[string]map[string][]byte{"sample": {"prompt.md": []byte("do not overwrite")}}}
	repo := &fakeRepo{createErr: map[string]error{"sample": workflow.ErrAlreadyExists}, exists: map[string]bool{"sample": true}}
	ledger := &fakeLedger{}
	require.ErrorIs(t, samples.NewSeeder(src, repo, &fakePublished{}, ledger, uuid.New(), discardLogger()).Seed(context.Background()), workflow.ErrAlreadyExists)
	assert.Empty(t, repo.files)
	assert.Empty(t, ledger.recorded)
}

func TestSeedRevisionErrorMatrix(t *testing.T) {
	for _, boundary := range []string{"source-list", "source-read", "ledger-read", "settle", "identity-read", "identity", "journal", "backup", "tree", "file", "publication-read", "publication", "receipt", "enqueue", "remove", "ledger"} {
		t.Run(boundary, func(t *testing.T) {
			f := newSeedFixture(t, true)
			boom := errors.New("injected " + boundary)
			switch boundary {
			case "source-list":
				f.source.listErr = boom
			case "source-read":
				f.source.readErr = map[string]error{"sample": boom}
			default:
				f.fail[boundary] = boom
			}
			committed := boundary == "receipt" || boundary == "enqueue" || boundary == "remove" || boundary == "ledger"
			err := f.s.Seed(context.Background())
			if committed && boundary != "ledger" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, boom)
			}
			saved, loadErr := f.repo.Load(context.Background(), workflow.Shared(), "sample")
			if !committed {
				assert.ErrorIs(t, loadErr, workflow.ErrNotFound)
				assert.Empty(t, f.requester.rows)
				assert.Empty(t, f.journal.entries)
				assert.Empty(t, f.repo.commits)
				assert.Empty(t, f.registry.entries)
				assert.Empty(t, f.publications.pubs)
				assert.False(t, f.ledger.names["sample"])
			} else {
				require.NoError(t, loadErr)
				assert.NotEmpty(t, saved.SavedRevision())
				assert.Equal(t, saved.SavedRevision(), f.publications.pubs["sample"].Revision)
				if boundary == "receipt" {
					require.Len(t, f.journal.entries, 1)
					assert.Equal(t, workflow.JournalPrepared, f.journal.entries[saved.SavedRevision()].State)
					assert.Empty(t, f.requester.rows)
				} else if boundary == "enqueue" {
					require.Len(t, f.journal.entries, 1)
					assert.Equal(t, workflow.JournalCommitted, f.journal.entries[saved.SavedRevision()].State)
					assert.Empty(t, f.requester.rows)
				} else {
					assert.Len(t, f.requester.rows, 1)
				}
				assert.Equal(t, boundary != "ledger", f.ledger.names["sample"])
			}
		})
	}
	for _, writeBoundary := range []string{"file", "publication"} {
		t.Run(writeBoundary+" rollback failure", func(t *testing.T) {
			f := newSeedFixture(t, true)
			cause := errors.New("write/proof failure")
			rollbackErr := errors.New("restore failed")
			f.fail[writeBoundary] = cause
			f.fail["rollback"] = rollbackErr
			err := f.s.Seed(context.Background())
			require.ErrorIs(t, err, cause)
			require.ErrorIs(t, err, rollbackErr)
			saved, err := f.repo.Load(context.Background(), workflow.Shared(), "sample")
			require.NoError(t, err)
			assert.Len(t, f.repo.commits, 1)
			require.Len(t, f.journal.entries, 1)
			assert.Equal(t, workflow.JournalPrepared, f.journal.entries[saved.SavedRevision()].State)
			assert.Empty(t, f.requester.rows)
			assert.Empty(t, f.publications.pubs)
			assert.Len(t, f.registry.entries, 1)
			assert.False(t, f.ledger.names["sample"])
		})
	}
	t.Run("identity rollback failure retains prepare", func(t *testing.T) {
		f := newSeedFixture(t, true)
		f.fail["tree"] = errors.New("create failed")
		boom := errors.New("identity rollback failed")
		f.fail["identity-rollback"] = boom
		require.ErrorIs(t, f.s.Seed(context.Background()), boom)
		assert.Len(t, f.journal.entries, 1)
		assert.Empty(t, f.requester.rows)
	})
	t.Run("enqueue outage retains byte exact committed input", func(t *testing.T) {
		f := newSeedFixture(t, false)
		f.fail["enqueue"] = errors.New("queue down")
		require.NoError(t, f.s.Seed(context.Background()))
		var original workflow.ArrangementRequest
		for _, e := range f.journal.entries {
			original = e.Request
			assert.Equal(t, workflow.JournalCommitted, e.State)
		}
		require.NotEmpty(t, original.Revision)
		delete(f.fail, "enqueue")
		require.NoError(t, f.locks.WithMutations(context.Background(), []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "sample"}}, func(ctx context.Context) error {
			req, err := f.stamper.EnsureStamped(ctx, workflow.Shared(), "sample", workflow.SaveReasonSeed)
			if err != nil {
				return err
			}
			assert.Equal(t, original, req)
			f.stamper.Committed(ctx, req)
			return nil
		}))
		assert.Equal(t, original, f.requester.rows[original.Revision])
		assert.Empty(t, f.journal.entries)
	})
}

func TestSeedRevisionMalformedVersusInfrastructure(t *testing.T) {
	t.Run("malformed sample does not block valid sample", func(t *testing.T) {
		f := newSeedFixture(t, false)
		f.source.names = []string{"bad", "sample"}
		f.source.readErr = map[string]error{"bad": samples.ErrMalformedSample}
		require.NoError(t, f.s.Seed(context.Background()))
		assert.Len(t, f.requester.rows, 1)
		assert.True(t, f.ledger.names["sample"])
		assert.False(t, f.ledger.names["bad"])
	})
	for _, stamped := range []bool{false, true} {
		t.Run(fmt.Sprint(stamped), func(t *testing.T) {
			f := newSeedFixture(t, false)
			if !stamped {
				f.s.SetRevisionStamper(nil)
				f.s.SetMutationCoordinator(nil)
			}
			boom := errors.New("source disk failed")
			f.source.readErr = map[string]error{"sample": boom}
			require.ErrorIs(t, f.s.Seed(context.Background()), boom)
			assert.Empty(t, f.requester.rows)
			assert.Empty(t, f.publications.pubs)
		})
	}
}
