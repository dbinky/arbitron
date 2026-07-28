package instance

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestSpawnLinkContext_RoundTrip(t *testing.T) {
	want := SpawnLink{
		ParentInstanceID: uuid.New(),
		ParentNodeID:     "spawn-1",
		Mode:             SpawnModeWaitForChild,
	}
	ctx := WithSpawnLink(context.Background(), want)

	got, ok := SpawnLinkFromContext(ctx)
	if !ok {
		t.Fatal("SpawnLinkFromContext must report ok=true after WithSpawnLink")
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestSpawnLinkContext_AbsentReportsNotOk(t *testing.T) {
	if _, ok := SpawnLinkFromContext(context.Background()); ok {
		t.Fatal("a plain context carries no spawn link")
	}
}

// The Origin carrier and the SpawnLink carrier are INDEPENDENT: setting one
// must not clear or shadow the other, because a spawned child still records how
// its parent's run was started.
func TestSpawnLinkContext_IndependentOfOrigin(t *testing.T) {
	sid := uuid.New()
	ctx := WithOrigin(context.Background(), Origin{Source: OriginSchedule, ScheduleID: &sid})
	ctx = WithSpawnLink(ctx, SpawnLink{
		ParentInstanceID: uuid.New(), ParentNodeID: "n", Mode: SpawnModeFireAndForget,
	})

	if o, ok := OriginFromContext(ctx); !ok || o.Source != OriginSchedule {
		t.Fatalf("the origin must survive alongside a spawn link, got %+v ok=%v", o, ok)
	}
	if l, ok := SpawnLinkFromContext(ctx); !ok || l.Mode != SpawnModeFireAndForget {
		t.Fatalf("the spawn link must survive alongside an origin, got %+v ok=%v", l, ok)
	}
}

// A derived context overrides the inherited link, which is what makes a
// grandchild carry ITS parent's link rather than its grandparent's.
func TestSpawnLinkContext_InnerOverridesOuter(t *testing.T) {
	outer := SpawnLink{ParentInstanceID: uuid.New(), ParentNodeID: "a", Mode: SpawnModeFireAndForget}
	inner := SpawnLink{ParentInstanceID: uuid.New(), ParentNodeID: "b", Mode: SpawnModeWaitForChild}

	ctx := WithSpawnLink(WithSpawnLink(context.Background(), outer), inner)
	got, _ := SpawnLinkFromContext(ctx)
	if got != inner {
		t.Fatalf("the innermost link must win: got %+v, want %+v", got, inner)
	}
}
