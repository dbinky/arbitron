package layouttest

import (
	"context"
	"sort"
	"sync"

	"github.com/arbitron/arbitron/domain/workflow"
)

// FakeCommittedRevisions keeps preparation distinct from proof, including history.
type FakeCommittedRevisions struct {
	mu      sync.Mutex
	entries map[string]workflow.JournalEntry
	err     error
}

func NewFakeCommittedRevisions() *FakeCommittedRevisions {
	return &FakeCommittedRevisions{entries: map[string]workflow.JournalEntry{}}
}
func (f *FakeCommittedRevisions) Prepare(req workflow.ArrangementRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[req.Revision] = workflow.JournalEntry{Request: copyRequest(req), State: workflow.JournalPrepared}
}
func (f *FakeCommittedRevisions) Commit(rev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.entries[rev]; ok {
		e.State = workflow.JournalCommitted
		f.entries[rev] = e
	}
}
func (f *FakeCommittedRevisions) Abandon(rev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, rev)
}
func (f *FakeCommittedRevisions) SetError(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.err = err }
func (f *FakeCommittedRevisions) CommittedRequests(ctx context.Context) ([]workflow.ArrangementRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []workflow.ArrangementRequest{}
	for _, e := range f.entries {
		if e.State == workflow.JournalCommitted {
			out = append(out, copyRequest(e.Request))
		}
	}
	sort.Slice(out, func(i, j int) bool { return requestLess(out[i], out[j]) })
	return out, f.err
}
func requestLess(a, b workflow.ArrangementRequest) bool {
	if a.RequestedAt.Equal(b.RequestedAt) {
		return a.Revision < b.Revision
	}
	return a.RequestedAt.Before(b.RequestedAt)
}

var _ workflow.CommittedRevisionSource = (*FakeCommittedRevisions)(nil)
