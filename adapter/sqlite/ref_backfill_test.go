package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refBackfillFixture wires a WorkflowService with a real SQLite-backed identity
// registry plus a ReferenceBackfiller over the same db, returning the pieces a
// test needs to seed rows and run BackfillReferences.
type refBackfillFixture struct {
	db       testDB
	registry *WorkflowIdentityRepo
	svc      *workflow.WorkflowService
	ctx      context.Context
}

func newRefBackfillFixture(t *testing.T) refBackfillFixture {
	t.Helper()
	db := setupTestDB(t)
	reg := NewWorkflowIdentityRepo(db.DB)
	backfiller := NewReferenceBackfiller(db.DB)

	// A WorkflowService whose only wired dependency for this path is the registry
	// + the reference backfiller. repo/publishers are nil; BackfillReferences does
	// not touch them.
	svc := workflow.NewWorkflowService(nil, nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(backfiller)

	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(),
		Role:   identity.RoleAdmin,
	})
	return refBackfillFixture{db: db, registry: reg, svc: svc, ctx: ctx}
}

// registerWorkflow seeds the identity registry with name->a-fresh-id and returns
// the allocated id.
func (f refBackfillFixture) registerWorkflow(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	require.NoError(t, f.registry.Register(f.ctx, workflow.RegistryEntry{
		ID:    id,
		Name:  name,
		Scope: workflow.ScopeShared,
	}))
	return id
}

const refTestOwner = "00000000-0000-0000-0000-000000000001"

// insertInstance inserts an instance the way pre-Phase-2 code does (no
// workflow_id) so the backfill has a NULL id to resolve. Returns the id.
func (f refBackfillFixture) insertInstance(t *testing.T, workflowName string) string {
	t.Helper()
	id := uuid.New().String()
	_, err := f.db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables)
		 VALUES (?, ?, ?, '{}', 'queued', 'normal', '{}', '{}')`,
		id, refTestOwner, workflowName,
	)
	require.NoError(t, err)
	return id
}

// insertSchedule inserts a schedule with a NULL workflow_id. Returns the id.
func (f refBackfillFixture) insertSchedule(t *testing.T, workflowName string) string {
	t.Helper()
	id := uuid.New().String()
	_, err := f.db.DB.Exec(
		`INSERT INTO schedules (id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables)
		 VALUES (?, ?, '0 * * * *', 'UTC', ?, '', 1, '{}')`,
		id, workflowName, refTestOwner,
	)
	require.NoError(t, err)
	return id
}

// insertPublication inserts a published_workflows row (its name column is the
// workflow name) with a NULL workflow_id.
func (f refBackfillFixture) insertPublication(t *testing.T, name string) {
	t.Helper()
	// publisher_id references users(id); the seeded default admin user works.
	var publisherID string
	require.NoError(t, f.db.DB.QueryRow(`SELECT id FROM users WHERE username = 'local'`).Scan(&publisherID))
	_, err := f.db.DB.Exec(
		`INSERT INTO published_workflows (name, publisher_id, source_name) VALUES (?, ?, ?)`,
		name, publisherID, name,
	)
	require.NoError(t, err)
}

func (f refBackfillFixture) instanceWorkflowID(t *testing.T, id string) *string {
	t.Helper()
	var wid *string
	require.NoError(t, f.db.DB.QueryRow(`SELECT workflow_id FROM instances WHERE id = ?`, id).Scan(&wid))
	return wid
}

func (f refBackfillFixture) scheduleWorkflowID(t *testing.T, id string) *string {
	t.Helper()
	var wid *string
	require.NoError(t, f.db.DB.QueryRow(`SELECT workflow_id FROM schedules WHERE id = ?`, id).Scan(&wid))
	return wid
}

func (f refBackfillFixture) publicationWorkflowID(t *testing.T, name string) *string {
	t.Helper()
	var wid *string
	require.NoError(t, f.db.DB.QueryRow(`SELECT workflow_id FROM published_workflows WHERE name = ?`, name).Scan(&wid))
	return wid
}

// Happy path: a name registered in the registry is resolved to its id across all
// three reference tables.
func TestRefBackfill_ResolvesNameToIDOnAllTables(t *testing.T) {
	f := newRefBackfillFixture(t)
	idA := f.registerWorkflow(t, "alpha")

	instID := f.insertInstance(t, "alpha")
	schedID := f.insertSchedule(t, "alpha")
	f.insertPublication(t, "alpha")

	require.NoError(t, f.svc.BackfillReferences(f.ctx))

	if got := f.instanceWorkflowID(t, instID); assert.NotNil(t, got) {
		assert.Equal(t, idA.String(), *got)
	}
	if got := f.scheduleWorkflowID(t, schedID); assert.NotNil(t, got) {
		assert.Equal(t, idA.String(), *got)
	}
	if got := f.publicationWorkflowID(t, "alpha"); assert.NotNil(t, got) {
		assert.Equal(t, idA.String(), *got)
	}
}

// Success/idempotency: a second run is stable and not an error.
func TestRefBackfill_IdempotentRerun(t *testing.T) {
	f := newRefBackfillFixture(t)
	idA := f.registerWorkflow(t, "alpha")
	instID := f.insertInstance(t, "alpha")
	schedID := f.insertSchedule(t, "alpha")
	f.insertPublication(t, "alpha")

	require.NoError(t, f.svc.BackfillReferences(f.ctx))
	require.NoError(t, f.svc.BackfillReferences(f.ctx))

	assert.Equal(t, idA.String(), *f.instanceWorkflowID(t, instID))
	assert.Equal(t, idA.String(), *f.scheduleWorkflowID(t, schedID))
	assert.Equal(t, idA.String(), *f.publicationWorkflowID(t, "alpha"))
}

// Edge: a name that does not resolve stays NULL and is not an error.
func TestRefBackfill_UnresolvedNameStaysNull(t *testing.T) {
	f := newRefBackfillFixture(t)
	// "ghost" is intentionally NOT registered.
	instID := f.insertInstance(t, "ghost")
	schedID := f.insertSchedule(t, "ghost")
	f.insertPublication(t, "ghost")

	require.NoError(t, f.svc.BackfillReferences(f.ctx))

	assert.Nil(t, f.instanceWorkflowID(t, instID), "unresolved instance name stays NULL")
	assert.Nil(t, f.scheduleWorkflowID(t, schedID), "unresolved schedule name stays NULL")
	assert.Nil(t, f.publicationWorkflowID(t, "ghost"), "unresolved publication name stays NULL")
}

// Edge: a row that already carries a workflow_id is left untouched (only NULL
// rows are candidates), and legacy NULL-id rows coexist alongside resolved ones.
func TestRefBackfill_ExistingIDUntouchedAndCoexists(t *testing.T) {
	f := newRefBackfillFixture(t)
	f.registerWorkflow(t, "alpha")

	// Pre-set an instance with a sentinel id under a *different* name; the backfill
	// must not overwrite a non-NULL workflow_id even if the name would resolve.
	preset := uuid.New().String()
	sentinel := uuid.New().String()
	_, err := f.db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_id, workflow_snapshot, state, priority, variables, original_variables)
		 VALUES (?, ?, 'alpha', ?, '{}', 'queued', 'normal', '{}', '{}')`,
		preset, refTestOwner, sentinel,
	)
	require.NoError(t, err)

	// A legacy NULL-id instance for an unresolved name coexists.
	ghost := f.insertInstance(t, "ghost")
	// A resolvable NULL-id instance.
	resolvable := f.insertInstance(t, "alpha")

	require.NoError(t, f.svc.BackfillReferences(f.ctx))

	assert.Equal(t, sentinel, *f.instanceWorkflowID(t, preset), "pre-existing workflow_id must not be overwritten")
	assert.Nil(t, f.instanceWorkflowID(t, ghost), "unresolved legacy row coexists as NULL")
	assert.NotNil(t, f.instanceWorkflowID(t, resolvable), "resolvable NULL-id row is backfilled")
}

