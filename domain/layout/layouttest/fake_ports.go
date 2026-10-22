package layouttest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

type FakeManifests struct {
	mu        sync.Mutex
	revisions map[string]string
	err       error
	calls     int
}

func NewFakeManifests() *FakeManifests { return &FakeManifests{revisions: map[string]string{}} }
func (f *FakeManifests) Set(s workflow.Scope, name, rev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revisions[layout.LocatorKey(s, name)] = rev
}
func (f *FakeManifests) Remove(s workflow.Scope, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.revisions, layout.LocatorKey(s, name))
}
func (f *FakeManifests) SetErr(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.err = err }
func (f *FakeManifests) Calls() int       { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }
func (f *FakeManifests) CurrentRevision(ctx context.Context, s workflow.Scope, name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if f.err != nil {
		return "", false, f.err
	}
	r, ok := f.revisions[layout.LocatorKey(s, name)]
	return r, ok && r != "", nil
}

type quarantinedEntry struct {
	entry  workflow.JournalEntry
	reason string
}
type FakeJournal struct {
	mu         sync.Mutex
	entries    map[string]workflow.JournalEntry
	quarantine map[string]quarantinedEntry
	intents    map[string]workflow.RelocationIntent
	err        error
}

func NewFakeJournal(entries ...workflow.ArrangementRequest) *FakeJournal {
	f := &FakeJournal{entries: map[string]workflow.JournalEntry{}, quarantine: map[string]quarantinedEntry{}, intents: map[string]workflow.RelocationIntent{}}
	for _, req := range entries {
		f.entries[req.Revision] = workflow.JournalEntry{Request: copyRequest(req), State: workflow.JournalPrepared}
	}
	return f
}
func (f *FakeJournal) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.err
}
func (f *FakeJournal) SetErr(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.err = err }
func (f *FakeJournal) Has(rev string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.entries[rev]
	return ok
}
func (f *FakeJournal) Len() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.entries) }
func (f *FakeJournal) Record(ctx context.Context, req workflow.ArrangementRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx); err != nil {
		return err
	}
	// Mirror the durable journal: an identical replay keeps the recorded entry
	// (and its receipt); different captured input for the revision is refused.
	if old, ok := f.entries[req.Revision]; ok {
		if !sameImmutableInput(old.Request, req) {
			return fmt.Errorf("journal: conflicting immutable input for %s", req.Revision)
		}
		return nil
	}
	f.entries[req.Revision] = workflow.JournalEntry{Request: copyRequest(req), State: workflow.JournalPrepared}
	return nil
}

