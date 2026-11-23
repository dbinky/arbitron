package runneragent

import (
	"context"
	"sort"
	"sync"

	"github.com/arbitron/arbitron/domain/runner"
)

// MemorySpool is a bounded in-memory Spool. It satisfies the same
// conformance suite as the file spool; tests and the P2 T10 contract test use
// it, production uses adapter/runneragent/spool.FileSpool.
type MemorySpool struct {
	mu       sync.Mutex
	capacity int
	frames   int
	jobs     map[JobRef]*SpooledJob
}

// NewMemorySpool returns an empty spool holding at most capacity unacked frames.
func NewMemorySpool(capacity int) *MemorySpool {
	if capacity < 1 {
		capacity = 1
	}
	return &MemorySpool{capacity: capacity, jobs: map[JobRef]*SpooledJob{}}
}

func (m *MemorySpool) job(ref JobRef) *SpooledJob {
	j := m.jobs[ref]
	if j == nil {
		j = &SpooledJob{Ref: ref}
		m.jobs[ref] = j
	}
	return j
}

func lastSeq(j *SpooledJob) uint64 {
	if n := len(j.Frames); n > 0 {
		return j.Frames[n-1].Seq
	}
	return j.LastAcked
}

// AppendOutput stores f when there is room and f.Seq is contiguous.
func (m *MemorySpool) AppendOutput(_ context.Context, ref JobRef, f OutputFrame) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[ref]
	last := uint64(0)
	if j != nil {
		last = lastSeq(j)
	}
	if f.Seq != last+1 {
		return ErrSpoolSequence
	}
	if m.frames >= m.capacity {
		return ErrSpoolFull
	}
	j = m.job(ref)
	j.Frames = append(j.Frames, f)
	m.frames++
	return nil
}

// PutResult retains the terminal result.
func (m *MemorySpool) PutResult(_ context.Context, ref JobRef, r Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.job(ref).Result = cloneResult(&r)
	return nil
}

// Ack drops frames with Seq <= seq; lower acks are ignored.
func (m *MemorySpool) Ack(_ context.Context, ref JobRef, seq uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[ref]
	if j == nil || seq <= j.LastAcked {
		return nil
	}
	kept := j.Frames[:0]
	for _, f := range j.Frames {
		if f.Seq <= seq {
			m.frames--
			continue
		}
		kept = append(kept, f)
	}
	j.Frames = kept
	j.LastAcked = seq
	return nil
}

// AckResult forgets the job after the server durably accepted its result.
func (m *MemorySpool) AckResult(ctx context.Context, ref JobRef) error { return m.Drop(ctx, ref) }

// Drop forgets the job (fenced or acknowledged).
func (m *MemorySpool) Drop(_ context.Context, ref JobRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.jobs[ref]; j != nil {
		m.frames -= len(j.Frames)
		delete(m.jobs, ref)
	}
	return nil
}

func cloneJob(j *SpooledJob) SpooledJob {
	out := SpooledJob{Ref: j.Ref, LastAcked: j.LastAcked, Frames: append([]OutputFrame(nil), j.Frames...)}
	if j.Result != nil {
		out.Result = cloneResult(j.Result)
	}
	return out
}

func cloneResult(r *Result) *Result {
	out := *r
	if r.Chat != nil {
		chat := *r.Chat
		out.Chat = &chat
	}
	out.Outcome.ResetAt = clonePointer(r.Outcome.ResetAt)
	out.Outcome.HeadroomHint = clonePointer(r.Outcome.HeadroomHint)
	out.Outcome.Windows = append([]runner.LimitWindow(nil), r.Outcome.Windows...)
	for i := range out.Outcome.Windows {
		out.Outcome.Windows[i].UsedPercent = clonePointer(r.Outcome.Windows[i].UsedPercent)
		out.Outcome.Windows[i].ResetAt = clonePointer(r.Outcome.Windows[i].ResetAt)
	}
	return &out
}

func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}

// Get returns a copy of one job's state.
func (m *MemorySpool) Get(_ context.Context, ref JobRef) (SpooledJob, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[ref]
	if j == nil {
		return SpooledJob{}, false, nil
	}
	return cloneJob(j), true, nil
}

// Pending returns copies of every retained job, sorted.
func (m *MemorySpool) Pending(_ context.Context) ([]SpooledJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SpooledJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, cloneJob(j))
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Ref.JobID != out[k].Ref.JobID {
			return out[i].Ref.JobID < out[k].Ref.JobID
		}
		return out[i].Ref.Generation < out[k].Ref.Generation
	})
	return out, nil
}
