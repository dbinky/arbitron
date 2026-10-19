package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// GroupRunRepo implements instance.GroupRunRepository backed by SQLite
// (tables group_runs / group_tasks, migration 019).
//
// Every statement is HAND-WRITTEN (sqlc is not part of the toolchain; see the
// createInstanceSQL precedent). Each race-prone operation is a single
// conditional UPDATE whose WHERE clause is the compare-and-swap predicate, so
// "who won" is decided by SQLite's write serialization, never by a
// read-then-write in Go. Operations that touch more than one row run inside
// ONE transaction, and every statement of that transaction goes through the
// *sql.Tx: the database is opened with SetMaxOpenConns(1), so a stray r.db
// call inside a transaction would wait forever for the connection the
// transaction holds.
type GroupRunRepo struct {
	db  *sql.DB
	now func() time.Time
}

var _ instance.GroupRunRepository = (*GroupRunRepo)(nil)

// NewGroupRunRepo creates a GroupRunRepo using the given database connection.
func NewGroupRunRepo(db *sql.DB) *GroupRunRepo {
	return &GroupRunRepo{db: db, now: time.Now}
}

// groupTimeLayout is fixed-width UTC RFC3339 with nanoseconds, so lexical
// ORDER BY on the stored strings equals chronological order (RFC3339Nano trims
// trailing zeros and would sort "…:05Z" after "…:05.5Z"). parseTime reads it.
const groupTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func groupTime(t time.Time) string { return t.UTC().Format(groupTimeLayout) }

// groupQueryer is satisfied by both *sql.DB and *sql.Tx.
type groupQueryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const groupRunColumns = `id, parent_instance_id, node_id, attempt, form, completion_rule, ` +
	`unfinished_behavior, gather_mode, config, body_ownership, start_variables, ` +
	`workspace_snapshot, state, decision, final_output, continuation_port, ` +
	`continuation_ready, continuation_delivered, actions_applied, launch_error, ` +
	`created_at, updated_at`

const groupTaskColumns = `group_run_id, position, task_key, kind, origin, child_instance_id, ` +
	`launch_state, launch_error, live_status, terminal_seq, terminal_at, result, failure, ` +
	`cancel_requested, blocking, continuing_independently`

// errNoDB is returned by every method of a repository constructed without a
// database, instead of a nil-pointer panic deep inside database/sql.
var errNoDB = errors.New("group run repository has no database")

func (r *GroupRunRepo) ready() error {
	if r == nil || r.db == nil {
		return errNoDB
	}
	return nil
}

// withTx runs fn in one transaction, committing only if fn succeeds.
func (r *GroupRunRepo) withTx(ctx context.Context, op string, fn func(tx *sql.Tx) error) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: begin transaction: %w", op, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: commit: %w", op, err)
	}
	return nil
}

// changed reports whether a conditional UPDATE matched at least one row —
// the CAS "did I win" signal.
func changed(res sql.Result) (bool, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reading rows affected: %w", err)
	}
	return n > 0, nil
}

// ---------------------------------------------------------------------------
// CreateWithTasks
// ---------------------------------------------------------------------------

