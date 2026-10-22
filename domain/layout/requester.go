package layout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

var ErrInvalidArrangementRequest = errors.New("layout: arrangement request needs a revision and a manifest")

// ErrConflictingArrangementInput refuses a second, different capture for an
// already acknowledged revision; the first stored bytes remain authoritative.
var ErrConflictingArrangementInput = errors.New("layout: revision already requested with a different manifest")

// Requester accepts immutable committed input; it never reloads a mutable workflow.
type Requester struct {
	store        Store
	attemptLimit int
	mu           sync.RWMutex
	notify       func(string)
}

func NewRequester(store Store, limit int, logger *slog.Logger) *Requester {
	if limit < 1 {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("layout attempt limit below 1; using 1", "attemptLimit", limit)
		limit = 1
	}
	return &Requester{store: store, attemptLimit: limit}
}
func (r *Requester) SetNotifier(fn func(string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notify = fn
}
func (r *Requester) notified(rev string) {
	r.mu.RLock()
	fn := r.notify
	r.mu.RUnlock()
	if fn != nil {
		fn(rev)
	}
}
func (r *Requester) AttemptLimit() int { return r.attemptLimit }
func validDestination(scope workflow.Scope, name string) bool {
	if scope.Kind != workflow.ScopePrivate && scope.Kind != workflow.ScopeShared {
		return false
	}
	if scope.Kind == workflow.ScopePrivate && scope.OwnerID == uuid.Nil || scope.Kind == workflow.ScopeShared && scope.OwnerID != uuid.Nil {
		return false
	}
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}
func (r *Requester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error {
	return r.requestArrangement(ctx, req, nil)
}

// AcknowledgedArrangement reads the saved request, regardless of job state. It
// neither queues work nor reconstructs input from the current workflow tree.
func (r *Requester) AcknowledgedArrangement(ctx context.Context, revision string) (workflow.ArrangementRequest, error) {
	row, err := r.store.Get(ctx, revision)
	if errors.Is(err, ErrRecordNotFound) {
		return workflow.ArrangementRequest{}, workflow.ErrNotFound
	}
	if err != nil {
		return workflow.ArrangementRequest{}, err
	}
	return workflow.ArrangementRequest{
		Revision: row.Revision, Scope: row.Destination.Scope, Name: row.Destination.Name,
		WorkflowID: row.Destination.WorkflowID, Reason: row.Reason, Manifest: row.Manifest,
		Retained: row.Retained, RequestedAt: row.RequestedAt,
	}, nil
}

func (r *Requester) requestArrangement(ctx context.Context, req workflow.ArrangementRequest, expected *Destination) error {
	id, err := uuid.Parse(req.Revision)
	if err != nil || id.Version() != 7 || id.Variant() != uuid.RFC4122 || id.String() != req.Revision || !validDestination(req.Scope, req.Name) || len(req.Manifest) == 0 {
		return ErrInvalidArrangementRequest
	}
	var wf workflow.Workflow
	if json.Unmarshal(req.Manifest, &wf) != nil || wf.SavedRevision() != req.Revision {
		return ErrInvalidArrangementRequest
	}
	row, _, err := r.store.Request(ctx, NewRequest{Revision: req.Revision, Destination: Destination{Scope: req.Scope, Name: req.Name, WorkflowID: req.WorkflowID}, Reason: req.Reason, Manifest: req.Manifest, Retained: req.Retained, AttemptLimit: r.attemptLimit, RequestedAt: req.RequestedAt})
	if err != nil {
		return err
	}
	if !sameManifest(row.Manifest, req.Manifest) {
		return ErrConflictingArrangementInput
	}
	if expected != nil && row.Destination != *expected {
		return ErrRevisionConflict
	}
	r.notified(req.Revision)
	return nil
}

// Destinations may relocate between replays; captured manifests never change.
// Numbers compare exactly: float64 decoding would equate distinct integers
// beyond 2^53, while 0.5 and 5e-1 remain the same input.
func sameManifest(a, b []byte) bool {
	av, aok := decodeExact(a)
	bv, bok := decodeExact(b)
	return aok && bok && sameJSON(av, bv)
}

func decodeExact(data []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	_, err := dec.Token()
	return v, err == io.EOF
}

func sameJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			y, ok := bv[k]
			if !ok || !sameJSON(x, y) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !sameJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	case json.Number:
		bv, ok := b.(json.Number)
		if !ok {
			return false
		}
		x, xok := new(big.Rat).SetString(string(av))
		y, yok := new(big.Rat).SetString(string(bv))
		return xok && yok && x.Cmp(y) == 0
	default:
		return a == b
	}
}
func (r *Requester) RelocateArrangements(ctx context.Context, from workflow.Scope, fromName string, to workflow.Scope, toName string) error {
	if !validDestination(from, fromName) || !validDestination(to, toName) {
		return ErrInvalidArrangementRequest
	}
	_, err := r.store.Relocate(ctx, Destination{Scope: from, Name: fromName}, Destination{Scope: to, Name: toName}, time.Now().UTC())
	return err
}

