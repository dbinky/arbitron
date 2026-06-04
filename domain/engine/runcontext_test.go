package engine

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// Happy path: OwnerFromContext retrieves the owner placed by withOwner.
func TestOwnerFromContext_SetAndGet(t *testing.T) {
	owner := uuid.New()
	ctx := withOwner(context.Background(), owner)
	got := OwnerFromContext(ctx)
	assert.Equal(t, owner, got)
}

// Failure: no owner in context returns uuid.Nil.
func TestOwnerFromContext_Absent(t *testing.T) {
	got := OwnerFromContext(context.Background())
	assert.Equal(t, uuid.Nil, got, "absent owner must return uuid.Nil")
}

// Edge: a child context inherits the owner set on the parent.
func TestOwnerFromContext_ChildInherits(t *testing.T) {
	owner := uuid.New()
	parent := withOwner(context.Background(), owner)
	child := context.WithValue(parent, struct{ tag string }{"child"}, "value")
	assert.Equal(t, owner, OwnerFromContext(child), "child context must inherit owner from parent")
}

// Edge: a later withOwner call shadows an earlier one (context overlay).
func TestOwnerFromContext_OverrideShadows(t *testing.T) {
	first := uuid.New()
	second := uuid.New()
	ctx := withOwner(context.Background(), first)
	ctx = withOwner(ctx, second)
	assert.Equal(t, second, OwnerFromContext(ctx), "innermost owner must shadow the outer one")
}

// Edge: withOwner with uuid.Nil is propagated correctly (shared-only reads).
func TestOwnerFromContext_NilOwner(t *testing.T) {
	ctx := withOwner(context.Background(), uuid.Nil)
	got := OwnerFromContext(ctx)
	assert.Equal(t, uuid.Nil, got, "explicit uuid.Nil owner must be propagated, not treated as absent")
}
