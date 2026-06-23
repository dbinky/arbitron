package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

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
