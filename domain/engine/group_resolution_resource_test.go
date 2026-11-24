package engine_test

import (
	"context"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func validResolutionResource() engine.ResolutionResource {
	id := engine.GroupResolutionIdentity{GroupRunID: uuid.New(), MergeIndex: 2, Attempt: 3, TaskKey: "b", Open: true}
	rid := engine.ResolutionResourceID(id)
	return engine.ResolutionResource{ID: rid, Identity: id, SourceDir: "/ws/.group-backing/x-gather-1", WorkDir: "/ws/.runner-resources/resolutions/" + rid.String()}
}

func TestResolutionResource_ValidateAcceptsCanonicalResource(t *testing.T) {
	require.NoError(t, validResolutionResource().Validate())
}

func TestResolutionResource_ValidateRejectsForgedIdentityAndDirs(t *testing.T) {
	for name, mut := range map[string]func(*engine.ResolutionResource){
		"id not derived from identity": func(r *engine.ResolutionResource) { r.ID = uuid.New() },
		"nil group run":                func(r *engine.ResolutionResource) { r.Identity.GroupRunID = uuid.Nil },
		"relative source":              func(r *engine.ResolutionResource) { r.SourceDir = "staging" },
		"empty workdir":                func(r *engine.ResolutionResource) { r.WorkDir = "" },
		"workdir is the source":        func(r *engine.ResolutionResource) { r.WorkDir = r.SourceDir },
		"workdir not named by id":      func(r *engine.ResolutionResource) { r.WorkDir = "/ws/.runner-resources/resolutions/other" },
	} {
		r := validResolutionResource()
		mut(&r)
		assert.Error(t, r.Validate(), name)
	}
}

func TestResolutionResource_ContextCarrier(t *testing.T) {
	_, ok := engine.ResolutionResourceFromContext(context.Background())
	assert.False(t, ok, "an ordinary turn carries no resolution resource")
	r := validResolutionResource()
	got, ok := engine.ResolutionResourceFromContext(engine.ContextWithResolutionResource(context.Background(), r))
	require.True(t, ok)
	assert.Equal(t, r, got)
}

func TestResolutionResourceID_DeterministicPerSessionAndNeverTheRun(t *testing.T) {
	run := uuid.New()
	id := engine.GroupResolutionIdentity{GroupRunID: run, MergeIndex: 1, Attempt: 1, TaskKey: "b", Open: true}
	a := engine.ResolutionResourceID(id)
	assert.NotEqual(t, uuid.Nil, a)
	assert.NotEqual(t, run, a, "the staging resource is never the group run (or parent) workspace")
	ended := id
	ended.Open, ended.TaskKey = false, "other"
	assert.Equal(t, a, engine.ResolutionResourceID(ended), "ending the session keeps its resource")
	assert.NotEqual(t, a, engine.ResolutionResourceID(engine.GroupResolutionIdentity{GroupRunID: run, MergeIndex: 1, Attempt: 2}))
	assert.NotEqual(t, a, engine.ResolutionResourceID(engine.GroupResolutionIdentity{GroupRunID: run, MergeIndex: 2, Attempt: 1}))
	assert.NotEqual(t, a, engine.ResolutionResourceID(engine.GroupResolutionIdentity{GroupRunID: uuid.New(), MergeIndex: 1, Attempt: 1}))
	assert.Equal(t, uuid.NewSHA1(run, []byte("resolution-staging/1/1")), a, "UUIDv5/SHA1 under GroupRunID")
	stored := map[string]any{"kind": "group_resolution", "groupRunId": run.String(), "mergeIndex": float64(1), "attempt": float64(1), "taskKey": "b"}
	decoded, ok := engine.GroupResolutionIdentityOf(stored)
	require.True(t, ok)
	assert.Equal(t, a, engine.ResolutionResourceID(decoded), "the identity decoded from the stored record names the same resource")
	assert.Equal(t, "resolution:"+a.String(), engine.ResolutionResourceKey(a))
}
