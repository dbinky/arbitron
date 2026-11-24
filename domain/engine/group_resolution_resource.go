package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
)

// ResolutionResourceID names one resolution session's private staging resource,
// independently of the parent workspace and of whether the session is open.
func ResolutionResourceID(id GroupResolutionIdentity) uuid.UUID {
	return uuid.NewSHA1(id.GroupRunID, []byte(fmt.Sprintf("resolution-staging/%d/%d", id.MergeIndex, id.Attempt)))
}

// ResolutionResource identifies a resolution session's trusted staging and private execution directories.
type ResolutionResource struct {
	ID        uuid.UUID
	Identity  GroupResolutionIdentity
	SourceDir string
	WorkDir   string
}

// Validate requires an identity-derived ID and distinct absolute directories,
// with the private execution directory named by the resource ID.
func (r ResolutionResource) Validate() error {
	switch {
	case r.Identity.GroupRunID == uuid.Nil:
		return errors.New("resolution resource: identity has no group run")
	case r.ID != ResolutionResourceID(r.Identity):
		return errors.New("resolution resource: id is not derived from its identity")
	case !filepath.IsAbs(r.SourceDir) || !filepath.IsAbs(r.WorkDir):
		return errors.New("resolution resource: source and work dirs must be absolute")
	case filepath.Clean(r.SourceDir) == filepath.Clean(r.WorkDir):
		return errors.New("resolution resource: the work dir must be a private copy, not the staging area")
	case filepath.Base(filepath.Clean(r.WorkDir)) != r.ID.String():
		return errors.New("resolution resource: work dir is not named by the resource id")
	}
	return nil
}

// StagingRestorer restores a resource's latest committed checkpoint into dir.
type StagingRestorer interface {
	RestoreBuiltIn(ctx context.Context, resourceID uuid.UUID, dir string) error
}

type resolutionResourceCtxKey struct{}

// ContextWithResolutionResource tags a resolution operation with its resource.
func ContextWithResolutionResource(ctx context.Context, r ResolutionResource) context.Context {
	return context.WithValue(ctx, resolutionResourceCtxKey{}, r)
}

// ResolutionResourceFromContext returns a resolution operation's resource.
func ResolutionResourceFromContext(ctx context.Context) (ResolutionResource, bool) {
	r, ok := ctx.Value(resolutionResourceCtxKey{}).(ResolutionResource)
	return r, ok
}

var (
	ErrMissingResolutionResource = errors.New("engine: resolution operation has no staging resource")
	ErrObsoleteResolutionAttempt = errors.New("engine: resolution attempt is no longer current")
	ErrResolutionNotCommitted    = errors.New("engine: resolution checkpoint is not committed")
)

// ResolutionResourceKey is the resource-lease key of a resolution resource.
func ResolutionResourceKey(resourceID uuid.UUID) string { return "resolution:" + resourceID.String() }