// CreateWithTasks implements instance.GroupRunRepository.
//
// The run row and EVERY roster row are inserted in ONE transaction: the
// roster must be complete before any child is enqueued, and a crash must
// never leave a run with half a roster. The run is validated and normalized
// first (Attempt 0 => 1, State "" => launching, task GroupRunID/LaunchState/
// LiveStatus defaulted, Blocking forced true) and the normalized values plus
// CreatedAt/UpdatedAt are written back to run only after the commit.
func (r *GroupRunRepo) CreateWithTasks(ctx context.Context, run *instance.GroupRun) error {
	if run == nil {
		return fmt.Errorf("creating group run: run is nil")
	}
	norm, err := normalizeNewGroupRun(*run)
	if err != nil {
		return fmt.Errorf("creating group run: %w", err)
	}
	now := r.nowUTC()
	if norm.CreatedAt.IsZero() {
		norm.CreatedAt = now
	}
	norm.CreatedAt = norm.CreatedAt.UTC()
	norm.UpdatedAt = norm.CreatedAt

	configJSON, err := toJSONString(norm.Config)
	if err != nil {
		return fmt.Errorf("creating group run: marshalling config: %w", err)
	}
	ownershipJSON, err := ownershipToJSON(norm.BodyOwnership)
	if err != nil {
		return fmt.Errorf("creating group run: marshalling body ownership: %w", err)
	}
	startJSON, err := toJSONString(norm.StartVariables)
	if err != nil {
		return fmt.Errorf("creating group run: marshalling start variables: %w", err)
	}

	err = r.withTx(ctx, "creating group run", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO group_runs (
			id, parent_instance_id, node_id, attempt, form, completion_rule,
			unfinished_behavior, gather_mode, config, body_ownership, start_variables,
			workspace_snapshot, state, launch_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuidToString(norm.ID), uuidToString(norm.ParentInstanceID), norm.NodeID, norm.Attempt,
			norm.Form, norm.CompletionRule, norm.UnfinishedBehavior, norm.GatherMode,
			configJSON, ownershipJSON, startJSON, norm.WorkspaceSnapshot, string(norm.State),
			norm.LaunchError, groupTime(norm.CreatedAt), groupTime(norm.UpdatedAt))
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("creating group run for parent %s node %q attempt %d: %w",
					norm.ParentInstanceID, norm.NodeID, norm.Attempt, instance.ErrGroupRunExists)
			}
			return fmt.Errorf("creating group run: %w", err)
		}
		for _, task := range norm.Tasks {
			_, err := tx.ExecContext(ctx, `INSERT INTO group_tasks (
				group_run_id, position, task_key, kind, origin, child_instance_id,
				launch_state, launch_error, live_status, blocking, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
				uuidToString(norm.ID), task.Position, task.Key, string(task.Kind), task.Origin,
				uuidToString(task.ChildInstanceID), string(task.LaunchState), task.LaunchError,
				string(task.LiveStatus), groupTime(norm.CreatedAt))
			if err != nil {
				if isUniqueViolation(err) && strings.Contains(err.Error(), "child_instance_id") {
					return fmt.Errorf("creating group run: task %q: child instance %s already belongs to a group run: %w",
						task.Key, task.ChildInstanceID, err)
				}
				return fmt.Errorf("creating group run: task %q: %w", task.Key, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	*run = norm
	return nil
}

// normalizeNewGroupRun validates a run about to be created and fills its
// defaults. It returns a copy; the caller's run is untouched on error.
func normalizeNewGroupRun(run instance.GroupRun) (instance.GroupRun, error) {
	if run.ID == uuid.Nil {
		return run, fmt.Errorf("run id is required")
	}
	if run.ParentInstanceID == uuid.Nil {
		return run, fmt.Errorf("parent instance id is required")
	}
	if run.NodeID == "" {
		return run, fmt.Errorf("node id is required")
	}
	if run.Attempt == 0 {
		run.Attempt = 1
	}
	if run.Attempt < 1 {
		return run, fmt.Errorf("attempt must be >= 1, got %d", run.Attempt)
	}
	if run.State == "" {
		run.State = instance.GroupRunLaunching
	}
	if run.State != instance.GroupRunLaunching && run.State != instance.GroupRunRunning {
		return run, fmt.Errorf("a new group run must be launching or running, got %q", run.State)
	}
	if run.Decision != nil || run.FinalOutput != nil || run.ContinuationReady ||
		run.ContinuationDelivered || run.ActionsApplied || run.ContinuationPort != "" {
		return run, fmt.Errorf("a new group run cannot carry a decision, final output or continuation flags")
	}
	if len(run.Tasks) == 0 {
		return run, fmt.Errorf("a group run needs at least one task")
	}

	tasks := make([]instance.GroupTask, len(run.Tasks))
	keys := make(map[string]bool, len(run.Tasks))
	children := make(map[uuid.UUID]bool, len(run.Tasks))
	for i, t := range run.Tasks {
		if t.Position != i {
			return run, fmt.Errorf("task %d (%q): position must be %d (tasks are stored in author order)", i, t.Key, i)
		}
		if t.Key == "" {
			return run, fmt.Errorf("task %d: key is required", i)
		}
		if keys[t.Key] {
			return run, fmt.Errorf("task %q: duplicate key", t.Key)
		}
		keys[t.Key] = true
		if !t.Kind.Valid() {
			return run, fmt.Errorf("task %q: invalid kind %q", t.Key, t.Kind)
		}
		if t.ChildInstanceID == uuid.Nil {
			return run, fmt.Errorf("task %q: reserved child instance id is required", t.Key)
		}
		if children[t.ChildInstanceID] {
			return run, fmt.Errorf("task %q: duplicate child instance id %s", t.Key, t.ChildInstanceID)
		}
		children[t.ChildInstanceID] = true
		if t.GroupRunID == uuid.Nil {
			t.GroupRunID = run.ID
		}
		if t.GroupRunID != run.ID {
			return run, fmt.Errorf("task %q: belongs to group run %s, not %s", t.Key, t.GroupRunID, run.ID)
		}
		if t.LaunchState == "" {
			t.LaunchState = instance.GroupTaskPending
		}
		if t.LaunchState != instance.GroupTaskPending {
			return run, fmt.Errorf("task %q: a new roster entry must be pending, got %q", t.Key, t.LaunchState)
		}
		if t.LiveStatus == "" {
			t.LiveStatus = instance.GroupTaskStatusNotStarted
		}
		if t.LiveStatus != instance.GroupTaskStatusNotStarted {
			return run, fmt.Errorf("task %q: a new roster entry must be not_started, got %q", t.Key, t.LiveStatus)
		}
		if t.TerminalSeq != nil || t.TerminalAt != nil || t.Result != nil || t.Failure != nil ||
			t.CancelRequested || t.ContinuingIndependently || t.FinalizedRef != "" {
			return run, fmt.Errorf("task %q: a new roster entry cannot carry terminal or action state", t.Key)
		}
		t.Blocking = true
		tasks[i] = t
	}
	run.Tasks = tasks
	return run, nil
}

func (r *GroupRunRepo) nowUTC() time.Time {
	if r.now == nil {
		return time.Now().UTC()
	}
	return r.now().UTC()
}

// ownershipToJSON renders taskKey -> body node ids; nil/empty => "{}".
func ownershipToJSON(m map[string][]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ownershipFromJSON(s string) (map[string][]string, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	var m map[string][]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// Get implements instance.GroupRunRepository. The run and its roster are read
// in ONE transaction so they are a consistent snapshot.
func (r *GroupRunRepo) Get(ctx context.Context, id uuid.UUID) (*instance.GroupRun, error) {
	var out *instance.GroupRun
	err := r.withTx(ctx, "getting group run", func(tx *sql.Tx) error {
		run, err := loadGroupRun(ctx, tx, `WHERE id = ?`, uuidToString(id))
		if err != nil {
			return fmt.Errorf("getting group run %s: %w", id, err)
		}
		if run == nil {
			return fmt.Errorf("group run %s: %w", id, instance.ErrGroupRunNotFound)
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetByParentNode implements instance.GroupRunRepository: the LATEST attempt
// for (parent, node), or (nil, nil). "No run yet" is the executor's
// first-entry signal and is deliberately distinguishable from an error —
// treating a storage failure as "no run" would launch a second roster.
func (r *GroupRunRepo) GetByParentNode(ctx context.Context, parentID uuid.UUID, nodeID string) (*instance.GroupRun, error) {
	var out *instance.GroupRun
	err := r.withTx(ctx, "getting group run by parent node", func(tx *sql.Tx) error {
		run, err := loadGroupRun(ctx, tx,
			`WHERE parent_instance_id = ? AND node_id = ? ORDER BY attempt DESC LIMIT 1`,
			uuidToString(parentID), nodeID)
		if err != nil {
			return fmt.Errorf("getting group run of %s node %q: %w", parentID, nodeID, err)
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetByChild implements instance.GroupRunRepository: the run whose roster
// reserved childID, or (nil, nil) when childID is not a group task.
func (r *GroupRunRepo) GetByChild(ctx context.Context, childID uuid.UUID) (*instance.GroupRun, error) {
	var out *instance.GroupRun
	err := r.withTx(ctx, "getting group run by child", func(tx *sql.Tx) error {
		run, err := loadGroupRun(ctx, tx,
			`WHERE id = (SELECT group_run_id FROM group_tasks WHERE child_instance_id = ?)`,
			uuidToString(childID))
		if err != nil {
			return fmt.Errorf("getting group run of child %s: %w", childID, err)
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListByParent implements instance.GroupRunRepository: every run of parentID
// (oldest first), each with its roster. An empty result is an empty slice.
func (r *GroupRunRepo) ListByParent(ctx context.Context, parentID uuid.UUID) ([]instance.GroupRun, error) {
	out := []instance.GroupRun{}
	err := r.withTx(ctx, "listing group runs by parent", func(tx *sql.Tx) error {
		runs, err := queryGroupRuns(ctx, tx,
			`WHERE parent_instance_id = ? ORDER BY created_at ASC, attempt ASC, id ASC`, uuidToString(parentID))
		if err != nil {
			return fmt.Errorf("listing group runs of %s: %w", parentID, err)
		}
		if len(runs) == 0 {
			return nil
		}
		tasks, err := queryGroupTasks(ctx, tx,
			`WHERE group_run_id IN (SELECT id FROM group_runs WHERE parent_instance_id = ?)
			 ORDER BY group_run_id, position`, uuidToString(parentID))
		if err != nil {
			return fmt.Errorf("listing group runs of %s: %w", parentID, err)
		}
		byRun := map[uuid.UUID][]instance.GroupTask{}
		for _, t := range tasks {
			byRun[t.GroupRunID] = append(byRun[t.GroupRunID], t)
		}
		for i := range runs {
			runs[i].Tasks = byRun[runs[i].ID]
			if err := hydrateGather(ctx, tx, &runs[i]); err != nil {
				return fmt.Errorf("listing group runs of %s: %w", parentID, err)
			}
		}
		out = runs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListUnsettled includes delivered groups that still own active tasks under a
// failed/cancelled parent. Delivery settles output, not descendant containment.
// UNION preserves the ordinary partial-index query and deduplicates runs with
// multiple active children. Actual child state excludes purged/terminal tasks.
func (r *GroupRunRepo) ListUnsettled(ctx context.Context) ([]uuid.UUID, error) {
	if err := r.ready(); err != nil {
		return nil, fmt.Errorf("listing unsettled group runs: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM (
		SELECT id, created_at FROM group_runs
		 WHERE state <> 'done' OR continuation_delivered = 0 OR actions_applied = 0
		UNION
		SELECT g.id, g.created_at FROM group_runs g
		 JOIN instances p ON p.id = g.parent_instance_id
		 JOIN group_tasks t ON t.group_run_id = g.id
		 JOIN instances c ON c.id = t.child_instance_id
		 WHERE p.state IN ('failed','cancelled')
		   AND c.state IN ('queued','running','waiting','paused')
	) ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing unsettled group runs: %w", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("listing unsettled group runs: %w", err)
		}
		id, err := parseUUID(raw)
		if err != nil {
			return nil, fmt.Errorf("listing unsettled group runs: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing unsettled group runs: %w", err)
	}
	return out, nil
}

// loadGroupRun reads the single run selected by where/args plus its roster,
// or (nil, nil) when no run matches.
func loadGroupRun(ctx context.Context, q groupQueryer, where string, args ...any) (*instance.GroupRun, error) {
	runs, err := queryGroupRuns(ctx, q, where, args...)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	run := runs[0]
	tasks, err := queryGroupTasks(ctx, q, `WHERE group_run_id = ? ORDER BY position`, uuidToString(run.ID))
	if err != nil {
		return nil, err
	}
	run.Tasks = tasks
	if err := hydrateGather(ctx, q, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func queryGroupRuns(ctx context.Context, q groupQueryer, where string, args ...any) ([]instance.GroupRun, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+groupRunColumns+` FROM group_runs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []instance.GroupRun
	for rows.Next() {
		run, err := scanGroupRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanGroupRun(s rowScanner) (*instance.GroupRun, error) {
	var (
		idRaw, parentRaw, nodeID, form, rule, behavior, gather string
		configRaw, ownershipRaw, startRaw, snapshot, state     string
		decisionRaw, finalRaw                                  sql.NullString
		port, launchErr, createdRaw, updatedRaw                string
		attempt, ready, delivered, applied                     int
	)
	if err := s.Scan(&idRaw, &parentRaw, &nodeID, &attempt, &form, &rule, &behavior, &gather,
		&configRaw, &ownershipRaw, &startRaw, &snapshot, &state, &decisionRaw, &finalRaw,
		&port, &ready, &delivered, &applied, &launchErr, &createdRaw, &updatedRaw); err != nil {
		return nil, err
	}
	id, err := parseUUID(idRaw)
	if err != nil {
		return nil, fmt.Errorf("group run id: %w", err)
	}
	parentID, err := parseUUID(parentRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s parent id: %w", idRaw, err)
	}
	config, err := fromJSONString(configRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s config: %w", idRaw, err)
	}
	ownership, err := ownershipFromJSON(ownershipRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s body ownership: %w", idRaw, err)
	}
	start, err := fromJSONString(startRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s start variables: %w", idRaw, err)
	}
	var decision *instance.GroupDecision
	if decisionRaw.Valid && decisionRaw.String != "" {
		var d instance.GroupDecision
		if err := json.Unmarshal([]byte(decisionRaw.String), &d); err != nil {
			return nil, fmt.Errorf("group run %s decision: %w", idRaw, err)
		}
		decision = &d
	}
	final, err := fromNullJSONString(finalRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s final output: %w", idRaw, err)
	}
	createdAt, err := parseTime(createdRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s created_at: %w", idRaw, err)
	}
	updatedAt, err := parseTime(updatedRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s updated_at: %w", idRaw, err)
	}
	return &instance.GroupRun{
		ID:                    id,
		ParentInstanceID:      parentID,
		NodeID:                nodeID,
		Attempt:               attempt,
		Form:                  form,
		CompletionRule:        rule,
		UnfinishedBehavior:    behavior,
		GatherMode:            gather,
		Config:                config,
		BodyOwnership:         ownership,
		StartVariables:        start,
		WorkspaceSnapshot:     snapshot,
		State:                 instance.GroupRunState(state),
		Decision:              decision,
		FinalOutput:           final,
		ContinuationPort:      port,
		ContinuationReady:     ready == 1,
		ContinuationDelivered: delivered == 1,
		ActionsApplied:        applied == 1,
		LaunchError:           launchErr,
		CreatedAt:             createdAt,
		UpdatedAt:             updatedAt,
	}, nil
}

func queryGroupTasks(ctx context.Context, q groupQueryer, where string, args ...any) ([]instance.GroupTask, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+groupTaskColumns+` FROM group_tasks `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []instance.GroupTask
	for rows.Next() {
		var (
			runRaw, key, kind, origin, childRaw string
			launchState, launchErr, live        string
			position                            int
			seq                                 sql.NullInt64
			atRaw, resultRaw, failureRaw        sql.NullString
			cancel, blocking, independent       int
		)
		if err := rows.Scan(&runRaw, &position, &key, &kind, &origin, &childRaw, &launchState,
			&launchErr, &live, &seq, &atRaw, &resultRaw, &failureRaw, &cancel, &blocking, &independent); err != nil {
			return nil, err
		}
		runID, err := parseUUID(runRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q run id: %w", key, err)
		}
		childID, err := parseUUID(childRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q child id: %w", key, err)
		}
		at, err := nullStringToTimePtr(atRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q terminal_at: %w", key, err)
		}
		result, err := fromNullJSONString(resultRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q result: %w", key, err)
		}
		failure, err := fromNullJSONString(failureRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q failure: %w", key, err)
		}
		var seqPtr *int64
		if seq.Valid {
			v := seq.Int64
			seqPtr = &v
		}
		out = append(out, instance.GroupTask{
			GroupRunID:              runID,
			Position:                position,
			Key:                     key,
			Kind:                    instance.GroupTaskKind(kind),
			Origin:                  origin,
			ChildInstanceID:         childID,
			LaunchState:             instance.GroupTaskLaunchState(launchState),
			LaunchError:             launchErr,
			LiveStatus:              instance.GroupTaskStatus(live),
			TerminalSeq:             seqPtr,
			TerminalAt:              at,
			Result:                  result,
			Failure:                 failure,
			CancelRequested:         cancel == 1,
			Blocking:                blocking == 1,
			ContinuingIndependently: independent == 1,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Existence helpers (only consulted after a conditional UPDATE matched 0 rows)
// ---------------------------------------------------------------------------

func runExists(ctx context.Context, q groupQueryer, runID uuid.UUID) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_runs WHERE id = ?`,
		uuidToString(runID)).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// missingRunErr distinguishes "the CAS lost" (run exists => nil) from "no such
// run" (ErrGroupRunNotFound).
func missingRunErr(ctx context.Context, q groupQueryer, runID uuid.UUID) error {
	ok, err := runExists(ctx, q, runID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
	}
	return nil
}

// taskRow reads one roster row's launch state and live status.
func taskRow(ctx context.Context, q groupQueryer, runID uuid.UUID, key string) (launch, live string, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT launch_state, live_status FROM group_tasks
		WHERE group_run_id = ? AND task_key = ?`, uuidToString(runID), key).Scan(&launch, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return launch, live, true, nil
}

// missingTaskErr reports a missing task as ErrGroupRunNotFound when its run is
// missing too, otherwise ErrGroupTaskNotFound.
func missingTaskErr(ctx context.Context, q groupQueryer, runID uuid.UUID, key string) error {
	if err := missingRunErr(ctx, q, runID); err != nil {
		return err
	}
	return fmt.Errorf("group run %s task %q: %w", runID, key, instance.ErrGroupTaskNotFound)
}

// ---------------------------------------------------------------------------
// Launch bookkeeping
// ---------------------------------------------------------------------------

// MarkTaskLaunched implements instance.GroupRunRepository. Idempotent: an
// already-launched task stays launched. A task that never started is shown
// as queued from here on (its child was just enqueued); a live status the
// coordinator already recorded is left alone. A task whose launch already
// FAILED cannot be marked launched.
func (r *GroupRunRepo) MarkTaskLaunched(ctx context.Context, runID uuid.UUID, key string) error {
	return r.withTx(ctx, "marking group task launched", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE group_tasks
			SET launch_state = 'launched',
			    live_status = CASE WHEN live_status = 'not_started' THEN 'queued' ELSE live_status END,
			    updated_at = ?
			WHERE group_run_id = ? AND task_key = ? AND launch_state IN ('pending','launched')`,
			groupTime(r.nowUTC()), uuidToString(runID), key)
		if err != nil {
			return fmt.Errorf("marking group task %q launched: %w", key, err)
		}
		ok, err := changed(res)
		if err != nil {
			return fmt.Errorf("marking group task %q launched: %w", key, err)
		}
		if ok {
			return nil
		}
		launch, _, found, err := taskRow(ctx, tx, runID, key)
		if err != nil {
			return fmt.Errorf("marking group task %q launched: %w", key, err)
		}
		if !found {
			return missingTaskErr(ctx, tx, runID, key)
		}
		return fmt.Errorf("marking group task %q launched: launch state is already %q", key, launch)
	})
}

// MarkTaskLaunchFailed implements instance.GroupRunRepository. It records the
// task's reason and, if the run has no launch error yet, the FIRST failure on
// the run ("task <key>: <reason>"), so GroupRun.LaunchError names what broke
// the launch barrier. A task that was already launched cannot be marked
// failed — its child exists and will report its own terminal.
func (r *GroupRunRepo) MarkTaskLaunchFailed(ctx context.Context, runID uuid.UUID, key, reason string) error {
	if reason == "" {
		reason = "launch failed"
	}
	return r.withTx(ctx, "marking group task launch failed", func(tx *sql.Tx) error {
		now := groupTime(r.nowUTC())
		res, err := tx.ExecContext(ctx, `UPDATE group_tasks
			SET launch_state = 'launch_failed', launch_error = ?, updated_at = ?
			WHERE group_run_id = ? AND task_key = ? AND launch_state IN ('pending','launch_failed')`,
			reason, now, uuidToString(runID), key)
		if err != nil {
			return fmt.Errorf("marking group task %q launch failed: %w", key, err)
		}
		ok, err := changed(res)
		if err != nil {
			return fmt.Errorf("marking group task %q launch failed: %w", key, err)
		}
		if !ok {
			launch, _, found, err := taskRow(ctx, tx, runID, key)
			if err != nil {
				return fmt.Errorf("marking group task %q launch failed: %w", key, err)
			}
			if !found {
				return missingTaskErr(ctx, tx, runID, key)
			}
			return fmt.Errorf("marking group task %q launch failed: launch state is already %q", key, launch)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE group_runs
			SET launch_error = CASE WHEN launch_error = '' THEN ? ELSE launch_error END, updated_at = ?
			WHERE id = ?`, fmt.Sprintf("task %s: %s", key, reason), now, uuidToString(runID)); err != nil {
			return fmt.Errorf("recording group run launch error: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

// TransitionState implements instance.GroupRunRepository as a CAS from -> to.
//
// Two transitions are refused because a dedicated CAS owns them: entering
// `decided` (Decide writes the decision in the same statement — a decided run
// without a decision could never be decided again) and leaving `done` (done
// is final; its output is immutable). Entering `done` IS allowed: it is how a
// run under a cancelled/failed parent is settled without a continuation.
func (r *GroupRunRepo) TransitionState(ctx context.Context, runID uuid.UUID, from, to instance.GroupRunState) (bool, error) {
	if !from.Valid() || !to.Valid() {
		return false, fmt.Errorf("transitioning group run %s: invalid state %q -> %q", runID, from, to)
	}
	if from == to {
		return false, fmt.Errorf("transitioning group run %s: from and to are both %q", runID, from)
	}
	if to == instance.GroupRunDecided {
		return false, fmt.Errorf("transitioning group run %s: use Decide to enter %q", runID, to)
	}
	if from == instance.GroupRunDone {
		return false, fmt.Errorf("transitioning group run %s: %q is final", runID, from)
	}
	if err := r.ready(); err != nil {
		return false, fmt.Errorf("transitioning group run %s: %w", runID, err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE group_runs SET state = ?, updated_at = ?
		WHERE id = ? AND state = ?`, string(to), groupTime(r.nowUTC()), uuidToString(runID), string(from))
	if err != nil {
		return false, fmt.Errorf("transitioning group run %s: %w", runID, err)
	}
	ok, err := changed(res)
	if err != nil {
		return false, fmt.Errorf("transitioning group run %s: %w", runID, err)
	}
	if ok {
		return true, nil
	}
	if err := missingRunErr(ctx, r.db, runID); err != nil {
		return false, err
	}
	return false, nil
}

// RecordTaskTerminal is a TEST-FIXTURE SEAM, deliberately not part of
// instance.GroupRunRepository: nothing in production calls it.
//
// Production receipts are captured by the instances terminal triggers in the
// state writer's transaction. An existing child without a receipt is a storage
// error, never permission to invent callback-time order. Childless adapter
// fixtures may seed receipts using the SAME durable clock as those triggers.
// Replays return the stored sequence with first=false and preserve all payloads.
func (r *GroupRunRepo) RecordTaskTerminal(ctx context.Context, runID uuid.UUID, key string,
	status instance.GroupTaskStatus, result, failure map[string]any, at time.Time) (int64, bool, error) {
	if !status.IsTerminal() {
		return 0, false, fmt.Errorf("recording group task %q terminal: %q is not a terminal status", key, status)
	}
	resultJSON, err := toNullJSONString(result)
	if err != nil {
		return 0, false, fmt.Errorf("recording group task %q terminal: marshalling result: %w", key, err)
	}
	failureJSON, err := toNullJSONString(failure)
	if err != nil {
		return 0, false, fmt.Errorf("recording group task %q terminal: marshalling failure: %w", key, err)
	}
	if at.IsZero() {
		at = r.nowUTC()
	}

	var seq int64
	var first bool
	err = r.withTx(ctx, "recording group task terminal", func(tx *sql.Tx) error {
		// Acquire SQLite's writer lock before reading, just as the old single
		// conditional UPDATE did. No child can commit between the lookup and
		// allocation, even if repositories use separate database handles.
		if _, err := tx.ExecContext(ctx, `UPDATE group_terminal_clock SET value = value WHERE singleton = 1`); err != nil {
			return fmt.Errorf("recording group task %q terminal: lock clock: %w", key, err)
		}
		var stored sql.NullInt64
		var childID string
		err := tx.QueryRowContext(ctx, `SELECT terminal_seq, child_instance_id FROM group_tasks
			WHERE group_run_id = ? AND task_key = ?`, uuidToString(runID), key).Scan(&stored, &childID)
		if errors.Is(err, sql.ErrNoRows) {
			return missingTaskErr(ctx, tx, runID, key)
		}
		if err != nil {
			return fmt.Errorf("recording group task %q terminal: %w", key, err)
		}
		if stored.Valid {
			seq = stored.Int64
			return nil
		}
		var childCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM instances WHERE id = ?`, childID).Scan(&childCount); err != nil {
			return fmt.Errorf("recording group task %q terminal: load child: %w", key, err)
		}
		if childCount != 0 {
			return fmt.Errorf("recording group task %q terminal: production child has no atomic receipt; verify terminal persistence wiring", key)
		}
		if err := tx.QueryRowContext(ctx, `UPDATE group_terminal_clock SET value = value + 1
			WHERE singleton = 1 RETURNING value`).Scan(&seq); err != nil {
			return fmt.Errorf("recording group task %q terminal: allocate clock: %w", key, err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE group_tasks
			SET terminal_seq = ?,
			    terminal_at = ?, live_status = ?, result = ?, failure = ?, updated_at = ?
			WHERE group_run_id = ? AND task_key = ? AND terminal_seq IS NULL`,
			seq,
			groupTime(at), string(status), resultJSON, failureJSON, groupTime(r.nowUTC()),
			uuidToString(runID), key)
		if err != nil {
			return fmt.Errorf("recording group task %q terminal: %w", key, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("recording group task %q terminal: %w", key, err)
		}
		first = n == 1
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return seq, first, nil
}

// UpdateTaskLiveStatus implements instance.GroupRunRepository. Only
// non-terminal LIVE statuses are accepted (terminal ones and their seq are
// captured atomically by the state writer; "unfinished" is frozen-only), and a task
// whose live status is already terminal is left unchanged — a late "running"
// event can never resurrect a finished task.
func (r *GroupRunRepo) UpdateTaskLiveStatus(ctx context.Context, runID uuid.UUID, key string, status instance.GroupTaskStatus) error {
	if !status.IsLive() || status.IsTerminal() {
		return fmt.Errorf("updating group task %q live status: %q is not a non-terminal live status (terminals require an atomic receipt)", key, status)
	}
	return r.withTx(ctx, "updating group task live status", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE group_tasks SET live_status = ?, updated_at = ?
			WHERE group_run_id = ? AND task_key = ?
			  AND live_status NOT IN ('completed','failed','cancelled')`,
			string(status), groupTime(r.nowUTC()), uuidToString(runID), key)
		if err != nil {
			return fmt.Errorf("updating group task %q live status: %w", key, err)
		}
		ok, err := changed(res)
		if err != nil {
			return fmt.Errorf("updating group task %q live status: %w", key, err)
		}
		if ok {
			return nil
		}
		_, _, found, err := taskRow(ctx, tx, runID, key)
		if err != nil {
			return fmt.Errorf("updating group task %q live status: %w", key, err)
		}
		if !found {
			return missingTaskErr(ctx, tx, runID, key)
		}
		return nil // terminal status is sticky: not an error
	})
}

// Decide implements instance.GroupRunRepository.
//
// The CAS predicate is `state IN ('launching','running') AND decision IS
// NULL`; the winner's transaction also clears Blocking on every task, so no
// reader can ever see a decided run whose tasks still block the parent. A
// losing call (already decided, or any later state) returns (false, nil) and
// leaves the stored decision untouched — but losing the CAS is NOT permission
// for the caller to skip the decision's side effects (P2-T10 reconciles them).
func (r *GroupRunRepo) Decide(ctx context.Context, runID uuid.UUID, d instance.GroupDecision) (bool, error) {
	if d.Outcome != "success" && d.Outcome != "failure" {
		return false, fmt.Errorf("deciding group run %s: outcome must be \"success\" or \"failure\", got %q", runID, d.Outcome)
	}
	if d.DecidedAt.IsZero() {
		d.DecidedAt = r.nowUTC()
	}
	d.DecidedAt = d.DecidedAt.UTC()
	raw, err := json.Marshal(d)
	if err != nil {
		return false, fmt.Errorf("deciding group run %s: marshalling decision: %w", runID, err)
	}

	var won bool
	err = r.withTx(ctx, "deciding group run", func(tx *sql.Tx) error {
		now := groupTime(r.nowUTC())
		res, err := tx.ExecContext(ctx, `UPDATE group_runs
			SET state = 'decided', decision = ?, updated_at = ?
			WHERE id = ? AND state IN ('launching','running') AND decision IS NULL`,
			string(raw), now, uuidToString(runID))
		if err != nil {
			return fmt.Errorf("deciding group run %s: %w", runID, err)
		}
		ok, err := changed(res)
		if err != nil {
			return fmt.Errorf("deciding group run %s: %w", runID, err)
		}
		if !ok {
			return missingRunErr(ctx, tx, runID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE group_tasks SET blocking = 0, updated_at = ?
			WHERE group_run_id = ?`, now, uuidToString(runID)); err != nil {
			return fmt.Errorf("releasing blocking tasks of group run %s: %w", runID, err)
		}
		won = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return won, nil
}

// ---------------------------------------------------------------------------
// Post-decision actions
// ---------------------------------------------------------------------------

// setTaskFlag sets one boolean roster column (plus any extra assignments) for
// (runID, key), reporting a missing task as ErrGroupTaskNotFound.
func (r *GroupRunRepo) setTaskFlag(ctx context.Context, op string, runID uuid.UUID, key, assignments string) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE group_tasks SET `+assignments+`, updated_at = ?
		WHERE group_run_id = ? AND task_key = ?`, groupTime(r.nowUTC()), uuidToString(runID), key)
	if err != nil {
		return fmt.Errorf("%s %q: %w", op, key, err)
	}
	ok, err := changed(res)
	if err != nil {
		return fmt.Errorf("%s %q: %w", op, key, err)
	}
	if ok {
		return nil
	}
	return missingTaskErr(ctx, r.db, runID, key)
}

// MarkTaskCancelRequested implements instance.GroupRunRepository. It records
// cancellation INTENT only; the task's live status reports "cancelled" only
// once its child actually is.
func (r *GroupRunRepo) MarkTaskCancelRequested(ctx context.Context, runID uuid.UUID, key string) error {
	return r.setTaskFlag(ctx, "marking group task cancel requested", runID, key, `cancel_requested = 1`)
}

// MarkTaskContinuingIndependently implements instance.GroupRunRepository. A
// task continuing independently never blocks the parent again.
func (r *GroupRunRepo) MarkTaskContinuingIndependently(ctx context.Context, runID uuid.UUID, key string) error {
	return r.setTaskFlag(ctx, "marking group task continuing independently", runID, key,
		`continuing_independently = 1, blocking = 0`)
}

// setRunFlag sets one boolean run column, reporting a missing run as
// ErrGroupRunNotFound. Idempotent.
func (r *GroupRunRepo) setRunFlag(ctx context.Context, op string, runID uuid.UUID, column string) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE group_runs SET `+column+` = 1, updated_at = ? WHERE id = ?`,
		groupTime(r.nowUTC()), uuidToString(runID))
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, runID, err)
	}
	ok, err := changed(res)
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, runID, err)
	}
	if ok {
		return nil
	}
	return fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
}

// MarkActionsApplied implements instance.GroupRunRepository.
func (r *GroupRunRepo) MarkActionsApplied(ctx context.Context, runID uuid.UUID) error {
	return r.setRunFlag(ctx, "marking group run actions applied", runID, "actions_applied")
}

// MarkContinuationDelivered implements instance.GroupRunRepository.
func (r *GroupRunRepo) MarkContinuationDelivered(ctx context.Context, runID uuid.UUID) error {
	return r.setRunFlag(ctx, "marking group run continuation delivered", runID, "continuation_delivered")
}

// SetFinalOutput implements instance.GroupRunRepository as ONE conditional
// UPDATE: `state = from AND final_output IS NULL` -> state done, the output,
// the port and continuation_ready = 1. The output is written exactly once;
// a nil output is stored as an empty object so "set" stays distinguishable
// from "not yet set".
func (r *GroupRunRepo) SetFinalOutput(ctx context.Context, runID uuid.UUID, port string, output map[string]any, from instance.GroupRunState) (bool, error) {
	if port != "success" && port != "failure" {
		return false, fmt.Errorf("setting group run %s final output: port must be \"success\" or \"failure\", got %q", runID, port)
	}
	if !from.Valid() || from == instance.GroupRunDone {
		return false, fmt.Errorf("setting group run %s final output: invalid from state %q", runID, from)
	}
	outJSON, err := toJSONString(output)
	if err != nil {
		return false, fmt.Errorf("setting group run %s final output: marshalling: %w", runID, err)
	}
	if err := r.ready(); err != nil {
		return false, fmt.Errorf("setting group run %s final output: %w", runID, err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE group_runs
		SET state = 'done', final_output = ?, continuation_port = ?, continuation_ready = 1, updated_at = ?
		WHERE id = ? AND state = ? AND final_output IS NULL`,
		outJSON, port, groupTime(r.nowUTC()), uuidToString(runID), string(from))
	if err != nil {
		return false, fmt.Errorf("setting group run %s final output: %w", runID, err)
	}
	ok, err := changed(res)
	if err != nil {
		return false, fmt.Errorf("setting group run %s final output: %w", runID, err)
	}
	if ok {
		return true, nil
	}
	if err := missingRunErr(ctx, r.db, runID); err != nil {
		return false, err
	}
	return false, nil
}