// sameImmutableInput is the durable journal's identity rule: everything but
// Scope and Name, which relocation changes without changing captured input.
func sameImmutableInput(a, b workflow.ArrangementRequest) bool {
	immutable := func(r workflow.ArrangementRequest) []byte {
		r.Scope, r.Name = workflow.Scope{}, ""
		data, err := json.Marshal(r)
		if err != nil {
			panic(err)
		}
		return data
	}
	return bytes.Equal(immutable(a), immutable(b))
}
func (f *FakeJournal) MarkCommitted(ctx context.Context, rev string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx); err != nil {
		return err
	}
	e, ok := f.entries[rev]
	if !ok {
		return fmt.Errorf("journal: unknown revision %s", rev)
	}
	e.State = workflow.JournalCommitted
	f.entries[rev] = e
	return nil
}
func (f *FakeJournal) Remove(ctx context.Context, rev string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx); err != nil {
		return err
	}
	delete(f.entries, rev)
	return nil
}
func (f *FakeJournal) Quarantine(ctx context.Context, rev, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx); err != nil {
		return err
	}
	if e, ok := f.entries[rev]; ok {
		f.quarantine[rev] = quarantinedEntry{entry: e, reason: reason}
		delete(f.entries, rev)
	}
	return nil
}
func (f *FakeJournal) Entries(ctx context.Context) ([]workflow.JournalEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	out := make([]workflow.JournalEntry, 0, len(f.entries))
	for _, e := range f.entries {
		e.Request = copyRequest(e.Request)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return requestLess(out[i].Request, out[j].Request) })
	return out, nil
}
func (f *FakeJournal) List(ctx context.Context) ([]workflow.ArrangementRequest, error) {
	entries, err := f.Entries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]workflow.ArrangementRequest, len(entries))
	for i, e := range entries {
		out[i] = e.Request
	}
	return out, nil
}
func (f *FakeJournal) BeginRelocation(ctx context.Context, from workflow.Scope, fromName string, to workflow.Scope, toName string, id uuid.UUID) (workflow.RelocationIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx); err != nil {
		return workflow.RelocationIntent{}, err
	}
	intent := workflow.RelocationIntent{ID: uuid.NewString(), From: from, FromName: fromName, To: to, ToName: toName, WorkflowID: id, CreatedAt: time.Now().UTC()}
	f.intents[intent.ID] = intent
	return intent, nil
}
func (f *FakeJournal) RelocationIntents(ctx context.Context) ([]workflow.RelocationIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	out := make([]workflow.RelocationIntent, 0, len(f.intents))
	for _, i := range f.intents {
		out = append(out, i)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}
func (f *FakeJournal) RelocationIntentsAt(ctx context.Context, s workflow.Scope, name string) ([]workflow.RelocationIntent, error) {
	all, err := f.RelocationIntents(ctx)
	if err != nil {
		return nil, err
	}
	key := layout.LocatorKey(s, name)
	var out []workflow.RelocationIntent
	for _, in := range all {
		if layout.LocatorKey(in.From, in.FromName) == key || layout.LocatorKey(in.To, in.ToName) == key {
			out = append(out, in)
		}
	}
	return out, nil
}
func (f *FakeJournal) Relocate(ctx context.Context, from workflow.Scope, fromName string, to workflow.Scope, toName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx); err != nil {
		return err
	}
	if to.Kind == workflow.ScopeShared {
		to = workflow.Shared()
	}
	for rev, e := range f.entries {
		if layout.LocatorKey(e.Request.Scope, e.Request.Name) == layout.LocatorKey(from, fromName) {
			e.Request.Scope = to
			e.Request.Name = toName
			f.entries[rev] = e
		}
	}
	return nil
}
func (f *FakeJournal) EndRelocation(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(ctx); err != nil {
		return err
	}
	delete(f.intents, id)
	return nil
}

var _ workflow.RevisionJournal = (*FakeJournal)(nil)

type FakeCache struct {
	mu                  sync.Mutex
	current             map[string]layout.CacheEntry
	published           map[string][]layout.CacheEntry
	publishErr, readErr error
	calls               int
}

func NewFakeCache() *FakeCache {
	return &FakeCache{current: map[string]layout.CacheEntry{}, published: map[string][]layout.CacheEntry{}}
}
func (f *FakeCache) SetPublishErr(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.publishErr = err }
func (f *FakeCache) SetReadErr(err error)    { f.mu.Lock(); defer f.mu.Unlock(); f.readErr = err }
func (f *FakeCache) Seed(s workflow.Scope, name string, e layout.CacheEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current[layout.LocatorKey(s, name)] = copyCache(e)
}
func (f *FakeCache) Published(dest layout.Destination) []layout.CacheEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]layout.CacheEntry, len(f.published[dest.Key()]))
	for i, e := range f.published[dest.Key()] {
		out[i] = copyCache(e)
	}
	return out
}
func (f *FakeCache) PublishCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }
func (f *FakeCache) PublishCache(ctx context.Context, dest layout.Destination, e layout.CacheEntry, fence func(context.Context) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.publishErr != nil {
		return f.publishErr
	}
	if fence == nil {
		return fmt.Errorf("cache: fence is required")
	}
	if err := fence(ctx); err != nil {
		return err
	}
	f.current[dest.Key()] = copyCache(e)
	f.published[dest.Key()] = append(f.published[dest.Key()], copyCache(e))
	return nil
}
func (f *FakeCache) ReadCache(ctx context.Context, s workflow.Scope, name string) (layout.CacheEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return layout.CacheEntry{}, false, err
	}
	if f.readErr != nil {
		return layout.CacheEntry{}, false, f.readErr
	}
	e, ok := f.current[layout.LocatorKey(s, name)]
	return copyCache(e), ok, nil
}

var _ layout.CacheReader = (*FakeCache)(nil)
var _ layout.CachePublisher = (*FakeCache)(nil)
