package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// Compile-time: satisfies the runner registry port.
var _ runner.Registry = (*RunnerRegistryRepo)(nil)

// RunnerRegistryRepo implements runner.Registry over SQLite (migration 041).
type RunnerRegistryRepo struct {
	db  *sql.DB
	now func() time.Time
}

// NewRunnerRegistryRepo creates a RunnerRegistryRepo.
func NewRunnerRegistryRepo(db *sql.DB) *RunnerRegistryRepo {
	return &RunnerRegistryRepo{db: db, now: time.Now}
}

const runnerColumns = `id, name, built_in, step_limit, labels, cordoned, draining, compat_state, compat_reason, ` +
	`liveness, last_contact, os, runner_version, harness_versions, workspace_root, created_at`

const sourceColumns = `id, runner_id, kind, credential_ref, unavailable_reason, families, backends, models, auth, capacity, reset_at, ` +
	`headroom, windows, congested, congested_until, last_probe_at, last_outcome, detail, confirmation_model, revision, capabilities`

func scanRunner(s rowScanner) (runner.Runner, error) {
	var r runner.Runner
	var id, labels, compatState, liveness, harness, created string
	var builtIn, cordoned, draining int
	var lastContact sql.NullString
	if err := s.Scan(&id, &r.Name, &builtIn, &r.StepLimit, &labels, &cordoned, &draining, &compatState, &r.Compat.Reason,
		&liveness, &lastContact, &r.OS, &r.RunnerVersion, &harness, &r.WorkspaceRoot, &created); err != nil {
		return runner.Runner{}, err
	}
	r.ID = runner.RunnerID(id)
	r.BuiltIn, r.Cordoned, r.Draining = builtIn != 0, cordoned != 0, draining != 0
	r.Compat.State = runner.CompatState(compatState)
	r.Liveness = runner.Liveness(liveness)
	var err error
	if r.Labels, err = extrasFromJSON(labels); err != nil {
		return runner.Runner{}, fmt.Errorf("decoding runner labels: %w", err)
	}
	if r.HarnessVersions, err = extrasFromJSON(harness); err != nil {
		return runner.Runner{}, fmt.Errorf("decoding harness versions: %w", err)
	}
	if r.LastContact, err = nullStringToTimePtr(lastContact); err != nil {
		return runner.Runner{}, err
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return runner.Runner{}, err
	}
	return r, nil
}

func scanSource(s rowScanner) (runner.Source, error) {
	var src runner.Source
	var id, runnerID, kind, families, backends, models, auth, capacity, windows, outcome, capabilities string
	var resetAt, congestedUntil, lastProbe sql.NullString
	var headroom sql.NullFloat64
	var congested int
	if err := s.Scan(&id, &runnerID, &kind, &src.CredentialRef, &src.UnavailableReason, &families, &backends, &models, &auth, &capacity, &resetAt,
		&headroom, &windows, &congested, &congestedUntil, &lastProbe, &outcome, &src.Health.Detail, &src.Health.ConfirmationModel, &src.Health.Revision, &capabilities); err != nil {
		return runner.Source{}, err
	}
	src.ID, src.RunnerID, src.Kind = runner.SourceID(id), runner.RunnerID(runnerID), runner.CredentialKind(kind)
	if err := decodeList(families, &src.Families); err != nil {
		return runner.Source{}, err
	}
	if err := decodeList(backends, &src.Backends); err != nil {
		return runner.Source{}, err
	}
	if err := decodeList(models, &src.Models); err != nil {
		return runner.Source{}, err
	}
	if err := decodeList(capabilities, &src.Capabilities); err != nil {
		return runner.Source{}, err
	}
	h := &src.Health
	h.Auth, h.Capacity, h.LastOutcome = runner.AuthState(auth), runner.CapacityState(capacity), runner.OutcomeClass(outcome)
	h.Congested = congested != 0
	if headroom.Valid {
		v := headroom.Float64
		h.Headroom = &v
	}
	if err := decodeList(windows, &h.Windows); err != nil {
		return runner.Source{}, err
	}
	var err error
	if h.ResetAt, err = nullStringToTimePtr(resetAt); err != nil {
		return runner.Source{}, err
	}
	if h.CongestedUntil, err = nullStringToTimePtr(congestedUntil); err != nil {
		return runner.Source{}, err
	}
	if h.LastProbeAt, err = nullStringToTimePtr(lastProbe); err != nil {
		return runner.Source{}, err
	}
	return src, nil
}

