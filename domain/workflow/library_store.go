package workflow

import "context"

// LibraryStore manages the files of one scope's library folder. Paths are
// library-relative and validated with the same rules as ParseLibPath's
// remainder. Malformed or escaping paths return ErrInvalidLibPath; a missing
// read or delete target returns ErrFileNotFound.
type LibraryStore interface {
	// ListFiles returns the scope's library files, sorted and slash-separated.
	// A missing library folder yields an empty slice.
	ListFiles(ctx context.Context, scope Scope) ([]string, error)
	// ReadFile reads rel from exactly the given scope's library.
	ReadFile(ctx context.Context, scope Scope, rel string) ([]byte, error)
	// WriteFile atomically replaces rel, creating the library on demand.
	WriteFile(ctx context.Context, scope Scope, rel string, content []byte) error
	// DeleteFile removes rel from the given scope's library.
	DeleteFile(ctx context.Context, scope Scope, rel string) error
}
