package workflow

import "errors"

// Sentinel errors for workflow repository operations. Adapters use errors.Is
// to classify domain errors without relying on error message strings.
var (
	// ErrNotFound indicates the requested workflow does not exist.
	ErrNotFound = errors.New("workflow not found")

	// ErrAlreadyExists indicates a workflow with the given name already exists.
	ErrAlreadyExists = errors.New("workflow already exists")

	// ErrInvalidName indicates the workflow name is invalid (empty, contains
	// path separators, or attempts directory traversal).
	ErrInvalidName = errors.New("invalid workflow name")

	// ErrInvalidFilePath indicates the relative file path is invalid
	// (attempts directory traversal outside the workflow directory).
	ErrInvalidFilePath = errors.New("invalid file path")

	// ErrFileNotFound indicates the requested file does not exist within
	// the workflow directory.
	ErrFileNotFound = errors.New("file not found")

	// ErrSharedReadOnly indicates a caller attempted to mutate a workflow in the
	// shared scope; shared workflows are only written via publish.
	ErrSharedReadOnly = errors.New("shared workflows are read-only; copy to edit or re-publish")

	// ErrUnauthorized indicates the caller is not permitted to perform the
	// requested operation (e.g. a non-admin loading another user's private workflow).
	ErrUnauthorized = errors.New("not authorized")

	// ErrInvalidVisibility indicates a publish/visibility request specified an
	// unstorable level, or omitted the required department for a department-level
	// publication.
	ErrInvalidVisibility = errors.New("invalid visibility request")
)
