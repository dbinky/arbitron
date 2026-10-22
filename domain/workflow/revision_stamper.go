package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StamperDeps supplies save-side ports; Displayed alone is optional.
type StamperDeps struct {
	Journal   RevisionJournal
	Requester ArrangementRequester
	Displayed DisplayedLayoutReader
	Settler   DestinationSettler
	NewToken  func() string
	Now       func() time.Time
	Logger    *slog.Logger
}

// RevisionStamper prepares immutable revisions and acknowledges committed saves.
// It never writes a workflow or acquires mutation locks itself.
type RevisionStamper struct {
	repo Repository
	deps StamperDeps
}

// NewRevisionStamper requires real journal, requester and settlement ports.
func NewRevisionStamper(repo Repository, deps StamperDeps) (*RevisionStamper, error) {
	if repo == nil || deps.Journal == nil || deps.Requester == nil || deps.Settler == nil {
		return nil, ErrStamperMisconfigured
	}
	if deps.NewToken == nil {
		deps.NewToken = func() string { return uuid.Must(uuid.NewV7()).String() }
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &RevisionStamper{repo: repo, deps: deps}, nil
}

func validateStampDestination(scope Scope, name string, reason SaveReason) error {
	if scope.Kind != ScopePrivate && scope.Kind != ScopeShared || scope.Kind == ScopePrivate && scope.OwnerID == uuid.Nil {
		return fmt.Errorf("workflow: invalid revision scope")
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\\\x00") {
		return ErrInvalidName
	}
	switch reason {
	case SaveReasonCreate, SaveReasonUpdate, SaveReasonPublish, SaveReasonCopy, SaveReasonDuplicate, SaveReasonRename, SaveReasonSeed:
		return nil
	}
	return fmt.Errorf("workflow: invalid save reason %q", reason)
}

func validRevision(revision string) bool {
	id, err := uuid.Parse(revision)
	return err == nil && id.Version() == 7 && id.Variant() == uuid.RFC4122 && id.String() == revision
}

func manifestPositions(wf *Workflow) RetainedLayout {
	out := RetainedLayout{Source: RetainedNone}
	if wf != nil {
		for _, n := range wf.Nodes {
			if n.Position != nil {
				if out.Positions == nil {
					out.Positions = map[string]NodePosition{}
				}
				out.Positions[n.ID] = *n.Position
			}
		}
	}
	if len(out.Positions) > 0 {
		out.Source = RetainedFromManifest
	}
	return out
}

func cloneArrangementRequest(r ArrangementRequest) ArrangementRequest {
	r.Manifest = append([]byte(nil), r.Manifest...)
	r.Retained = cloneRetained(r.Retained)
	return r
}

func cloneRetained(r RetainedLayout) RetainedLayout {
	if r.Positions != nil {
		positions := make(map[string]NodePosition, len(r.Positions))
		for id, p := range r.Positions {
			positions[id] = p
		}
		r.Positions = positions
	}
	return r
}

func filterRetained(r RetainedLayout, wf *Workflow) RetainedLayout {
	positions := map[string]NodePosition{}
	for _, n := range wf.Nodes {
		if p, ok := r.Positions[n.ID]; ok {
			positions[n.ID] = p
		}
	}
	if len(positions) == 0 {
		return RetainedLayout{Source: RetainedNone}
	}
	r.Positions = positions
	return r
}

func (s *RevisionStamper) retained(ctx context.Context, wf, prior *Workflow) RetainedLayout {
	if prior != nil {
		if prior.Presentation != nil {
			if s.deps.Displayed != nil {
				r, ok, err := s.deps.Displayed.DisplayedLayout(ctx, prior.SavedRevision())
				if err != nil {
					s.deps.Logger.Warn("read displayed layout fallback", "revision", prior.SavedRevision(), "error", err)
				} else if ok {
					r = filterRetained(r, wf)
					if len(r.Positions) > 0 {
						r.Source = RetainedFromLayout
						return r
					}
				}
			}
			r := filterRetained(prior.Presentation.Retained, wf)
			if len(r.Positions) > 0 {
				return r
			}
		}
		r := filterRetained(manifestPositions(prior), wf)
		if len(r.Positions) > 0 {
			return r
		}
	}
	return filterRetained(manifestPositions(wf), wf)
}

// Stamp prepares an immutable save input before its caller makes it visible.
// The caller holds the destination mutation lock throughout the save protocol.
func (s *RevisionStamper) Stamp(ctx context.Context, scope Scope, name string, wf *Workflow, prior *Workflow, reason SaveReason) (ArrangementRequest, error) {
	if wf == nil {
		return ArrangementRequest{}, fmt.Errorf("workflow: cannot stamp nil workflow")
	}
	if err := validateStampDestination(scope, name, reason); err != nil {
		return ArrangementRequest{}, err
	}
	if scope.Kind == ScopeShared {
		scope = Shared()
	}
	if err := s.deps.Settler.Settle(ctx, scope, name); err != nil {
		return ArrangementRequest{}, err
	}
	token, now := s.deps.NewToken(), s.deps.Now().UTC()
	if !validRevision(token) || now.IsZero() {
		return ArrangementRequest{}, fmt.Errorf("workflow: invalid revision token or save time")
	}
	retained := s.retained(ctx, wf, prior)
	old := wf.Presentation
	wf.Presentation = &Presentation{
		Revision: token, SavedAt: now, Reason: reason,
		Scope: ScopeLabel(scope), OwnerID: scope.OwnerID, Name: name,
		Retained: cloneRetained(retained),
	}
	b, err := json.Marshal(wf)
	if err != nil {
		wf.Presentation = old
		return ArrangementRequest{}, err
	}
	req := ArrangementRequest{
		Revision: token, Scope: scope, Name: name, WorkflowID: wf.ID,
		Reason: reason, Manifest: b, Retained: retained, RequestedAt: now,
	}
	if err = s.deps.Journal.Record(ctx, cloneArrangementRequest(req)); err != nil {
		wf.Presentation = old
		return ArrangementRequest{}, err
	}
	return req, nil
}

// Committed records durable proof before enqueue. A failed receipt or enqueue
// leaves the exact prepared input available to recovery, even on caller cancellation.
func (s *RevisionStamper) Committed(ctx context.Context, req ArrangementRequest) {
	s.committed(ctx, req)
}

// committed reports whether a durable receipt permits tree-backup cleanup.
func (s *RevisionStamper) committed(ctx context.Context, req ArrangementRequest) bool {
	if s == nil || req.Revision == "" {
		return false
	}
	if err := s.deps.Journal.MarkCommitted(ctx, req.Revision); err != nil {
		s.deps.Logger.Error("record layout commit receipt", "revision", req.Revision, "error", err)
		return false
	}
	if err := s.deps.Requester.RequestArrangement(ctx, cloneArrangementRequest(req)); err != nil {
		s.deps.Logger.Error("request committed arrangement", "revision", req.Revision, "error", err)
		return true
	}
	s.Abandon(ctx, req)
	return true
}

// Abandon removes evidence only after the caller has ruled out a visible commit
// (or after Committed has received an enqueue acknowledgement).
func (s *RevisionStamper) Abandon(ctx context.Context, req ArrangementRequest) {
	if s == nil || req.Revision == "" {
		return
	}
	if err := s.deps.Journal.Remove(ctx, req.Revision); err != nil {
		s.deps.Logger.Error("remove layout journal entry", "revision", req.Revision, "error", err)
	}
}

// EnsureStamped resumes only an exact, proven interrupted operation. A token in
// a folder alone is not commit proof. A cleaned seed receipt can be restored
// only from an exact durable acknowledgement; existing receipts are never rewritten.
// As with Stamp, the caller holds the destination mutation lock.
func (s *RevisionStamper) EnsureStamped(ctx context.Context, scope Scope, name string, reason SaveReason) (ArrangementRequest, error) {
	if err := validateStampDestination(scope, name, reason); err != nil {
		return ArrangementRequest{}, err
	}
	if scope.Kind == ScopeShared {
		scope = Shared()
	}
	if err := s.deps.Settler.Settle(ctx, scope, name); err != nil {
		return ArrangementRequest{}, err
	}
	wf, err := s.repo.Load(ctx, scope, name)
	if err != nil {
		return ArrangementRequest{}, err
	}
	if wf == nil || wf.Presentation == nil {
		return ArrangementRequest{}, fmt.Errorf("workflow: no interrupted save at %s", name)
	}
	p := wf.Presentation
	if !validRevision(p.Revision) || p.SavedAt.IsZero() || p.Scope != ScopeLabel(scope) || p.OwnerID != scope.OwnerID || p.Name != name || p.Reason != reason {
		return ArrangementRequest{}, fmt.Errorf("workflow: saved presentation does not match destination")
	}
	entries, err := s.deps.Journal.Entries(ctx)
	if err != nil {
		return ArrangementRequest{}, err
	}
	saved, err := json.Marshal(wf)
	if err != nil {
		return ArrangementRequest{}, err
	}

	matches := func(r ArrangementRequest) bool {
		return r.Revision == p.Revision && r.Scope == scope && r.Name == name && r.WorkflowID == wf.ID &&
			r.Reason == reason && r.RequestedAt.Equal(p.SavedAt) && sameJSON(r.Manifest, saved) && reflect.DeepEqual(r.Retained, p.Retained)
	}
	hasInput := false
	for _, e := range entries {
		if e.Request.Revision == p.Revision {
			hasInput = true
		}
		if e.State == JournalCommitted && matches(e.Request) {
			return cloneArrangementRequest(e.Request), nil
		}
	}
	// Only seeds have a second acknowledgement (the once-per-name ledger).
	// Enqueue and worker recovery may already have removed their journal input.
	// Restore that exact input for the normal receipt/enqueue protocol, without
	// minting a token or trusting a same-name folder or publication alone.
	if reader, ok := s.deps.Requester.(AcknowledgedArrangementReader); ok && reason == SaveReasonSeed && !hasInput && wf.ID != uuid.Nil {
		r, err := reader.AcknowledgedArrangement(ctx, p.Revision)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return ArrangementRequest{}, err
		}
		if err == nil && matches(r) {
			if err := s.deps.Journal.Record(ctx, cloneArrangementRequest(r)); err != nil {
				return ArrangementRequest{}, err
			}
			if err := s.deps.Journal.MarkCommitted(ctx, r.Revision); err != nil {
				return ArrangementRequest{}, err
			}
			return cloneArrangementRequest(r), nil
		}
	}

	return ArrangementRequest{}, fmt.Errorf("%w: no exact committed input for %s", ErrMutationPending, p.Revision)
}

func sameJSON(a, b []byte) bool {
	decode := func(data []byte) (any, bool) {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) != nil {
			return nil, false
		}
		_, err := dec.Token()
		return v, err == io.EOF
	}
	av, aok := decode(a)
	bv, bok := decode(b)
	return aok && bok && sameJSONValue(av, bv)
}

// Compare numbers without float64 rounding: distinct acknowledged inputs must
// stay distinct, while equivalent decimal/exponent spellings still match.
func sameJSONValue(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for key, x := range av {
			y, ok := bv[key]
			if !ok || !sameJSONValue(x, y) {
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
			if !sameJSONValue(av[i], bv[i]) {
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
		return reflect.DeepEqual(a, b)
	}
}
