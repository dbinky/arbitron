package workflow

import (
	"errors"

	"github.com/arbitron/arbitron/domain/identity"
)

// ErrForbidden: the caller is authenticated but not an admin. Aliased to the
// canonical identity.ErrForbidden (the same consolidation UserService does) so an
// errors.Is across package boundaries matches and the adapters map 403 uniformly.
// It gates the admin-only workflow lifecycle operations (Rename, etc.).
var ErrForbidden = identity.ErrForbidden

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

	// ErrInvalidStatsRange indicates a workflow-stats query specified a
	// from/to window that is inverted (from > to) or wider than MaxStatsWindow.
	ErrInvalidStatsRange = errors.New("invalid stats range")

	// ErrStatsUnavailable indicates the stats read port was not configured on the
	// Service (a composition/wiring error, not a caller error).
	ErrStatsUnavailable = errors.New("stats not available")

	// ErrHasRunningInstances indicates an admin Delete was refused because the
	// workflow still has one or more non-terminal instances (queued, running,
	// waiting, or paused) carrying its stable id. Deletion would orphan a live run,
	// so the operation is rejected until those runs reach a terminal state.
	ErrHasRunningInstances = errors.New("workflow has running instances")

	// ErrArchived indicates a trigger was refused because the workflow is archived.
	// An archived workflow remains listable and editable (and the in-flight runs it
	// already spawned continue), but new triggers are rejected until it is
	// unarchived.
	ErrArchived = errors.New("workflow is archived")

	// ErrMisconfiguredGuard indicates a server/admin-path lifecycle op (Delete,
	// Unpublish) was refused because its running-instance guard could not run: a
	// registry is wired but no ActiveInstanceCounter is, so the service cannot
	// confirm there are no live runs. The op FAILS CLOSED rather than silently
	// skipping the guard — a half-wired admin stack must not perform an unguarded
	// delete. It is a composition/wiring error (an operator fix), not a caller
	// error.
	ErrMisconfiguredGuard = errors.New("running-instance guard is not configured")
)
