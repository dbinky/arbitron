// Package spool is the runner's durable output spool: a per-job append log in
// front of the bounded runneragent.MemorySpool.
package spool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/runneragent"
)

type record struct {
	Op     string              `json:"op"` // "out" | "ack" | "res"
	Seq    uint64              `json:"seq,omitempty"`
	Token  string              `json:"tok,omitempty"`
	Result *runneragent.Result `json:"res,omitempty"`
}

// FileSpool implements runneragent.Spool with crash-durable terminal results.
type FileSpool struct {
	dir string
	mem *runneragent.MemorySpool
	mu  sync.Mutex
}

var _ runneragent.Spool = (*FileSpool)(nil)

// Open replays every job log under dir into a spool bounded by capacity.
func Open(dir string, capacity int) (*FileSpool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("spool: %w", err)
	}
	s := &FileSpool{dir: dir, mem: runneragent.NewMemorySpool(capacity)}
	logs, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		return nil, fmt.Errorf("spool: %w", err)
	}
	sort.Strings(logs)
	for _, p := range logs {
		if err := s.replay(p); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func checkRef(ref runneragent.JobRef) error {
	if _, err := uuid.Parse(ref.JobID); err != nil || ref.Generation < 1 {
		return fmt.Errorf("spool: invalid job reference %q/%d", ref.JobID, ref.Generation)
	}
	return nil
}

func (s *FileSpool) path(ref runneragent.JobRef) string {
	return filepath.Join(s.dir, fmt.Sprintf("%s.%d.log", ref.JobID, ref.Generation))
}

func parseName(p string) (runneragent.JobRef, bool) {
	base := strings.TrimSuffix(filepath.Base(p), ".log")
	i := strings.LastIndex(base, ".")
	if i < 0 {
		return runneragent.JobRef{}, false
	}
	gen, err := strconv.ParseInt(base[i+1:], 10, 64)
	ref := runneragent.JobRef{JobID: base[:i], Generation: gen}
	return ref, err == nil && checkRef(ref) == nil
}

func (s *FileSpool) replay(p string) error {
	ref, ok := parseName(p)
	if !ok {
		return nil // not ours
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	defer func() { _ = f.Close() }()
	ctx := context.Background()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			break // truncated tail from a crash mid-write
		}
		switch r.Op {
		case "out":
			if err := s.mem.AppendOutput(ctx, ref, runneragent.OutputFrame{Seq: r.Seq, Token: r.Token}); err != nil {
				return fmt.Errorf("spool: replay %s: %w (raise output_buffer_frames or clear the spool)", filepath.Base(p), err)
			}
		case "ack":
			_ = s.mem.Ack(ctx, ref, r.Seq)
		case "res":
			if r.Result != nil {
				_ = s.mem.PutResult(ctx, ref, *r.Result)
			}
		}
	}
	return nil
}

func (s *FileSpool) appendRecord(ref runneragent.JobRef, r record, durable bool) error {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	f, err := os.OpenFile(s.path(ref), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("spool: %w", err)
	}
	if durable {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return fmt.Errorf("spool: %w", err)
		}
	}
	return f.Close()
}

// AppendOutput spools f; ErrSpoolFull applies backpressure, nothing is dropped.
func (s *FileSpool) AppendOutput(ctx context.Context, ref runneragent.JobRef, f runneragent.OutputFrame) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mem.AppendOutput(ctx, ref, f); err != nil {
		return err
	}
	return s.appendRecord(ref, record{Op: "out", Seq: f.Seq, Token: f.Token}, false)
}

// PutResult durably retains the terminal result (fsync).
func (s *FileSpool) PutResult(ctx context.Context, ref runneragent.JobRef, r runneragent.Result) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mem.PutResult(ctx, ref, r); err != nil {
		return err
	}
	res := r
	return s.appendRecord(ref, record{Op: "res", Result: &res}, true)
}

// Ack drops frames up to seq.
func (s *FileSpool) Ack(ctx context.Context, ref runneragent.JobRef, seq uint64) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok, _ := s.mem.Get(ctx, ref); !ok {
		return nil
	}
	if err := s.mem.Ack(ctx, ref, seq); err != nil {
		return err
	}
	return s.appendRecord(ref, record{Op: "ack", Seq: seq}, false)
}

// AckResult forgets a job whose result the server durably accepted.
func (s *FileSpool) AckResult(ctx context.Context, ref runneragent.JobRef) error {
	return s.Drop(ctx, ref)
}

// Drop forgets the job and deletes its log.
func (s *FileSpool) Drop(ctx context.Context, ref runneragent.JobRef) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mem.Drop(ctx, ref); err != nil {
		return err
	}
	if err := os.Remove(s.path(ref)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("spool: %w", err)
	}
	return nil
}

// Get returns one job's spooled state.
func (s *FileSpool) Get(ctx context.Context, ref runneragent.JobRef) (runneragent.SpooledJob, bool, error) {
	return s.mem.Get(ctx, ref)
}

// Pending returns every spooled job.
func (s *FileSpool) Pending(ctx context.Context) ([]runneragent.SpooledJob, error) {
	return s.mem.Pending(ctx)
}