// decodeList decodes a JSON array column; "[]" and "" decode to a nil slice.
func decodeList[T any](raw string, dst *[]T) error {
	if raw == "" || raw == "[]" {
		*dst = nil
		return nil
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("decoding JSON list: %w", err)
	}
	return nil
}

func encodeList[T any](v []T) (string, error) {
	if len(v) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encoding JSON list: %w", err)
	}
	return string(b), nil
}

func headroomArg(h *float64) sql.NullFloat64 {
	if h == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *h, Valid: true}
}

// Snapshot reads runners, sources and live slot counts in one transaction.
func (r *RunnerRegistryRepo) Snapshot(ctx context.Context) (runner.PoolSnapshot, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return runner.PoolSnapshot{}, fmt.Errorf("beginning snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	runners, err := listRunners(ctx, tx)
	if err != nil {
		return runner.PoolSnapshot{}, err
	}
	sources, err := listSources(ctx, tx, `SELECT `+sourceColumns+` FROM runner_sources ORDER BY runner_id, id`)
	if err != nil {
		return runner.PoolSnapshot{}, err
	}
	snap := runner.PoolSnapshot{
		Runners: runners, Sources: sources,
		SlotsInUse: map[runner.RunnerID]int{}, SourceInFlight: map[runner.SourceID]int{},
		Telemetry: map[runner.RunnerID]runner.Telemetry{}, Homes: map[uuid.UUID]runner.Home{},
	}
	rows, err := tx.QueryContext(ctx, `SELECT runner_id, COALESCE(source_id, ''), COUNT(*) FROM runner_jobs
		WHERE state IN ('offered', 'running') AND runner_id IS NOT NULL GROUP BY runner_id, source_id`)
	if err != nil {
		return runner.PoolSnapshot{}, fmt.Errorf("counting slots: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rid, sid string
		var n int
		if err := rows.Scan(&rid, &sid, &n); err != nil {
			return runner.PoolSnapshot{}, fmt.Errorf("scanning slot count: %w", err)
		}
		snap.SlotsInUse[runner.RunnerID(rid)] += n
		if sid != "" {
			snap.SourceInFlight[runner.SourceID(sid)] += n
		}
	}
	if err := rows.Err(); err != nil {
		return runner.PoolSnapshot{}, err
	}
	return snap, nil
}

func listRunners(ctx context.Context, q groupQueryer) ([]runner.Runner, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+runnerColumns+` FROM runners ORDER BY built_in DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("listing runners: %w", err)
	}
	defer rows.Close()
	var out []runner.Runner
	for rows.Next() {
		rn, err := scanRunner(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning runner: %w", err)
		}
		out = append(out, rn)
	}
	return out, rows.Err()
}

func listSources(ctx context.Context, q groupQueryer, query string, args ...any) ([]runner.Source, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing sources: %w", err)
	}
	defer rows.Close()
	var out []runner.Source
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning source: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListRunners returns every runner, built-in first.
func (r *RunnerRegistryRepo) ListRunners(ctx context.Context) ([]runner.Runner, error) {
	return listRunners(ctx, r.db)
}

// GetRunner returns one runner and its sources.
func (r *RunnerRegistryRepo) GetRunner(ctx context.Context, id runner.RunnerID) (runner.Runner, []runner.Source, error) {
	rn, err := scanRunner(r.db.QueryRowContext(ctx, `SELECT `+runnerColumns+` FROM runners WHERE id = ?`, string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return runner.Runner{}, nil, fmt.Errorf("%w: %s", runner.ErrRunnerNotFound, id)
	}
	if err != nil {
		return runner.Runner{}, nil, fmt.Errorf("reading runner: %w", err)
	}
	sources, err := listSources(ctx, r.db, `SELECT `+sourceColumns+` FROM runner_sources WHERE runner_id = ? ORDER BY id`, string(id))
	if err != nil {
		return runner.Runner{}, nil, err
	}
	return rn, sources, nil
}

// UpsertRunner registers a runner or refreshes its machine facts. Operator-owned
// configuration (step limit, labels, cordon, drain) is written only on insert.
func (r *RunnerRegistryRepo) UpsertRunner(ctx context.Context, rn runner.Runner) error {
	if rn.ID == "" {
		return errors.New("runner registry: runner id is required")
	}
	if !runner.ValidStepLimit(rn.StepLimit) {
		return fmt.Errorf("%w: %d", runner.ErrInvalidStepLimit, rn.StepLimit)
	}
	labels, err := extrasToJSON(rn.Labels)
	if err != nil {
		return err
	}
	harness, err := extrasToJSON(rn.HarnessVersions)
	if err != nil {
		return err
	}
	compat := rn.Compat.State
	if compat == "" {
		compat = runner.CompatCompatible
	}
	liveness := rn.Liveness
	if liveness == "" {
		liveness = runner.LivenessOffline
	}
	now := r.now().UTC()
	created := rn.CreatedAt
	if created.IsZero() {
		created = now
	}
	name := rn.Name
	if name == "" {
		name = string(rn.ID)
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO runners (`+runnerColumns+`, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, built_in = excluded.built_in,
			compat_state = excluded.compat_state, compat_reason = excluded.compat_reason,
			liveness = excluded.liveness, last_contact = excluded.last_contact,
			os = excluded.os, runner_version = excluded.runner_version,
			harness_versions = excluded.harness_versions, workspace_root = excluded.workspace_root,
			updated_at = excluded.updated_at`,
		string(rn.ID), name, boolToInt(rn.BuiltIn), rn.StepLimit, labels, boolToInt(rn.Cordoned), boolToInt(rn.Draining),
		string(compat), rn.Compat.Reason, string(liveness), timeToNullString(rn.LastContact), rn.OS, rn.RunnerVersion,
		harness, rn.WorkspaceRoot, timeToString(created), timeToString(now))
	if err != nil {
		return fmt.Errorf("upserting runner: %w", err)
	}
	return nil
}

// UpdateConfig applies an operator patch atomically and returns the runner.
func (r *RunnerRegistryRepo) UpdateConfig(ctx context.Context, id runner.RunnerID, p runner.RunnerConfigPatch) (runner.Runner, error) {
	if p.StepLimit != nil && !runner.ValidStepLimit(*p.StepLimit) {
		return runner.Runner{}, fmt.Errorf("%w: %d", runner.ErrInvalidStepLimit, *p.StepLimit)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return runner.Runner{}, fmt.Errorf("beginning config update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := timeToString(r.now())
	if err := requireRunnerRow(ctx, tx, id); err != nil {
		return runner.Runner{}, err
	}
	if p.StepLimit != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE runners SET step_limit = ?, updated_at = ? WHERE id = ?`, *p.StepLimit, now, string(id)); err != nil {
			return runner.Runner{}, fmt.Errorf("updating step limit: %w", err)
		}
	}
	if p.Labels != nil {
		labels, err := extrasToJSON(p.Labels)
		if err != nil {
			return runner.Runner{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runners SET labels = ?, updated_at = ? WHERE id = ?`, labels, now, string(id)); err != nil {
			return runner.Runner{}, fmt.Errorf("updating labels: %w", err)
		}
	}
	rn, err := scanRunner(tx.QueryRowContext(ctx, `SELECT `+runnerColumns+` FROM runners WHERE id = ?`, string(id)))
	if err != nil {
		return runner.Runner{}, fmt.Errorf("reading runner: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return runner.Runner{}, fmt.Errorf("committing config update: %w", err)
	}
	return rn, nil
}

func requireRunnerRow(ctx context.Context, q groupQueryer, id runner.RunnerID) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM runners WHERE id = ?`, string(id)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", runner.ErrRunnerNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("reading runner: %w", err)
	}
	return nil
}

func (r *RunnerRegistryRepo) updateRunner(ctx context.Context, id runner.RunnerID, set string, args ...any) error {
	args = append(args, timeToString(r.now()), string(id))
	res, err := r.db.ExecContext(ctx, `UPDATE runners SET `+set+`, updated_at = ? WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("updating runner: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return fmt.Errorf("%w: %s", runner.ErrRunnerNotFound, id)
	}
	return nil
}

func (r *RunnerRegistryRepo) SetCordoned(ctx context.Context, id runner.RunnerID, cordoned bool) error {
	return r.updateRunner(ctx, id, `cordoned = ?`, boolToInt(cordoned))
}

func (r *RunnerRegistryRepo) SetDraining(ctx context.Context, id runner.RunnerID, draining bool) error {
	return r.updateRunner(ctx, id, `draining = ?`, boolToInt(draining))
}

func (r *RunnerRegistryRepo) SetLiveness(ctx context.Context, id runner.RunnerID, l runner.Liveness, at time.Time) error {
	return r.updateRunner(ctx, id, `liveness = ?, last_contact = ?`, string(l), timeToString(at))
}

func (r *RunnerRegistryRepo) SetCompat(ctx context.Context, id runner.RunnerID, c runner.Compatibility) error {
	return r.updateRunner(ctx, id, `compat_state = ?, compat_reason = ?`, string(c.State), c.Reason)
}

// ReplaceSources sets the runner's advertised sources. Existing IDs keep their
// health (only advertised facts are refreshed); new IDs start unknown/available
// unless the caller supplies health; missing IDs are deleted.
func (r *RunnerRegistryRepo) ReplaceSources(ctx context.Context, id runner.RunnerID, sources []runner.Source) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning source replace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireRunnerRow(ctx, tx, id); err != nil {
		return err
	}
	now := timeToString(r.now())
	keep := map[runner.SourceID]bool{}
	for _, s := range sources {
		want := runner.MakeSourceID(id, s.Kind, s.CredentialRef)
		if s.ID == "" {
			s.ID = want
		}
		if s.ID != want {
			return fmt.Errorf("runner registry: source id %q is not its identity %q", s.ID, want)
		}
		h := s.Health
		if h.Auth == "" {
			h.Auth = runner.AuthUnknown
		}
		if h.Capacity == "" {
			h.Capacity = runner.CapacityAvailable
		}
		families, err := encodeList(s.Families)
		if err != nil {
			return err
		}
		backends, err := encodeList(s.Backends)
		if err != nil {
			return err
		}
		models, err := encodeList(s.Models)
		if err != nil {
			return err
		}
		capabilities, err := encodeList(s.Capabilities)
		if err != nil {
			return err
		}
		windows, err := encodeList(h.Windows)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runner_sources (`+sourceColumns+`, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET unavailable_reason = excluded.unavailable_reason, families = excluded.families, backends = excluded.backends,
				models = excluded.models, capabilities = excluded.capabilities, updated_at = excluded.updated_at`,
			string(s.ID), string(id), string(s.Kind), s.CredentialRef, s.UnavailableReason, families, backends, models,
			string(h.Auth), string(h.Capacity), timeToNullString(h.ResetAt), headroomArg(h.Headroom), windows,
			boolToInt(h.Congested), timeToNullString(h.CongestedUntil), timeToNullString(h.LastProbeAt),
			string(h.LastOutcome), h.Detail, h.ConfirmationModel, h.Revision, capabilities, now); err != nil {
			return fmt.Errorf("upserting source %s: %w", s.ID, err)
		}
		keep[s.ID] = true
	}
	existing, err := listSources(ctx, tx, `SELECT `+sourceColumns+` FROM runner_sources WHERE runner_id = ?`, string(id))
	if err != nil {
		return err
	}
	for _, s := range existing {
		if keep[s.ID] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM runner_sources WHERE id = ?`, string(s.ID)); err != nil {
			return fmt.Errorf("deleting source %s: %w", s.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing source replace: %w", err)
	}
	return nil
}

// RecordHealth overwrites one source's health.
func (r *RunnerRegistryRepo) RecordHealth(ctx context.Context, id runner.SourceID, h runner.SourceHealth) error {
	windows, err := encodeList(h.Windows)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE runner_sources SET auth = ?, capacity = ?, reset_at = ?, headroom = ?, windows = ?,
		congested = ?, congested_until = ?, last_probe_at = ?, last_outcome = ?, detail = ?, confirmation_model = ?, revision = ?, updated_at = ? WHERE id = ?`,
		string(h.Auth), string(h.Capacity), timeToNullString(h.ResetAt), headroomArg(h.Headroom), windows,
		boolToInt(h.Congested), timeToNullString(h.CongestedUntil), timeToNullString(h.LastProbeAt),
		string(h.LastOutcome), h.Detail, h.ConfirmationModel, h.Revision, timeToString(r.now()), string(id))
	if err != nil {
		return fmt.Errorf("recording source health: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return fmt.Errorf("%w: %s", runner.ErrSourceNotFound, id)
	}
	return nil
}

// GetSource reads one source with its health.
func (r *RunnerRegistryRepo) GetSource(ctx context.Context, id runner.SourceID) (runner.Source, error) {
	s, err := scanSource(r.db.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM runner_sources WHERE id = ?`, string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return runner.Source{}, fmt.Errorf("%w: %s", runner.ErrSourceNotFound, id)
	}
	if err != nil {
		return runner.Source{}, fmt.Errorf("reading source: %w", err)
	}
	return s, nil
}
