package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeRawLink bypasses Create's all-or-none guard to plant a PARTIAL link,
// standing in for a hand-edited row, a partial restore, or a future second
// writer. Create itself can never produce this state.
func writeRawLink(t *testing.T, db testDB, id uuid.UUID, parentID, nodeID, mode any) {
	t.Helper()
	base := newLinkedInstance(instance.SpawnLink{})
	base.ID = id
	require.NoError(t, NewInstanceRepo(db.DB).Create(context.Background(), base))
	_, err := db.DB.Exec(
		`UPDATE instances SET parent_instance_id = ?, parent_node_id = ?, spawn_mode = ? WHERE id = ?`,
		parentID, nodeID, mode, uuidToString(id))
	require.NoError(t, err)
}

// A row carrying a parent id but NO node id can never be matched by the wake:
// read back as a half-populated link it would report as non-root while being
// unreachable, stranding a parent forever. It must collapse to a root instead.
func TestSpawnLinkRead_ParentWithoutNodeIDReadsAsRoot(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	id := uuid.New()
	writeRawLink(t, db, id, uuidToString(uuid.New()), nil, "wait_for_child")

	got, err := repo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, got.IsRoot(), "an incomplete tuple must collapse to no link at all")
	assert.Nil(t, got.ParentNodeID)
	assert.Equal(t, instance.SpawnMode(""), got.SpawnMode)
}

func TestSpawnLinkRead_NodeIDWithoutParentReadsAsRoot(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	id := uuid.New()
	writeRawLink(t, db, id, nil, "spawn-1", "wait_for_child")

	got, err := repo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, got.IsRoot())
	assert.Nil(t, got.ParentNodeID, "no partial population: all three or none")
}

// An EMPTY node id is as unmatchable as a NULL one — the wake matches on an
// exact node id — so it collapses too. (An unrecognized MODE cannot reach the
// read side at all: migration 018's CHECK already rejects it. The read-side
// guard exists for the tuple-completeness invariant the CHECK cannot express
// without rebuilding the table.)
func TestSpawnLinkRead_EmptyNodeIDReadsAsRoot(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	id := uuid.New()
	writeRawLink(t, db, id, uuidToString(uuid.New()), "", "wait_for_child")

	got, err := repo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, got.IsRoot())
	assert.Equal(t, instance.SpawnMode(""), got.SpawnMode)
}

// The mode CHECK is a real part of this invariant's defence, so pin it: a
// second writer cannot plant an unmatchable mode even by raw SQL.
func TestSpawnLinkWrite_SchemaRejectsAnUnrecognizedMode(t *testing.T) {
	db := setupTestDB(t)
	id := uuid.New()
	base := newLinkedInstance(instance.SpawnLink{})
	base.ID = id
	require.NoError(t, NewInstanceRepo(db.DB).Create(context.Background(), base))

	_, err := db.DB.Exec(`UPDATE instances SET spawn_mode = ? WHERE id = ?`,
		"WAIT_FOR_CHILD", uuidToString(id))
	require.Error(t, err, "migration 018's CHECK must reject a mode outside the allowed set")
}

// A COMPLETE tuple is of course preserved verbatim — the collapse must not eat
// real links.
func TestSpawnLinkRead_CompleteTupleRoundTrips(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	childID := childOf(t, repo, parent.ID, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateRunning, uuid.MustParse(spawnTestOwner), instance.VisibilityPrivate, "")

	got, err := repo.Get(ctx, childID)
	require.NoError(t, err)
	require.NotNil(t, got.ParentInstanceID)
	assert.Equal(t, parent.ID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID)
	assert.Equal(t, "spawn-1", *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeWaitForChild, got.SpawnMode)
}

// A CORRUPT parent id is an error, not a silent root: a malformed value is
// damage, and rooting it would hide real corruption behind a plausible state.
func TestSpawnLinkRead_MalformedParentIDIsAnError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	id := uuid.New()
	writeRawLink(t, db, id, "not-a-uuid", "spawn-1", "wait_for_child")

	_, err := repo.Get(context.Background(), id)
	require.Error(t, err)
}

// ---------- hand-written INSERT vs queries/instances.sql ----------

var insertColumnsRe = regexp.MustCompile(`(?s)INSERT INTO instances\s*\(([^)]*)\)`)

// readQueriesFile loads the readable source-of-truth SQL next to this package.
func readQueriesFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("queries", "instances.sql"))
	require.NoError(t, err)
	return string(b)
}

func columnsFrom(t *testing.T, stmt string) []string {
	t.Helper()
	m := insertColumnsRe.FindStringSubmatch(stmt)
	require.Len(t, m, 2, "could not locate the INSERT column list")
	var cols []string
	for _, c := range strings.Split(m[1], ",") {
		if c = strings.TrimSpace(c); c != "" {
			cols = append(cols, c)
		}
	}
	sort.Strings(cols)
	return cols
}

// InstanceRepo.Create executes a HAND-WRITTEN INSERT so the column set can
// evolve without a sqlc regeneration, while queries/instances.sql carries the
// readable source of truth. Two executable-looking copies of the same statement
// are exactly the kind of thing that drifts silently — a column added to one and
// not the other reads as a documentation bug until someone regenerates and gets
// a runtime arity error.
//
// This pins them to the same column SET (order is irrelevant; both are
// positional against their own placeholder list) and to matching arity, which is
// what makes the .sql file trustworthy as documentation.
func TestCreateInstanceSQL_MatchesTheSourceOfTruthQuery(t *testing.T) {
	source := readQueriesFile(t)

	start := strings.Index(source, "-- name: CreateInstance :one")
	require.GreaterOrEqual(t, start, 0, "CreateInstance must exist in queries/instances.sql")
	end := strings.Index(source[start:], ";")
	require.Greater(t, end, 0)
	documented := source[start : start+end]

	handWritten := columnsFrom(t, createInstanceSQL)
	assert.Equal(t, columnsFrom(t, documented), handWritten,
		"the hand-written INSERT and queries/instances.sql must name the same columns")

	assert.Equal(t, len(handWritten), strings.Count(createInstanceSQL[strings.Index(createInstanceSQL, "VALUES"):], "?"),
		"one placeholder per column in the hand-written statement")
	assert.Equal(t, len(handWritten), strings.Count(documented[strings.Index(documented, "VALUES"):], "?"),
		"one placeholder per column in the documented statement")
}
