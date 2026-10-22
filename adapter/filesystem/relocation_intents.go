package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Relocation intents are named relocations/<id>.<k(from)>.<k(to)>.json, where
// k is a fixed-length hash of the canonical endpoint. A destination finds the
// intents protecting it from filenames alone: unrelated bodies are never read,
// and a damaged body still identifies (and blocks) both of its endpoints.

const relocationKeyBytes = 16

func relocationKey(s workflow.Scope, n string) string {
	if s.Kind == workflow.ScopeShared {
		s.OwnerID = uuid.Nil
	}
	sum := sha256.Sum256([]byte(workflow.ScopeLabel(s) + "\x00" + s.OwnerID.String() + "\x00" + n))
	return hex.EncodeToString(sum[:relocationKeyBytes])
}

func relocationFilename(in workflow.RelocationIntent) string {
	return in.ID + "." + relocationKey(in.From, in.FromName) + "." + relocationKey(in.To, in.ToName) + ".json"
}

// relocationFile is a parsed intent filename.
type relocationFile struct{ name, id, from, to string }

func validRelocationKey(k string) bool {
	if len(k) != 2*relocationKeyBytes || strings.ToLower(k) != k {
		return false
	}
	_, err := hex.DecodeString(k)
	return err == nil
}

func (j *RevisionJournal) relocationDir() string { return filepath.Join(j.dir, "relocations") }

// relocationFiles lists intent files. Durable-write temporaries and operator
// directories (such as quarantine/) are ignored. Any other name cannot be
// scoped to endpoints, so it fails closed for every destination.
func (j *RevisionJournal) relocationFiles() ([]relocationFile, error) {
	files, err := os.ReadDir(j.relocationDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []relocationFile
	var errs []error
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || strings.HasPrefix(name, ".write-") && strings.HasSuffix(name, ".tmp") {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(name, ".json"), ".")
		if !strings.HasSuffix(name, ".json") || len(parts) != 3 || !validJournalID(parts[0]) || !validRelocationKey(parts[1]) || !validRelocationKey(parts[2]) {
			errs = append(errs, fmt.Errorf("filesystem: unscoped relocation record %q", name))
			continue
		}
		out = append(out, relocationFile{name: name, id: parts[0], from: parts[1], to: parts[2]})
	}
	return out, errors.Join(errs...)
}

// readRelocation parses one intent and checks it against its filename keys.
func (j *RevisionJournal) readRelocation(f relocationFile) (workflow.RelocationIntent, error) {
	var in workflow.RelocationIntent
	data, err := os.ReadFile(filepath.Join(j.relocationDir(), f.name))
	if err != nil {
		return in, err
	}
	if err = json.Unmarshal(data, &in); err != nil {
		return in, err
	}
	if in.ID != f.id || !canonicalDestination(in.From, in.FromName) || !canonicalDestination(in.To, in.ToName) || relocationFilename(in) != f.name || in.WorkflowID == uuid.Nil || in.CreatedAt.IsZero() {
		return in, fmt.Errorf("filesystem: invalid relocation intent")
	}
	return in, nil
}

func sortIntents(out []workflow.RelocationIntent) {
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].CreatedAt.Equal(out[b].CreatedAt) {
			return out[a].ID < out[b].ID
		}
		return out[a].CreatedAt.Before(out[b].CreatedAt)
	})
}

func (j *RevisionJournal) BeginRelocation(ctx context.Context, from workflow.Scope, fn string, to workflow.Scope, tn string, id uuid.UUID) (intent workflow.RelocationIntent, err error) {
	if !canonicalDestination(from, fn) || !canonicalDestination(to, tn) || from == to && fn == tn || id == uuid.Nil {
		return intent, fmt.Errorf("filesystem: invalid relocation")
	}
	intent = workflow.RelocationIntent{ID: uuid.NewString(), From: from, FromName: fn, To: to, ToName: tn, WorkflowID: id, CreatedAt: time.Now().UTC()}
	err = j.locked(ctx, func() error {
		if err := durableMkdir(j.relocationDir()); err != nil {
			return err
		}
		data, e := json.Marshal(intent)
		if e != nil {
			return e
		}
		return j.files.write(ctx, filepath.Join(j.relocationDir(), relocationFilename(intent)), data, 0600)
	})
	return
}

// RelocationIntents is the global recovery enumeration. An unreadable body is
// logged by the caller and left in place for operator repair; it is reported
// per record and never counts as settled.
func (j *RevisionJournal) RelocationIntents(ctx context.Context) (out []workflow.RelocationIntent, err error) {
	err = j.locked(ctx, func() error {
		files, unscoped := j.relocationFiles()
		errs := []error{unscoped}
		for _, f := range files {
			in, e := j.readRelocation(f)
			if e != nil {
				errs = append(errs, &workflow.DestinationError{Record: f.id, Err: fmt.Errorf("unreadable relocation intent %s: %w", f.name, e)})
				continue
			}
			out = append(out, in)
		}
		sortIntents(out)
		return errors.Join(errs...)
	})
	return
}

// RelocationIntentsAt matches filenames before parsing: intents that do not
// name (s, n) are never read, so their damage cannot affect this destination.
func (j *RevisionJournal) RelocationIntentsAt(ctx context.Context, s workflow.Scope, n string) (out []workflow.RelocationIntent, err error) {
	key := relocationKey(s, n)
	err = j.locked(ctx, func() error {
		files, unscoped := j.relocationFiles()
		if unscoped != nil {
			return unscoped
		}
		var errs []error
		for _, f := range files {
			if f.from != key && f.to != key {
				continue
			}
			in, e := j.readRelocation(f)
			if e != nil {
				errs = append(errs, &workflow.DestinationError{Targets: []workflow.MutationTarget{{Scope: s, Name: n}}, Record: f.id, Err: fmt.Errorf("%w: unreadable relocation intent %s: %v", workflow.ErrMutationPending, f.name, e)})
				continue
			}
			out = append(out, in)
		}
		sortIntents(out)
		return errors.Join(errs...)
	})
	return
}

func (j *RevisionJournal) EndRelocation(ctx context.Context, id string) error {
	if !validJournalID(id) {
		return fmt.Errorf("filesystem: invalid relocation ID")
	}
	return j.locked(ctx, func() error {
		// id is a validated UUID, so it carries no glob metacharacters.
		matches, err := filepath.Glob(filepath.Join(j.relocationDir(), id+".*.*.json"))
		if err != nil {
			return err
		}
		for _, path := range matches {
			if err := durableRemove(path); err != nil {
				return err
			}
		}
		return nil
	})
}