// Robustness: a workflow run many times (multiple instances + schedules for the
// same name) must have ALL of its NULL rows stamped in a single backfill call.
// This is the dominant real-world scenario: Phase-2 is deployed against a DB
// that already has hundreds of instance rows per workflow name.
func TestRefBackfill_MultipleNullRowsSameName(t *testing.T) {
	f := newRefBackfillFixture(t)
	idA := f.registerWorkflow(t, "heavy-wf")

	// 3 instances for "heavy-wf" — typical for a frequently-run workflow.
	inst1 := f.insertInstance(t, "heavy-wf")
	inst2 := f.insertInstance(t, "heavy-wf")
	inst3 := f.insertInstance(t, "heavy-wf")
	// 2 schedules — workflow enabled on multiple crons.
	sched1 := f.insertSchedule(t, "heavy-wf")
	sched2 := f.insertSchedule(t, "heavy-wf")
	// 1 publication.
	f.insertPublication(t, "heavy-wf")

	require.NoError(t, f.svc.BackfillReferences(f.ctx))

	for _, id := range []string{inst1, inst2, inst3} {
		got := f.instanceWorkflowID(t, id)
		if assert.NotNil(t, got, "instance %s must be stamped", id) {
			assert.Equal(t, idA.String(), *got)
		}
	}
	for _, id := range []string{sched1, sched2} {
		got := f.scheduleWorkflowID(t, id)
		if assert.NotNil(t, got, "schedule %s must be stamped", id) {
			assert.Equal(t, idA.String(), *got)
		}
	}
	if got := f.publicationWorkflowID(t, "heavy-wf"); assert.NotNil(t, got) {
		assert.Equal(t, idA.String(), *got, "publication must be stamped")
	}
}

// Robustness: when the same workflow name has NULL rows in multiple reference
// tables, UnresolvedReferenceNames must return it only once (UNION semantics).
// If it returned duplicate names, BackfillReferences would call SetReferenceID
// redundantly — not harmful (idempotent), but indicative of a SQL regression.
func TestRefBackfill_UnresolvedNamesDeduplicatedAcrossTables(t *testing.T) {
	f := newRefBackfillFixture(t)

	// "multi" appears in both instances and schedules with a NULL workflow_id.
	f.insertInstance(t, "multi")
	f.insertSchedule(t, "multi")
	// "pub-only" appears only in published_workflows.
	f.insertPublication(t, "pub-only")

	backfiller := NewReferenceBackfiller(f.db.DB)
	names, err := backfiller.UnresolvedReferenceNames(f.ctx)
	require.NoError(t, err)

	// UNION must deduplicate "multi" even though it appears in two tables.
	assert.Len(t, names, 2, "UNION must return each name exactly once")
	assert.ElementsMatch(t, []string{"multi", "pub-only"}, names)
}
