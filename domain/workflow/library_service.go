package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/domain/audit"
)

// ErrLibraryUnavailable reports that no LibraryStore is wired.
var ErrLibraryUnavailable = errors.New("shared library store is not configured")

// libraryAuditTarget is the audit target recorded for shared library edits.
const libraryAuditTarget = "@lib"

// LibraryListing is the caller's view of both library scopes.
type LibraryListing struct {
	Private []string `json:"private"`
	Shared  []string `json:"shared"`
}

// SetLibraryStore injects the @lib file store. Library methods never take the
// execution gate: library files are read at run time, not snapshotted.
func (s *Service) SetLibraryStore(l LibraryStore) { s.library = l }

// libraryCaller resolves the caller and confirms a store is configured.
func (s *Service) libraryCaller(ctx context.Context) (Scope, bool, error) {
	id, err := caller(ctx)
	if err != nil {
		return Scope{}, false, err
	}
	if s.library == nil {
		return Scope{}, false, ErrLibraryUnavailable
	}
	return Private(id.UserID), id.IsAdmin(), nil
}

// LibraryList lists the caller's private library and the shared library.
func (s *Service) LibraryList(ctx context.Context) (LibraryListing, error) {
	own, _, err := s.libraryCaller(ctx)
	if err != nil {
		return LibraryListing{}, err
	}
	priv, err := s.library.ListFiles(ctx, own)
	if err != nil {
		return LibraryListing{}, libraryError("list", err)
	}
	shared, err := s.library.ListFiles(ctx, Shared())
	if err != nil {
		return LibraryListing{}, libraryError("list", err)
	}
	return LibraryListing{Private: priv, Shared: shared}, nil
}

// LibraryRead reads rel. A nil scope is the effective read: the caller's
// private copy first, then shared, falling through ONLY on a clean not-found.
// Any other private-side failure (invalid path, escaping link, I/O) is
// returned, never masked by the shared copy.
func (s *Service) LibraryRead(ctx context.Context, scope *Scope, rel string) ([]byte, error) {
	own, _, err := s.libraryCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidLibRel(rel); err != nil {
		return nil, err
	}
	if scope != nil {
		if err := checkLibraryScope(own, *scope); err != nil {
			return nil, err
		}
		b, err := s.library.ReadFile(ctx, *scope, rel)
		if err != nil {
			return nil, libraryError("read", err)
		}
		return b, nil
	}
	b, err := s.library.ReadFile(ctx, own, rel)
	if err == nil {
		return b, nil
	}
	if !errors.Is(err, ErrFileNotFound) {
		return nil, libraryError("read", err)
	}
	b, err = s.library.ReadFile(ctx, Shared(), rel)
	if err != nil {
		return nil, libraryError("read", err)
	}
	return b, nil
}

// LibraryWrite writes rel in the given scope.
func (s *Service) LibraryWrite(ctx context.Context, scope Scope, rel string, content []byte) error {
	return s.libraryMutate(ctx, scope, rel, "write", func() error {
		return s.library.WriteFile(ctx, scope, rel, content)
	})
}

// LibraryDelete removes rel from the given scope.
func (s *Service) LibraryDelete(ctx context.Context, scope Scope, rel string) error {
	return s.libraryMutate(ctx, scope, rel, "delete", func() error {
		return s.library.DeleteFile(ctx, scope, rel)
	})
}

// libraryMutate authorizes, runs op, and audits successful shared edits.
func (s *Service) libraryMutate(ctx context.Context, scope Scope, rel, opName string, op func() error) error {
	own, admin, err := s.libraryCaller(ctx)
	if err != nil {
		return err
	}
	if err := checkLibraryScope(own, scope); err != nil {
		return err
	}
	if scope.Kind == ScopeShared && !admin {
		return fmt.Errorf("%w: only an administrator may change the shared library", ErrUnauthorized)
	}
	if err := ValidLibRel(rel); err != nil {
		return err
	}
	if err := op(); err != nil {
		return libraryError(opName, err)
	}
	if scope.Kind == ScopeShared {
		s.audit.Record(ctx, audit.ActionLibEditShared, libraryAuditTarget, auditDetail(map[string]string{
			"path": rel,
			"op":   opName,
		}))
	}
	return nil
}

// checkLibraryScope allows Shared, or the caller's own private scope.
func checkLibraryScope(own, requested Scope) error {
	if requested.Kind == ScopeShared || requested == own {
		return nil
	}
	return fmt.Errorf("%w: a private library belongs to its owner", ErrUnauthorized)
}

// libraryError keeps the store error's sentinel identity but replaces its text,
// which can carry absolute server paths. The raw detail goes to the debug log.
func libraryError(op string, err error) error {
	slog.Debug("library store error", "op", op, "error", err)
	for _, sentinel := range []error{ErrInvalidLibPath, ErrFileNotFound, ErrUnauthorized} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("%w: library %s refused", sentinel, op)
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("library %s failed", op)
}
