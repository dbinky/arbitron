package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Protocol adapters for the helper seam only. The durable journal/tree/lock
// producers and the initDomain/SQLite probe belong to P2T7/P2T9.
type helperSeedKey struct{}
type helperSeedMutations struct{ mu sync.Mutex }

func (m *helperSeedMutations) WithMutations(ctx context.Context, ds []workflow.MutationTarget, fn func(context.Context) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(context.WithValue(ctx, helperSeedKey{}, ds))
}
func helperSeedHeld(ctx context.Context, s workflow.Scope, n string) error {
	ds, _ := ctx.Value(helperSeedKey{}).([]workflow.MutationTarget)
	for _, d := range ds {
		if d.Scope == s && d.Name == n {
			return nil
		}
	}
	return errors.New("missing helper mutation wiring")
}

type helperSeedRepo struct {
	*filesystem.WorkflowRepo
	commits map[string]workflow.TreeCommit
}

func (r *helperSeedRepo) PrepareTreeCommit(ctx context.Context, d workflow.MutationTarget, rev string) (workflow.TreeCommit, error) {
	if err := helperSeedHeld(ctx, d.Scope, d.Name); err != nil {
		return workflow.TreeCommit{}, err
	}
	if exists, err := r.Exists(ctx, d.Scope, d.Name); err != nil {
		return workflow.TreeCommit{}, err
	} else if exists {
		return workflow.TreeCommit{}, errors.New("helper probe expects fresh destination")
	}
	c := workflow.TreeCommit{ID: uuid.NewString(), Destination: d, Revision: rev}
	r.commits[c.ID] = c
	return c, nil
}
func (r *helperSeedRepo) FinishTreeCommit(_ context.Context, id string) error {
	delete(r.commits, id)
	return nil
}
func (r *helperSeedRepo) RollbackTreeCommit(ctx context.Context, id string) error {
	c, ok := r.commits[id]
	if !ok {
		return nil
	}
	if exists, err := r.Exists(ctx, c.Destination.Scope, c.Destination.Name); err != nil {
		return err
	} else if exists {
		if err := r.Delete(ctx, c.Destination.Scope, c.Destination.Name); err != nil {
			return err
		}
	}
	delete(r.commits, id)
	return nil
}
func (r *helperSeedRepo) PendingTreeCommits(context.Context) ([]workflow.TreeCommit, error) {
	var cs []workflow.TreeCommit
	for _, c := range r.commits {
		cs = append(cs, c)
	}
	return cs, nil
}

type helperSeedJournal struct {
	entries map[string]workflow.JournalEntry
}

func (j *helperSeedJournal) Record(ctx context.Context, r workflow.ArrangementRequest) error {
	if err := helperSeedHeld(ctx, r.Scope, r.Name); err != nil {
		return err
	}
	r.Manifest = append([]byte(nil), r.Manifest...)
	e, ok := j.entries[r.Revision]
	if !ok {
		e.State = workflow.JournalPrepared
	}
	e.Request = r
	j.entries[r.Revision] = e
	return nil
}
func (j *helperSeedJournal) MarkCommitted(_ context.Context, rev string) error {
	e, ok := j.entries[rev]
	if !ok {
		return workflow.ErrNotFound
	}
	e.State = workflow.JournalCommitted
	j.entries[rev] = e
	return nil
}
func (j *helperSeedJournal) Remove(_ context.Context, rev string) error {
	delete(j.entries, rev)
	return nil
}
func (j *helperSeedJournal) Quarantine(ctx context.Context, rev, reason string) error {
	return j.Remove(ctx, rev)
}
func (j *helperSeedJournal) Entries(context.Context) ([]workflow.JournalEntry, error) {
	var es []workflow.JournalEntry
	for _, e := range j.entries {
		es = append(es, e)
	}
	return es, nil
}
func (j *helperSeedJournal) List(ctx context.Context) ([]workflow.ArrangementRequest, error) {
	es, err := j.Entries(ctx)
	var rs []workflow.ArrangementRequest
	for _, e := range es {
		rs = append(rs, e.Request)
	}
	return rs, err
}
func (*helperSeedJournal) BeginRelocation(context.Context, workflow.Scope, string, workflow.Scope, string, uuid.UUID) (workflow.RelocationIntent, error) {
	return workflow.RelocationIntent{}, errors.New("unexpected relocation")
}
func (*helperSeedJournal) RelocationIntentsAt(context.Context, workflow.Scope, string) ([]workflow.RelocationIntent, error) {
	return nil, nil
}
func (*helperSeedJournal) RelocationIntents(context.Context) ([]workflow.RelocationIntent, error) {
	return nil, nil
}
func (*helperSeedJournal) Relocate(context.Context, workflow.Scope, string, workflow.Scope, string) error {
	return errors.New("unexpected relocation")
}
func (*helperSeedJournal) EndRelocation(context.Context, string) error {
	return errors.New("unexpected relocation")
}
func (*helperSeedJournal) Settle(ctx context.Context, s workflow.Scope, n string) error {
	return helperSeedHeld(ctx, s, n)
}

type helperSeedPublications struct {
	recordingPublished
	proofs map[string]workflow.Publication
}

