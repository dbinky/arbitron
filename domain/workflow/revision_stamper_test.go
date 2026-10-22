package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// All test ports retain/copy their load-bearing arguments. None is installed in production.
type stampJournal struct {
	entries     map[string]workflow.JournalEntry
	intents     map[string]workflow.RelocationIntent
	quarantined map[string]string
	events      *[]string
	fail        map[string]error
}

func cloneRequest(r workflow.ArrangementRequest) workflow.ArrangementRequest {
	r.Manifest = append([]byte(nil), r.Manifest...)
	if r.Retained.Positions != nil {
		p := map[string]workflow.NodePosition{}
		for k, v := range r.Retained.Positions {
			p[k] = v
		}
		r.Retained.Positions = p
	}
	return r
}
func (j *stampJournal) op(ctx context.Context, op string) error {
	*j.events = append(*j.events, op)
	if err := ctx.Err(); err != nil {
		return err
	}
	return j.fail[op]
}
func (j *stampJournal) Record(ctx context.Context, r workflow.ArrangementRequest) error {
	if err := j.op(ctx, "record"); err != nil {
		return err
	}
	j.entries[r.Revision] = workflow.JournalEntry{Request: cloneRequest(r), State: workflow.JournalPrepared}
	return nil
}
func (j *stampJournal) MarkCommitted(ctx context.Context, r string) error {
	if err := j.op(ctx, "receipt"); err != nil {
		return err
	}
	e, ok := j.entries[r]
	if !ok {
		return errors.New("unknown revision")
	}
	e.State = workflow.JournalCommitted
	j.entries[r] = e
	return nil
}
func (j *stampJournal) Remove(ctx context.Context, r string) error {
	if err := j.op(ctx, "remove"); err != nil {
		return err
	}
	delete(j.entries, r)
	return nil
}
func (j *stampJournal) Quarantine(ctx context.Context, r, reason string) error {
	if err := j.op(ctx, "quarantine"); err != nil {
		return err
	}
	j.quarantined[r] = reason
	delete(j.entries, r)
	return nil
}
func (j *stampJournal) Entries(ctx context.Context) ([]workflow.JournalEntry, error) {
	if err := j.op(ctx, "entries"); err != nil {
		return nil, err
	}
	out := []workflow.JournalEntry{}
	for _, e := range j.entries {
		e.Request = cloneRequest(e.Request)
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Request.RequestedAt.Before(out[b].Request.RequestedAt) })
	return out, nil
}
func (j *stampJournal) List(ctx context.Context) ([]workflow.ArrangementRequest, error) {
	es, err := j.Entries(ctx)
	var out []workflow.ArrangementRequest
	for _, e := range es {
		out = append(out, e.Request)
	}
	return out, err
}
func (j *stampJournal) BeginRelocation(ctx context.Context, from workflow.Scope, fn string, to workflow.Scope, tn string, id uuid.UUID) (workflow.RelocationIntent, error) {
	if err := j.op(ctx, "begin"); err != nil {
		return workflow.RelocationIntent{}, err
	}
	i := workflow.RelocationIntent{ID: uuid.NewString(), From: from, FromName: fn, To: to, ToName: tn, WorkflowID: id, CreatedAt: time.Now()}
	j.intents[i.ID] = i
	return i, nil
}
func (j *stampJournal) RelocationIntents(ctx context.Context) ([]workflow.RelocationIntent, error) {
	if err := j.op(ctx, "intents"); err != nil {
		return nil, err
	}
	var out []workflow.RelocationIntent
	for _, i := range j.intents {
		out = append(out, i)
	}
	return out, nil
}
func (j *stampJournal) RelocationIntentsAt(ctx context.Context, s workflow.Scope, n string) ([]workflow.RelocationIntent, error) {
	all, err := j.RelocationIntents(ctx)
	var out []workflow.RelocationIntent
	for _, i := range all {
		if i.From == s && i.FromName == n || i.To == s && i.ToName == n {
			out = append(out, i)
		}
	}
	return out, err
}
func (j *stampJournal) Relocate(ctx context.Context, from workflow.Scope, fn string, to workflow.Scope, tn string) error {
	if err := j.op(ctx, "relocate"); err != nil {
		return err
	}
	for k, e := range j.entries {
		if e.Request.Scope == from && e.Request.Name == fn {
			e.Request.Scope = to
			e.Request.Name = tn
			j.entries[k] = e
		}
	}
	return nil
}
func (j *stampJournal) EndRelocation(ctx context.Context, id string) error {
	if err := j.op(ctx, "end"); err != nil {
		return err
	}
	delete(j.intents, id)
	return nil
}

