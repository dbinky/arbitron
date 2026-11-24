package runneragent

import (
	"context"
	"errors"
	"sort"
)

type localJob struct {
	ref        JobRef
	cancel     context.CancelFunc
	done       chan struct{}
	seq        uint64
	sent       uint64
	terminal   bool
	resultSent bool
}

func (a *Agent) stopJobs() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, j := range a.jobs {
		j.cancel()
	}
}

func (a *Agent) held() []HeldJob {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []HeldJob
	for ref, j := range a.jobs {
		state := HeldRunning
		if j.terminal {
			state = HeldTerminal
		}
		out = append(out, HeldJob{JobID: ref.JobID, Generation: ref.Generation, State: state, LastOutputSeq: j.seq})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JobID < out[j].JobID })
	return out
}

func (a *Agent) onDispatch(ctx context.Context, s Session, in Inbound) error {
	ref := JobRef{JobID: in.JobID, Generation: in.Generation}
	a.mu.Lock()
	defer a.mu.Unlock()
	if ref.Generation <= a.fenced[ref.JobID] {
		return nil
	}
	if _, ok := a.jobs[ref]; ok {
		return s.Send(ctx, Outbound{Kind: KindAccept, JobID: ref.JobID, Generation: ref.Generation})
	}
	for old, j := range a.jobs {
		if old.JobID == ref.JobID {
			if old.Generation > ref.Generation {
				return nil
			}
			j.cancel()
			a.mu.Unlock()
			<-j.done
			a.mu.Lock()
			delete(a.jobs, old)
			if err := a.deps.Spool.Drop(ctx, old); err != nil {
				return err
			}
			close(a.wake)
			a.wake = make(chan struct{})
		}
	}
	d := *in.Dispatch
	d.JobID = ref.JobID
	d.Generation = ref.Generation
	if ok, reason := a.deps.Exec.Supports(d); !ok {
		return s.Send(ctx, Outbound{Kind: KindReject, JobID: ref.JobID, Generation: ref.Generation, RejectReason: reason})
	}
	jobCtx, cancel := context.WithCancel(a.runCtx)
	j := &localJob{ref: ref, cancel: cancel, done: make(chan struct{})}
	a.jobs[ref] = j
	if err := s.Send(ctx, Outbound{Kind: KindAccept, JobID: ref.JobID, Generation: ref.Generation}); err != nil {
		cancel()
		delete(a.jobs, ref)
		return err
	}
	go a.execute(jobCtx, j, d)
	return nil
}

func (a *Agent) execute(ctx context.Context, j *localJob, d Dispatch) {
	defer close(j.done)
	result := a.deps.Exec.Execute(ctx, d, func(ctx context.Context, token string) error {
		for {
			a.mu.Lock()
			if err := ctx.Err(); err != nil {
				a.mu.Unlock()
				return err
			}
			f := OutputFrame{Seq: j.seq + 1, Token: token}
			err := a.deps.Spool.AppendOutput(ctx, j.ref, f)
			if errors.Is(err, ErrSpoolFull) {
				wake := a.wake
				a.mu.Unlock()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-wake:
					continue
				}
			}
			if err != nil {
				a.mu.Unlock()
				return err
			}
			j.seq = f.Seq
			err = a.flush(ctx, j)
			a.mu.Unlock()
			return err
		}
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil || a.jobs[j.ref] != j {
		return
	}
	if err := a.deps.Spool.PutResult(ctx, j.ref, result); err != nil {
		return
	}
	j.terminal = true
	_ = a.flush(ctx, j)
}

// flush sends retained data once on the current connection. Transport failure
// leaves it in the spool, whose durable ACK is the only removal authority.
func (a *Agent) flush(ctx context.Context, j *localJob) error {
	if a.sessionConn == nil {
		return nil
	}
	sj, ok, err := a.deps.Spool.Get(ctx, j.ref)
	if err != nil || !ok {
		return err
	}
	for _, f := range sj.Frames {
		if f.Seq > j.sent {
			frame := f
			if err := a.sessionConn.Send(a.sessionCtx, Outbound{Kind: KindOutput, JobID: j.ref.JobID, Generation: j.ref.Generation, Seq: f.Seq, Output: &frame}); err != nil {
				return nil
			}
			j.sent = f.Seq
		}
	}
	if sj.Result != nil && !j.resultSent {
		if err := a.sessionConn.Send(a.sessionCtx, Outbound{Kind: KindResult, JobID: j.ref.JobID, Generation: j.ref.Generation, Result: sj.Result}); err == nil {
			j.resultSent = true
		}
	}
	return nil
}

// restore retains completed jobs across process restarts. A running process
// cannot survive a restart, so its incomplete spool is not advertised as held.
func (a *Agent) restore(ctx context.Context) error {
	pending, err := a.deps.Spool.Pending(ctx)
	if err != nil {
		return err
	}
	for _, sj := range pending {
		if sj.Result == nil {
			if err := a.deps.Spool.Drop(ctx, sj.Ref); err != nil {
				return err
			}
			continue
		}
		_, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		close(done)
		seq := sj.LastAcked
		for _, f := range sj.Frames {
			seq = max(seq, f.Seq)
		}
		a.jobs[sj.Ref] = &localJob{ref: sj.Ref, cancel: cancel, done: done, seq: seq, terminal: true}
	}
	return nil
}

func (a *Agent) ack(ctx context.Context, in Inbound) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ref := JobRef{JobID: in.JobID, Generation: in.Generation}
	j := a.jobs[ref]
	if j == nil {
		return nil
	}
	if in.AckResult {
		if !j.terminal {
			return nil
		}
		if err := a.deps.Spool.AckResult(ctx, ref); err != nil {
			return err
		}
		j.cancel()
		a.fenced[ref.JobID] = max(a.fenced[ref.JobID], ref.Generation)
		delete(a.jobs, ref)
		return nil
	}
	if err := a.deps.Spool.Ack(ctx, ref, in.Seq); err != nil {
		return err
	}
	close(a.wake)
	a.wake = make(chan struct{})
	return nil
}

func (a *Agent) fence(ctx context.Context, in Inbound) error {
	if in.JobID == "" || in.Generation <= 0 {
		return nil
	}
	ref := JobRef{JobID: in.JobID, Generation: in.Generation}
	a.mu.Lock()
	a.fenced[ref.JobID] = max(a.fenced[ref.JobID], ref.Generation)
	j := a.jobs[ref]
	if j != nil {
		j.cancel()
	}
	a.mu.Unlock()
	if j == nil {
		return nil
	}
	select {
	case <-j.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.deps.Spool.Drop(ctx, ref); err != nil {
		return err
	}
	delete(a.jobs, ref)
	close(a.wake)
	a.wake = make(chan struct{})
	return nil
}
