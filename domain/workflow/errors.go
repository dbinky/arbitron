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
)