var _ workflow.RevisionJournal = (*stampJournal)(nil)

type stampRequester struct {
	journal  *stampJournal
	requests []workflow.ArrangementRequest
	err      error
}

func (r *stampRequester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error {
	*r.journal.events = append(*r.journal.events, "request")
	if r.journal.entries[req.Revision].State != workflow.JournalCommitted {
		return errors.New("request without receipt")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.err != nil {
		return r.err
	}
	r.requests = append(r.requests, cloneRequest(req))
	return nil
}
func (r *stampRequester) RelocateArrangements(ctx context.Context, from workflow.Scope, fn string, to workflow.Scope, tn string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.err != nil {
		return r.err
	}
	for i, req := range r.requests {
		if req.Scope == from && req.Name == fn {
			r.requests[i].Scope = to
			r.requests[i].Name = tn
		}
	}
	return nil
}

type stampSettler struct {
	events  *[]string
	targets []workflow.MutationTarget
	err     error
	settle  func(context.Context, workflow.Scope, string) error
}

func (s *stampSettler) Settle(ctx context.Context, scope workflow.Scope, name string) error {
	*s.events = append(*s.events, "settle")
	s.targets = append(s.targets, workflow.MutationTarget{Scope: scope, Name: name})
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.err != nil {
		return s.err
	}
	if s.settle != nil {
		return s.settle(ctx, scope, name)
	}
	return nil
}

type stampDisplayed struct {
	revision string
	value    workflow.RetainedLayout
	ok       bool
	err      error
}

func (d *stampDisplayed) DisplayedLayout(ctx context.Context, revision string) (workflow.RetainedLayout, bool, error) {
	if err := ctx.Err(); err != nil {
		return workflow.RetainedLayout{}, false, err
	}
	if revision != d.revision {
		return workflow.RetainedLayout{}, false, nil
	}
	return d.value, d.ok, d.err
}
func stampWorkflow() *workflow.Workflow {
	return &workflow.Workflow{Name: "flow", Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}}, Edges: []workflow.Edge{}}
}
func stamperFixture(t *testing.T) (*workflow.RevisionStamper, *stampJournal, *stampRequester, *stampSettler, *fakeRepo) {
	t.Helper()
	events := []string{}
	j := &stampJournal{entries: map[string]workflow.JournalEntry{}, intents: map[string]workflow.RelocationIntent{}, quarantined: map[string]string{}, events: &events, fail: map[string]error{}}
	r := &stampRequester{journal: j}
	settler := &stampSettler{events: &events}
	repo := newFakeRepo()
	s, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: j, Requester: r, Settler: settler, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	return s, j, r, settler, repo
}

func TestNewRevisionStamperRequiredPorts(t *testing.T) {
	_, j, r, settler, repo := stamperFixture(t)
	good := workflow.StamperDeps{Journal: j, Requester: r, Settler: settler}
	for _, port := range []string{"journal", "requester", "settler", "repository"} {
		t.Run(port, func(t *testing.T) {
			d := good
			var rp workflow.Repository = repo
			switch port {
			case "journal":
				d.Journal = nil
			case "requester":
				d.Requester = nil
			case "settler":
				d.Settler = nil
			case "repository":
				rp = nil
			}
			s, err := workflow.NewRevisionStamper(rp, d)
			assert.Nil(t, s)
			assert.ErrorIs(t, err, workflow.ErrStamperMisconfigured)
		})
	}
}

func TestRevisionStamperInvalidEnvelope(t *testing.T) {
	for _, field := range []string{"nil", "scope", "owner", "name", "reason", "token", "time"} {
		t.Run(field, func(t *testing.T) {
			_, j, r, settler, repo := stamperFixture(t)
			d := workflow.StamperDeps{Journal: j, Requester: r, Settler: settler}
			wf := stampWorkflow()
			scope := workflow.Shared()
			name := wf.Name
			reason := workflow.SaveReasonCreate
			switch field {
			case "nil":
				wf = nil
			case "scope":
				scope.Kind = 99
			case "owner":
				scope = workflow.Private(uuid.Nil)
			case "name":
				name = "../escape"
			case "reason":
				reason = "bogus"
			case "token":
				d.NewToken = func() string { return "not-a-token" }
			case "time":
				d.Now = func() time.Time { return time.Time{} }
			}
			s, err := workflow.NewRevisionStamper(repo, d)
			require.NoError(t, err)
			_, err = s.Stamp(context.Background(), scope, name, wf, nil, reason)
			assert.Error(t, err)
			assert.Empty(t, j.entries)
			assert.Empty(t, r.requests)
			if wf != nil {
				assert.Nil(t, wf.Presentation)
			}
		})
	}
}