func (p *helperSeedPublications) CommitPublication(ctx context.Context, n string, v workflow.Publication) error {
	if err := helperSeedHeld(ctx, workflow.Shared(), n); err != nil {
		return err
	}
	p.proofs[n] = v
	return nil
}
func (p *helperSeedPublications) PublicationOf(_ context.Context, n string) (workflow.Publication, bool, error) {
	v, ok := p.proofs[n]
	return v, ok, nil
}
func (p *helperSeedPublications) PublisherOf(ctx context.Context, n string) (uuid.UUID, bool, error) {
	v, ok, err := p.PublicationOf(ctx, n)
	return v.Publisher, ok, err
}
func (*helperSeedPublications) RenamePublication(context.Context, uuid.UUID, string, string, workflow.Publication) error {
	return errors.New("unexpected rename")
}

type helperSeedRegistry struct {
	workflow.Registry
	entries map[string]workflow.RegistryEntry
}

func (r *helperSeedRegistry) Resolve(_ context.Context, n string) (workflow.RegistryEntry, error) {
	e, ok := r.entries[n]
	if !ok {
		return e, workflow.ErrNotFound
	}
	return e, nil
}
func (r *helperSeedRegistry) Register(_ context.Context, e workflow.RegistryEntry) error {
	if _, ok := r.entries[e.Name]; ok {
		return workflow.ErrAlreadyExists
	}
	r.entries[e.Name] = e
	return nil
}
func (r *helperSeedRegistry) Delete(_ context.Context, id uuid.UUID) error {
	for n, e := range r.entries {
		if e.ID == id {
			delete(r.entries, n)
			return nil
		}
	}
	return workflow.ErrNotFound
}

type helperSeedRequester struct {
	t        *testing.T
	repo     *helperSeedRepo
	pubs     *helperSeedPublications
	journal  *helperSeedJournal
	root     string
	requests []workflow.ArrangementRequest
}

func (r *helperSeedRequester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error {
	r.t.Helper()
	require.NoError(r.t, helperSeedHeld(ctx, req.Scope, req.Name))
	saved, err := r.repo.Load(ctx, req.Scope, req.Name)
	require.NoError(r.t, err)
	b, err := json.Marshal(saved)
	require.NoError(r.t, err)
	require.JSONEq(r.t, string(b), string(req.Manifest))
	require.Equal(r.t, saved.SavedRevision(), req.Revision)
	require.NotEmpty(r.t, req.Revision)
	require.Equal(r.t, workflow.Shared(), req.Scope)
	require.Equal(r.t, "sample", req.Name)
	require.Equal(r.t, saved.ID, req.WorkflowID)
	require.Equal(r.t, req.Revision, r.pubs.proofs[req.Name].Revision)
	require.Equal(r.t, req.WorkflowID, r.pubs.proofs[req.Name].WorkflowID)
	require.Equal(r.t, workflow.JournalCommitted, r.journal.entries[req.Revision].State)
	prompt, err := os.ReadFile(filepath.Join(r.root, "shared", req.Name, "prompts", "main.md"))
	require.NoError(r.t, err)
	require.Equal(r.t, "source prompt", string(prompt))
	r.requests = append(r.requests, req)
	return nil
}
func (*helperSeedRequester) RelocateArrangements(context.Context, workflow.Scope, string, workflow.Scope, string) error {
	return errors.New("unexpected relocation")
}

func TestSeedSampleWorkflows_RevisionWiring(t *testing.T) {
	source, workspace := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(source, "sample", "prompts"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "sample", "workflow.json"), []byte(`{"name":"sample","nodes":[{"id":"start","position":{"x":12,"y":34}},{"id":"end"}],"edges":[{"source":"start","target":"end"}],"presentation":{"revision":"foreign-source"}}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "sample", "prompts", "main.md"), []byte("source prompt"), 0644))
	repo := &helperSeedRepo{WorkflowRepo: filesystem.NewWorkflowRepo(workspace), commits: map[string]workflow.TreeCommit{}}
	journal := &helperSeedJournal{entries: map[string]workflow.JournalEntry{}}
	pub := &helperSeedPublications{recordingPublished: recordingPublished{t: t}, proofs: map[string]workflow.Publication{}}
	registry := &helperSeedRegistry{entries: map[string]workflow.RegistryEntry{}}
	requester := &helperSeedRequester{t: t, repo: repo, pubs: pub, journal: journal, root: workspace}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: requester, Settler: journal, Logger: logger})
	require.NoError(t, err)
	mutations := &helperSeedMutations{}
	owner := uuid.New()
	require.NoError(t, seedSampleWorkflows(context.Background(), source, workspace, repo, pub, stamper, mutations, registry, nil, nil, owner, logger))
	require.Len(t, requester.requests, 1)
	req := requester.requests[0]
	require.NotEqual(t, uuid.Nil, req.WorkflowID)
	assert.Equal(t, registry.entries["sample"].ID, req.WorkflowID)
	assert.Equal(t, workflow.ScopeShared, registry.entries["sample"].Scope)
	assert.Equal(t, workflow.NodePosition{X: 12, Y: 34}, req.Retained.Positions["start"])
	var captured workflow.Workflow
	require.NoError(t, json.Unmarshal(req.Manifest, &captured))
	assert.Len(t, captured.Nodes, 2)
	assert.Len(t, captured.Edges, 1)
	assert.NotEqual(t, "foreign-source", req.Revision)
	assert.Empty(t, journal.entries)
	ledger, err := filesystem.NewSeedLedger(workspace).Seeded(context.Background())
	require.NoError(t, err)
	assert.True(t, ledger["sample"])
	require.NoError(t, seedSampleWorkflows(context.Background(), source, workspace, repo, pub, stamper, mutations, registry, nil, nil, owner, logger))
	assert.Len(t, requester.requests, 1)
}
