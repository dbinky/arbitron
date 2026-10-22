package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

// RevisionJournal persists immutable save inputs separately from commit receipts.
// Its directory lock serializes receipt/relocation rewrites across processes.
type RevisionJournal struct {
	dir   string
	files durableFiles
}

func NewRevisionJournal(basePath string) *RevisionJournal {
	return &RevisionJournal{dir: filepath.Join(basePath, ".arbitron-layout-journal")}
}

var _ workflow.RevisionJournal = (*RevisionJournal)(nil)

func validJournalRevision(rev string) bool {
	id, err := uuid.Parse(rev)
	return err == nil && id.String() == rev && id.Version() == 7 && id.Variant() == uuid.RFC4122
}
func validJournalID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}
func canonicalDestination(s workflow.Scope, n string) bool {
	return (s.Kind == workflow.ScopeShared && s.OwnerID == uuid.Nil || s.Kind == workflow.ScopePrivate && s.OwnerID != uuid.Nil) && n != "" && n != "." && n != ".." && !strings.ContainsAny(n, "/\\\x00") && !internalArtifact(n)
}
func validateJournalRequest(r workflow.ArrangementRequest) error {
	if !validJournalRevision(r.Revision) || !canonicalDestination(r.Scope, r.Name) || r.RequestedAt.IsZero() {
		return fmt.Errorf("filesystem: invalid journal envelope")
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(r.Manifest, &wf); err != nil {
		return err
	}
	p := wf.Presentation
	if p == nil || p.Revision != r.Revision || !p.SavedAt.Equal(r.RequestedAt) || p.Reason != r.Reason || wf.ID != r.WorkflowID || !reflect.DeepEqual(p.Retained, r.Retained) || !canonicalDestination(workflow.ScopeFromPresentation(p), p.Name) {
		return fmt.Errorf("filesystem: inconsistent journal manifest")
	}
	switch r.Reason {
	case workflow.SaveReasonCreate, workflow.SaveReasonUpdate, workflow.SaveReasonCopy, workflow.SaveReasonDuplicate, workflow.SaveReasonPublish, workflow.SaveReasonRename, workflow.SaveReasonSeed:
	default:
		return fmt.Errorf("filesystem: invalid save reason")
	}
	return nil
}
func immutableJournal(r workflow.ArrangementRequest) workflow.ArrangementRequest {
	r.Scope = workflow.Scope{}
	r.Name = ""
	return r
}
func requestDigest(r workflow.ArrangementRequest) string {
	data, _ := json.Marshal(immutableJournal(r))
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type commitReceipt struct {
	Revision string
	Digest   string
}

func (j *RevisionJournal) locked(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := durableMkdir(j.dir); err != nil {
		return err
	}
	f := flock.New(filepath.Join(j.dir, "journal.lock"))
	defer f.Close()
	ok, err := f.TryLockContext(ctx, lockPollInterval)
	if err != nil {
		return err
	}
	if !ok {
		return ctx.Err()
	}
	defer f.Unlock()
	return fn()
}
func (j *RevisionJournal) read(rev string) (workflow.ArrangementRequest, error) {
	var r workflow.ArrangementRequest
	data, err := os.ReadFile(filepath.Join(j.dir, rev+".json"))
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Revision != rev {
		return r, fmt.Errorf("filesystem: journal filename mismatch")
	}
	return r, validateJournalRequest(r)
}
func (j *RevisionJournal) Record(ctx context.Context, r workflow.ArrangementRequest) error {
	if err := validateJournalRequest(r); err != nil {
		return err
	}
	return j.locked(ctx, func() error {
		old, err := j.read(r.Revision)
		if err == nil {
			if requestDigest(old) != requestDigest(r) {
				return fmt.Errorf("filesystem: conflicting immutable journal input")
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		if _, err := os.Stat(filepath.Join(j.dir, r.Revision+".committed")); err == nil {
			return fmt.Errorf("filesystem: orphan commit receipt")
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return j.files.write(ctx, filepath.Join(j.dir, r.Revision+".json"), data, 0600)
	})
}
func (j *RevisionJournal) MarkCommitted(ctx context.Context, rev string) error {
	if !validJournalRevision(rev) {
		return fmt.Errorf("filesystem: invalid journal revision")
	}
	return j.locked(ctx, func() error {
		r, err := j.read(rev)
		if err != nil {
			return err
		}
		expected := commitReceipt{rev, requestDigest(r)}
		path := filepath.Join(j.dir, rev+".committed")
		data, err := os.ReadFile(path)
		if err == nil {
			var got commitReceipt
			if json.Unmarshal(data, &got) != nil || got != expected {
				return fmt.Errorf("filesystem: inconsistent receipt")
			}
			// An earlier write may have renamed the receipt but failed its sync.
			return j.files.syncDir(j.dir)
		}
		if !os.IsNotExist(err) {
			return err
		}
		data, err = json.Marshal(expected)
		if err != nil {
			return err
		}
		return j.files.write(ctx, path, data, 0600)
	})
}
func (j *RevisionJournal) Remove(ctx context.Context, rev string) error {
	if !validJournalRevision(rev) {
		return fmt.Errorf("filesystem: invalid journal revision")
	}
	return j.locked(ctx, func() error {
		// Remove receipt first: an interrupted removal still has input to settle.
		if err := durableRemove(filepath.Join(j.dir, rev+".committed")); err != nil {
			return err
		}
		return durableRemove(filepath.Join(j.dir, rev+".json"))
	})
}
func (j *RevisionJournal) quarantine(ctx context.Context, rev, reason string) error {
	dir := filepath.Join(j.dir, "quarantine", rev)
	if err := durableMkdir(dir); err != nil {
		return err
	}
	if err := j.files.write(ctx, filepath.Join(dir, "reason.txt"), []byte(reason), 0600); err != nil {
		return err
	}
	for _, suffix := range []string{".json", ".committed"} {
		src, dst := filepath.Join(j.dir, rev+suffix), filepath.Join(dir, rev+suffix)
		if err := os.Rename(src, dst); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := syncDir(dir); err != nil {
			return err
		}
		if err := syncDir(j.dir); err != nil {
			return err
		}
	}
	return nil
}
func (j *RevisionJournal) Quarantine(ctx context.Context, rev, reason string) error {
	if !validJournalRevision(rev) {
		return fmt.Errorf("filesystem: invalid journal revision")
	}
	return j.locked(ctx, func() error { return j.quarantine(ctx, rev, reason) })
}
func (j *RevisionJournal) entries(ctx context.Context) ([]workflow.JournalEntry, error) {
	files, err := os.ReadDir(j.dir)
	if err != nil {
		return nil, err
	}
	revisions := map[string]bool{}
	hasReceipts := false
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		for _, suffix := range []string{".json", ".committed"} {
			if strings.HasSuffix(f.Name(), suffix) {
				revisions[strings.TrimSuffix(f.Name(), suffix)] = true
				hasReceipts = hasReceipts || suffix == ".committed"
			}
		}
	}
	// A receipt can be visible after its writer's directory sync failed. Under
	// the journal lock, persist those renames before exposing any commit proof.
	if hasReceipts {
		if err := j.files.syncDir(j.dir); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(revisions))
	for rev := range revisions {
		keys = append(keys, rev)
	}
	sort.Strings(keys)
	var out []workflow.JournalEntry
	var errs []error
	for _, rev := range keys {
		if err := ctx.Err(); err != nil {
			return out, errors.Join(append(errs, err)...)
		}
		if !validJournalRevision(rev) {
			errs = append(errs, fmt.Errorf("filesystem: invalid journal filename %q", rev))
			continue
		}
		r, e := j.read(rev)
		state := workflow.JournalPrepared
		if e == nil {
			data, readErr := os.ReadFile(filepath.Join(j.dir, rev+".committed"))
			if readErr == nil {
				var receipt commitReceipt
				if json.Unmarshal(data, &receipt) != nil || receipt != (commitReceipt{rev, requestDigest(r)}) {
					e = fmt.Errorf("inconsistent receipt")
				} else {
					state = workflow.JournalCommitted
				}
			} else if !os.IsNotExist(readErr) {
				e = readErr
			}
		}
		if e != nil {
			// Confined to this record; an unparseable filename above fails closed.
			errs = append(errs, &workflow.DestinationError{Record: rev, Err: fmt.Errorf("journal %s: %w", rev, e)})
			// I/O availability is undecided, not corruption. Preserve it for retry.
			var pathErr *os.PathError
			if !errors.As(e, &pathErr) || os.IsNotExist(e) {
				if q := j.quarantine(ctx, rev, e.Error()); q != nil {
					errs = append(errs, q)
				}
			}
			continue
		}
		out = append(out, workflow.JournalEntry{Request: r, State: state})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Request.RequestedAt.Equal(out[b].Request.RequestedAt) {
			return out[a].Request.Revision < out[b].Request.Revision
		}
		return out[a].Request.RequestedAt.Before(out[b].Request.RequestedAt)
	})
	return out, errors.Join(errs...)
}
func (j *RevisionJournal) Entries(ctx context.Context) (out []workflow.JournalEntry, err error) {
	err = j.locked(ctx, func() error { var e error; out, e = j.entries(ctx); return e })
	return
}
func (j *RevisionJournal) List(ctx context.Context) ([]workflow.ArrangementRequest, error) {
	entries, err := j.Entries(ctx)
	out := make([]workflow.ArrangementRequest, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Request)
	}
	return out, err
}
func (j *RevisionJournal) Relocate(ctx context.Context, from workflow.Scope, fn string, to workflow.Scope, tn string) error {
	if !canonicalDestination(from, fn) || !canonicalDestination(to, tn) {
		return fmt.Errorf("filesystem: invalid relocation")
	}
	return j.locked(ctx, func() error {
		entries, err := j.entries(ctx)
		if err != nil {
			return err
		}
		for _, e := range entries {
			r := e.Request
			if r.Scope != from || r.Name != fn {
				continue
			}
			r.Scope, r.Name = to, tn
			data, err := json.Marshal(r)
			if err != nil {
				return err
			}
			if err = j.files.write(ctx, filepath.Join(j.dir, r.Revision+".json"), data, 0600); err != nil {
				return err
			}
		}
		return nil
	})
}