// jsonValue compares repo formatting without discarding any saved fields.
func jsonValue(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestRevisionStamperStampOrdering(t *testing.T) {
	_, j, r, settler, repo := stamperFixture(t)
	token := uuid.Must(uuid.NewV7()).String()
	now := time.Date(2026, 10, 16, 3, 4, 5, 6, time.FixedZone("offset", 3600))
	d := workflow.StamperDeps{Journal: j, Requester: r, Settler: settler, Now: func() time.Time { return now }, NewToken: func() string { *j.events = append(*j.events, "token"); return token }}
	s, err := workflow.NewRevisionStamper(repo, d)
	require.NoError(t, err)
	wf := stampWorkflow()
	wf.ID = uuid.New()
	scope := workflow.Scope{Kind: workflow.ScopeShared, OwnerID: uuid.New()}
	req, err := s.Stamp(context.Background(), scope, wf.Name, wf, nil, workflow.SaveReasonSeed)
	require.NoError(t, err)
	assert.Equal(t, []string{"settle", "token", "record"}, *j.events)
	assert.Equal(t, []workflow.MutationTarget{{Scope: workflow.Shared(), Name: wf.Name}}, settler.targets)
	assert.Equal(t, workflow.Shared(), req.Scope)
	assert.Equal(t, uuid.Nil, wf.Presentation.OwnerID)
	assert.Equal(t, wf.ID, req.WorkflowID)
	assert.Equal(t, token, req.Revision)
	assert.True(t, now.Equal(req.RequestedAt))
	assert.Equal(t, workflow.JournalPrepared, j.entries[token].State)
	assert.JSONEq(t, jsonValue(t, wf), string(req.Manifest))
	assert.Empty(t, r.requests)
	before := jsonValue(t, wf)
	*j.events = nil
	settler.err = workflow.ErrMutationPending
	_, err = s.Stamp(context.Background(), workflow.Shared(), wf.Name, wf, wf, workflow.SaveReasonUpdate)
	assert.ErrorIs(t, err, workflow.ErrMutationPending)
	assert.Equal(t, []string{"settle"}, *j.events)
	assert.Equal(t, before, jsonValue(t, wf))
}

func TestRevisionStamperFallback(t *testing.T) {
	for _, source := range []string{"displayed", "retained", "prior", "input", "none", "reader-error", "empty-display", "filtered-display", "alias"} {
		t.Run(source, func(t *testing.T) {
			_, j, r, settler, repo := stamperFixture(t)
			oldToken := uuid.Must(uuid.NewV7()).String()
			prior := stampWorkflow()
			prior.Nodes[0].Position = &workflow.NodePosition{X: 30, Y: 31}
			prior.Presentation = &workflow.Presentation{Revision: oldToken, Retained: workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: "old-result", FromGeneration: 4, Positions: map[string]workflow.NodePosition{"a": {X: 20, Y: 21}, "deleted": {X: 999}}}}
			wf := stampWorkflow()
			wf.Nodes = append(wf.Nodes, workflow.Node{ID: "new"})
			wf.Nodes[0].Position = &workflow.NodePosition{X: 40, Y: 41}
			displayed := &stampDisplayed{revision: oldToken, ok: true, value: workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: oldToken, FromGeneration: 7, Positions: map[string]workflow.NodePosition{"a": {X: 10, Y: 11}, "deleted": {X: 999}}}}
			want := workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: oldToken, FromGeneration: 7, Positions: map[string]workflow.NodePosition{"a": {X: 10, Y: 11}}}
			switch source {
			case "retained", "reader-error", "empty-display", "filtered-display":
				displayed.ok = false
				want = workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: "old-result", FromGeneration: 4, Positions: map[string]workflow.NodePosition{"a": {X: 20, Y: 21}}}
				if source == "reader-error" {
					displayed.err = errors.New("offline")
				}
				if source == "empty-display" {
					displayed.ok = true
					displayed.value.Positions = nil
				}
				if source == "filtered-display" {
					displayed.ok = true
					displayed.value.Positions = map[string]workflow.NodePosition{"deleted": {X: 99}}
				}
			case "prior":
				displayed.ok = false
				prior.Presentation.Retained.Positions = nil
				want = workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"a": {X: 30, Y: 31}}}
			case "input":
				prior = nil
				want = workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"a": {X: 40, Y: 41}}}
			case "none":
				prior = nil
				wf.Nodes[0].Position = nil
				want = workflow.RetainedLayout{Source: workflow.RetainedNone}
			case "alias":
				wf = prior
			}
			s, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: j, Requester: r, Settler: settler, Displayed: displayed})
			require.NoError(t, err)
			req, err := s.Stamp(context.Background(), workflow.Shared(), wf.Name, wf, prior, workflow.SaveReasonUpdate)
			require.NoError(t, err)
			assert.Equal(t, want, req.Retained)
			assert.Equal(t, want, wf.Presentation.Retained)
			captured := cloneRequest(j.entries[req.Revision].Request)
			if displayed.value.Positions != nil {
				displayed.value.Positions["a"] = workflow.NodePosition{X: 3000}
			}
			if prior != nil && prior != wf {
				if prior.Presentation.Retained.Positions != nil {
					prior.Presentation.Retained.Positions["a"] = workflow.NodePosition{X: 4000}
				}
				prior.Nodes[0].Position.X = 5000
			}
			assert.Equal(t, want, req.Retained, "source mutation cannot change captured fallback")
			assert.Equal(t, want, wf.Presentation.Retained)
			if wf.Presentation.Retained.Positions != nil {
				wf.Presentation.Retained.Positions["a"] = workflow.NodePosition{X: 1000}
			}
			if req.Retained.Positions != nil {
				assert.Equal(t, want, req.Retained)
				req.Retained.Positions["a"] = workflow.NodePosition{X: 2000}
			}
			assert.Equal(t, captured, j.entries[req.Revision].Request, "prepared bytes and fallback are immutable")
		})
	}
}

