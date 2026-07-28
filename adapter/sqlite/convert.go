package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

func uuidToString(id uuid.UUID) string {
	return id.String()
}

func parseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parsing uuid %q: %w", s, err)
	}
	return id, nil
}

func timeToString(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing time %q: %w", s, err)
	}
	return t, nil
}

func timeToNullString(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: timeToString(*t), Valid: true}
}

func nullStringToTimePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func stringToNullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: true}
}

func ptrToNullString(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func nullStringToPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

// nullStringToString returns the string value of ns, or "" when NULL.
func nullStringToString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// deptToNullString maps an instance department to a nullable SQL string: an
// empty department (private/default group) becomes SQL NULL, a non-empty one a
// valid string. The round-trip reads NULL back as "" via nullStringToString.
func deptToNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// scheduleIDToNullString maps a nilable schedule UUID to a nullable SQL string:
// nil (manual run) => SQL NULL; a value => its canonical UUID string.
func scheduleIDToNullString(id *uuid.UUID) sql.NullString {
	if id == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: id.String(), Valid: true}
}

// nullStringToScheduleID maps a nullable SQL string back to *uuid.UUID; NULL =>
// nil. A deleted schedule's id is still a valid UUID string, so this round-trips
// it.
func nullStringToScheduleID(ns sql.NullString) (*uuid.UUID, error) {
	if !ns.Valid {
		return nil, nil
	}
	id, err := uuid.Parse(ns.String)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// workflowIDToNullString maps a stable workflow UUID to a nullable SQL string:
// uuid.Nil (no id reference — legacy or local mode) => SQL NULL; a value => its
// canonical UUID string. It is the inverse of nullStringToWorkflowID.
func workflowIDToNullString(id uuid.UUID) sql.NullString {
	if id == uuid.Nil {
		return sql.NullString{}
	}
	return sql.NullString{String: id.String(), Valid: true}
}

// nullStringToWorkflowID maps a nullable workflow_id column back to a uuid.UUID:
// NULL (or empty) => uuid.Nil; otherwise the parsed UUID.
func nullStringToWorkflowID(ns sql.NullString) (uuid.UUID, error) {
	if !ns.Valid || ns.String == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(ns.String)
}

func intPtrToNullInt64(i *int) sql.NullInt64 {
	if i == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*i), Valid: true}
}

func nullInt64ToIntPtr(ni sql.NullInt64) *int {
	if !ni.Valid {
		return nil
	}
	v := int(ni.Int64)
	return &v
}

func toJSONString(m map[string]any) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func fromJSONString(s string) (map[string]any, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func extrasToJSON(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func extrasFromJSON(s string) (map[string]string, error) {
	if s == "" || s == "{}" {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

func toNullJSONString(m map[string]any) (sql.NullString, error) {
	if m == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func fromNullJSONString(ns sql.NullString) (map[string]any, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(ns.String), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// parentIDToNullString maps a nilable parent instance UUID to a nullable SQL
// string: nil (a root run) => SQL NULL; a value => its canonical UUID string.
// It is the inverse of nullStringToParentID.
func parentIDToNullString(id *uuid.UUID) sql.NullString {
	if id == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: id.String(), Valid: true}
}

// nullStringToSpawnLink maps the three nullable spawn-link columns back to the
// domain triple, enforcing the ALL-OR-NONE invariant on the READ side.
//
// The write side already refuses a partial link (InstanceRepo.Create writes all
// three columns or all three NULLs) and the schema cannot express the
// constraint without rebuilding the hottest table in the database — which is
// out of scope while there is exactly one writer. That leaves the read side as
// the place to make a partial row HARMLESS rather than merely unlikely: a row
// with a parent id but no node id (hand-edited, restored from a partial backup,
// or written by some future second writer) would otherwise read back as a
// half-populated link that IsRoot() calls non-root while the wake can never
// match it — a parent hung forever.
//
// An incomplete tuple therefore collapses to NO LINK — the run reads as a root,
// which is the one interpretation every read path already handles safely. A
// malformed parent id is still an ERROR: that is a corrupt value, not a missing
// one, and silently rooting it would hide real corruption.
func nullStringToSpawnLink(parentRaw, nodeRaw, modeRaw sql.NullString) (*uuid.UUID, *string, instance.SpawnMode, error) {
	parentID, err := nullStringToParentID(parentRaw)
	if err != nil {
		return nil, nil, "", err
	}
	nodeID := nullStringToPtr(nodeRaw)
	mode := instance.SpawnMode(nullStringToString(modeRaw))

	if parentID == nil || nodeID == nil || *nodeID == "" || !mode.Valid() {
		return nil, nil, "", nil
	}
	return parentID, nodeID, mode, nil
}

// nullStringToParentID maps a nullable parent_instance_id column back to
// *uuid.UUID: NULL or empty => nil (a root run). A deleted parent's id is still
// a valid UUID string, so this round-trips a dangling link too — there is no FK
// on the column by design.
func nullStringToParentID(ns sql.NullString) (*uuid.UUID, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	id, err := uuid.Parse(ns.String)
	if err != nil {
		return nil, fmt.Errorf("parsing parent_instance_id: %w", err)
	}
	return &id, nil
}