// passProblem is shared by read truth and actionable retry of rejected passes.
func passProblem(wf *workflow.Workflow, result StoredResult) string {
	g, err := BuildGraph(wf)
	if err != nil {
		return fmt.Sprintf("invalid captured graph: %v", err)
	}
	if !Compatible(result.Geometry, g) {
		return "stored geometry is incompatible with this revision"
	}
	if result.Verdict != VerdictPassed {
		return "stored result verdict does not support a pass"
	}
	if !HasRenderedEvidence(result.Geometry, result.Review) {
		return "stored result lacks matching rendered evidence"
	}
	if BlockingCount(result.UnresolvedFindings()) != 0 {
		return "stored result contains blocking findings"
	}
	return ""
}
func (r *Requester) Retry(ctx context.Context, rev string) (Record, error) {
	return r.retry(ctx, rev, nil)
}

// Workflow retries supply the destination authorised under the mutation lock.
// Historical retries follow the immutable revision even after it relocates.
func (r *Requester) retry(ctx context.Context, rev string, expected *Destination) (Record, error) {
	row, err := r.store.Get(ctx, rev)
	if err != nil {
		return Record{}, err
	}
	if expected != nil && row.Destination != *expected {
		return Record{}, ErrRevisionConflict
	}
	if row.State == StatePassed || row.State == StateSuperseded && row.Verdict == VerdictPassed {
		result, loadErr := r.store.Result(ctx, rev, row.ResultGeneration)
		if loadErr != nil && !errors.Is(loadErr, ErrRecordNotFound) {
			return Record{}, loadErr
		}
		problem := ""
		var wf workflow.Workflow
		switch {
		case loadErr != nil || row.ResultGeneration < 1:
			problem = "stored result is missing"
		case json.Unmarshal(row.Manifest, &wf) != nil:
			problem = "captured manifest is unreadable"
		case wf.SavedRevision() != rev:
			problem = "captured manifest revision does not match row"
		case result.Revision != rev || result.Generation != row.ResultGeneration:
			problem = "stored result identity does not match revision"
		case row.Verdict != VerdictPassed:
			problem = "stored verdict does not support a pass"
		default:
			problem = passProblem(&wf, result)
		}
		if problem == "" {
			return Record{}, ErrRetryConflict
		}
		if err = r.store.InvalidateResult(ctx, Fence{Revision: rev, Generation: row.JobGeneration}, row.ResultGeneration, problem, time.Now().UTC()); err != nil {
			return Record{}, err
		}
	}
	retried, err := r.store.Retry(ctx, rev, r.attemptLimit, time.Now().UTC())
	if err != nil {
		return Record{}, err
	}
	r.notified(rev)
	return retried, nil
}

var _ workflow.ArrangementRequester = (*Requester)(nil)

var _ workflow.AcknowledgedArrangementReader = (*Requester)(nil)