func TestRevisionStamperStampFailureRestoresInput(t *testing.T) {
	for _, failure := range []string{"marshal", "journal", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			s, j, r, _, _ := stamperFixture(t)
			wf := stampWorkflow()
			wf.Presentation = &workflow.Presentation{Revision: "original"}
			old := wf.Presentation
			if failure == "marshal" {
				wf.Nodes[0].Config.OutputSchema = make(chan int)
			}
			if failure == "journal" {
				j.fail["record"] = errors.New("disk full")
			}
			ctx := context.Background()
			if failure == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			req, err := s.Stamp(ctx, workflow.Shared(), wf.Name, wf, wf, workflow.SaveReasonUpdate)
			assert.Error(t, err)
			assert.Empty(t, req.Revision)
			assert.Same(t, old, wf.Presentation)
			assert.Empty(t, j.entries)
			assert.Empty(t, r.requests)
		})
	}
}

func TestRevisionStamperCommitted(t *testing.T) {
	for _, failure := range []string{"", "receipt", "request", "remove", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			s, j, r, _, _ := stamperFixture(t)
			wf := stampWorkflow()
			wf.Nodes[0].Position = &workflow.NodePosition{X: 12, Y: 34}
			req, err := s.Stamp(context.Background(), workflow.Shared(), wf.Name, wf, nil, workflow.SaveReasonCreate)
			require.NoError(t, err)
			captured := cloneRequest(req)
			*j.events = nil
			ctx := context.Background()
			if failure == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if failure == "request" {
				r.err = errors.New("offline")
			} else if failure != "" && failure != "cancelled" {
				j.fail[failure] = errors.New("disk full")
			}
			s.Committed(ctx, req)
			switch failure {
			case "receipt", "cancelled":
				assert.Equal(t, []string{"receipt"}, *j.events)
				assert.Equal(t, workflow.JournalPrepared, j.entries[req.Revision].State)
				assert.Empty(t, r.requests)
			case "request":
				assert.Equal(t, []string{"receipt", "request"}, *j.events)
				assert.Equal(t, workflow.JournalCommitted, j.entries[req.Revision].State)
				assert.Empty(t, r.requests)
			default:
				assert.Equal(t, []string{"receipt", "request", "remove"}, *j.events)
				require.Len(t, r.requests, 1)
				assert.Equal(t, captured, r.requests[0])
			}
			if failure != "" {
				assert.Equal(t, captured, j.entries[req.Revision].Request)
				j.fail = map[string]error{}
				r.err = nil
				s.Committed(context.Background(), captured)
				assert.NotContains(t, j.entries, req.Revision)
				require.NotEmpty(t, r.requests)
				assert.Equal(t, captured, r.requests[len(r.requests)-1])
			} else {
				assert.Empty(t, j.entries)
			}
		})
	}
}

func TestRevisionStamperAbandon(t *testing.T) {
	s, j, r, _, _ := stamperFixture(t)
	wf := stampWorkflow()
	req, err := s.Stamp(context.Background(), workflow.Shared(), wf.Name, wf, nil, workflow.SaveReasonCreate)
	require.NoError(t, err)
	j.fail["remove"] = errors.New("cannot remove")
	s.Abandon(context.Background(), req)
	assert.Contains(t, j.entries, req.Revision)
	j.fail = map[string]error{}
	s.Abandon(context.Background(), req)
	assert.Empty(t, j.entries)
	assert.Empty(t, r.requests)
	*j.events = nil
	var absent *workflow.RevisionStamper
	absent.Committed(context.Background(), req)
	absent.Abandon(context.Background(), req)
	s.Committed(context.Background(), workflow.ArrangementRequest{})
	s.Abandon(context.Background(), workflow.ArrangementRequest{})
	assert.Empty(t, *j.events)
}

func TestRevisionStamperEnsureStamped(t *testing.T) {
	for _, state := range []string{"committed", "settled", "prepared", "foreign", "pre-feature", "missing", "changed", "wrong-input", "wrong-envelope", "entries-error", "settle-error", "load-error"} {
		t.Run(state, func(t *testing.T) {
			s, j, r, settler, repo := stamperFixture(t)
			wf := stampWorkflow()
			wf.Nodes[0].Position = &workflow.NodePosition{X: 12, Y: 34}
			req, err := s.Stamp(context.Background(), workflow.Shared(), wf.Name, wf, nil, workflow.SaveReasonSeed)
			require.NoError(t, err)
			repo.bucket(workflow.Shared())[wf.Name] = wf
			entry := j.entries[req.Revision]
			entry.State = workflow.JournalCommitted
			j.entries[req.Revision] = entry
			switch state {
			case "settled":
				entry.State = workflow.JournalPrepared
				j.entries[req.Revision] = entry
				settler.settle = func(ctx context.Context, scope workflow.Scope, name string) error {
					if scope != req.Scope || name != req.Name {
						return errors.New("wrong destination")
					}
					return j.MarkCommitted(ctx, req.Revision)
				}
			case "prepared":
				entry.State = workflow.JournalPrepared
				j.entries[req.Revision] = entry
			case "foreign":
				wf.Presentation.Scope = "private"
				wf.Presentation.OwnerID = uuid.New()
			case "pre-feature":
				wf.Presentation = nil
			case "missing":
				delete(repo.bucket(workflow.Shared()), wf.Name)
			case "changed":
				wf.Nodes[0].Label = "changed after prepare"
			case "wrong-input":
				entry.Request.Manifest = []byte(`{"name":"flow"}`)
				j.entries[req.Revision] = entry
			case "wrong-envelope":
				entry.Request.WorkflowID = uuid.New()
				j.entries[req.Revision] = entry
			case "entries-error":
				j.fail["entries"] = errors.New("offline")
			case "settle-error":
				settler.err = workflow.ErrMutationPending
			case "load-error":
				s, err = workflow.NewRevisionStamper(&stampReadErrorRepo{Repository: repo}, workflow.StamperDeps{Journal: j, Requester: r, Settler: settler})
				require.NoError(t, err)
			}
			before := jsonValue(t, wf)
			savedEntry := cloneRequest(j.entries[req.Revision].Request)
			*j.events = nil
			got, err := s.EnsureStamped(context.Background(), workflow.Shared(), req.Name, workflow.SaveReasonSeed)
			if state == "committed" || state == "settled" {
				require.NoError(t, err)
				require.Equal(t, req, got)
				assert.Equal(t, workflow.JournalCommitted, j.entries[req.Revision].State)
				// Returned maps and bytes must not alias recovery evidence.
				got.Manifest[0] = '!'
				got.Retained.Positions["a"] = workflow.NodePosition{X: 9999}
				assert.Equal(t, req.Retained, j.entries[req.Revision].Request.Retained)
				assert.Equal(t, req.Manifest, j.entries[req.Revision].Request.Manifest)
			} else {
				assert.Error(t, err)
				assert.Empty(t, got.Revision)
			}
			assert.NotContains(t, *j.events, "record", "resuming must not downgrade a committed receipt")
			assert.NotContains(t, *j.events, "request")
			assert.Equal(t, before, jsonValue(t, wf))
			assert.Equal(t, savedEntry, j.entries[req.Revision].Request)
		})
	}
}

type stampReadErrorRepo struct{ workflow.Repository }

func (r *stampReadErrorRepo) Load(ctx context.Context, scope workflow.Scope, name string) (*workflow.Workflow, error) {
	return nil, errors.New("read failed")
}

// The durable reader returns a captured acknowledgement, independently of the
// mutable repository. Removing this input must prevent journal reconstruction.
type acknowledgedStampRequester struct {
	*stampRequester
	input    workflow.ArrangementRequest
	revision string
	err      error
}

func (r *acknowledgedStampRequester) AcknowledgedArrangement(ctx context.Context, revision string) (workflow.ArrangementRequest, error) {
	if revision != r.revision {
		return workflow.ArrangementRequest{}, errors.New("unexpected acknowledged revision")
	}
	if err := ctx.Err(); err != nil {
		return workflow.ArrangementRequest{}, err
	}
	if r.err != nil {
		return workflow.ArrangementRequest{}, r.err
	}
	if r.input.Revision == "" {
		return workflow.ArrangementRequest{}, workflow.ErrNotFound
	}
	return cloneRequest(r.input), nil
}

func TestRevisionStamperAcknowledgedSeedResume(t *testing.T) {
	for _, boundary := range []string{"exact", "formatted-input", "equivalent-numbers", "rounded-integer", "rounded-fraction", "trailing-json", "missing", "empty-manifest", "manifest", "revision", "scope", "owner", "name", "identity", "nil-identity", "reason", "time", "retained", "deadline", "outage", "no-reader", "record", "receipt", "existing-prepare", "conflicting-entry", "non-seed"} {
		t.Run(boundary, func(t *testing.T) {
			s, j, requester, settler, repo := stamperFixture(t)
			wf := stampWorkflow()
			wf.ID = uuid.New()
			if boundary == "nil-identity" {
				wf.ID = uuid.Nil
			}
			wf.Nodes[0].Position = &workflow.NodePosition{X: 12, Y: 34}
			wf.Nodes[0].Config.OutputSchema = map[string]any{"limits": []any{json.Number("9007199254740992"), json.Number("0.5")}}
			reason := workflow.SaveReasonSeed
			if boundary == "non-seed" {
				reason = workflow.SaveReasonCreate
			}
			req, err := s.Stamp(context.Background(), workflow.Shared(), wf.Name, wf, nil, reason)
			require.NoError(t, err)
			repo.bucket(workflow.Shared())[wf.Name] = wf
			delete(j.entries, req.Revision)
			if boundary == "formatted-input" {
				req.Manifest, err = json.MarshalIndent(wf, "", "  ")
				require.NoError(t, err)
			}
			if boundary == "equivalent-numbers" {
				req.Manifest = bytes.ReplaceAll(req.Manifest, []byte("9007199254740992"), []byte("9.007199254740992e15"))
				req.Manifest = bytes.ReplaceAll(req.Manifest, []byte("0.5"), []byte("5e-1"))
			}
			reader := &acknowledgedStampRequester{stampRequester: requester, input: cloneRequest(req), revision: req.Revision}
			switch boundary {
			case "rounded-integer":
				reader.input.Manifest = bytes.ReplaceAll(reader.input.Manifest, []byte("9007199254740992"), []byte("9007199254740993"))
			case "rounded-fraction":
				reader.input.Manifest = bytes.ReplaceAll(reader.input.Manifest, []byte("0.5"), []byte("0.50000000000000001"))
			case "trailing-json":
				reader.input.Manifest = append(reader.input.Manifest, []byte(" {}")...)
			case "missing":
				reader.input = workflow.ArrangementRequest{}
			case "empty-manifest":
				reader.input.Manifest = nil
			case "manifest":
				reader.input.Manifest = []byte(`{"name":"flow","nodes":[]}`)
			case "revision":
				reader.input.Revision = uuid.Must(uuid.NewV7()).String()
			case "scope":
				reader.input.Scope = workflow.Private(uuid.New())
			case "owner":
				reader.input.Scope.OwnerID = uuid.New()
			case "name":
				reader.input.Name = "foreign"
			case "identity":
				reader.input.WorkflowID = uuid.New()
			case "reason":
				reader.input.Reason = workflow.SaveReasonUpdate
			case "time":
				reader.input.RequestedAt = reader.input.RequestedAt.Add(time.Second)
			case "retained":
				reader.input.Retained.Positions["a"] = workflow.NodePosition{X: 999}
			case "deadline":
				reader.err = context.DeadlineExceeded
			case "outage":
				reader.err = errors.New("acknowledgement store unavailable")
			case "record":
				j.fail["record"] = errors.New("disk full")
			case "receipt":
				j.fail["receipt"] = errors.New("receipt unavailable")
			case "existing-prepare":
				j.entries[req.Revision] = workflow.JournalEntry{Request: cloneRequest(req), State: workflow.JournalPrepared}
			case "conflicting-entry":
				conflict := cloneRequest(req)
				conflict.Manifest = []byte(`{"name":"conflicting input"}`)
				j.entries[req.Revision] = workflow.JournalEntry{Request: conflict, State: workflow.JournalCommitted}
			}
			var injected workflow.ArrangementRequester = reader
			if boundary == "no-reader" {
				injected = requester
			}
			s, err = workflow.NewRevisionStamper(repo, workflow.StamperDeps{
				Journal: j, Requester: injected, Settler: settler,
				NewToken: func() string { t.Fatal("resume must not mint a token"); return "" },
				Now:      func() time.Time { t.Fatal("resume must preserve saved time"); return time.Time{} },
			})
			require.NoError(t, err)
			before := jsonValue(t, wf)
			priorEntry := j.entries[req.Revision]
			*j.events = nil
			// Shared callers may carry an owner; the saved destination is canonical.
			scope := workflow.Shared()
			scope.OwnerID = uuid.New()
			got, err := s.EnsureStamped(context.Background(), scope, wf.Name, reason)
			if boundary == "exact" || boundary == "formatted-input" || boundary == "equivalent-numbers" {
				require.NoError(t, err)
				require.Equal(t, req, got)
				require.Equal(t, workflow.JournalCommitted, j.entries[req.Revision].State)
				require.Equal(t, req, j.entries[req.Revision].Request)
				got.Manifest[0] = '!'
				got.Retained.Positions["a"] = workflow.NodePosition{X: 9999}
				require.Equal(t, req, j.entries[req.Revision].Request)
				*j.events = nil
				again, err := s.EnsureStamped(context.Background(), scope, wf.Name, reason)
				require.NoError(t, err)
				require.Equal(t, req, again)
				require.Len(t, j.entries, 1)
				require.Equal(t, []string{"settle", "entries"}, *j.events, "an existing receipt must not be rewritten or downgraded")
			} else {
				require.Error(t, err)
				require.Empty(t, got.Revision)
				if reader.err != nil {
					require.ErrorIs(t, err, reader.err)
				}
				if failure := j.fail[boundary]; failure != nil {
					require.ErrorIs(t, err, failure)
				}
				if boundary == "receipt" || boundary == "existing-prepare" {
					require.Equal(t, workflow.JournalPrepared, j.entries[req.Revision].State)
					require.Equal(t, req, j.entries[req.Revision].Request)
				}
				if boundary == "conflicting-entry" {
					require.Equal(t, priorEntry, j.entries[req.Revision])
				}
				if boundary != "receipt" && boundary != "existing-prepare" && boundary != "conflicting-entry" {
					require.Empty(t, j.entries)
				}
				if boundary != "record" && boundary != "receipt" {
					require.NotContains(t, *j.events, "record", "rejected input must not change the journal")
					require.NotContains(t, *j.events, "receipt")
				}
			}
			require.Equal(t, before, jsonValue(t, wf))
			require.Empty(t, requester.requests, "resume cannot enqueue before publication authority is checked")
		})
	}
}
