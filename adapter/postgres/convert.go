package postgres

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// toPgUUID converts a google/uuid.UUID to a pgtype.UUID.
func toPgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// fromPgUUID converts a pgtype.UUID to a google/uuid.UUID.
func fromPgUUID(id pgtype.UUID) uuid.UUID {
	return uuid.UUID(id.Bytes)
}

// toPgTimestamptz converts a time.Time to a pgtype.Timestamptz.
func toPgTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// toPgTimestamptzPtr converts a *time.Time to a pgtype.Timestamptz (null if nil).
func toPgTimestamptzPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// fromPgTimestamptz converts a pgtype.Timestamptz to a time.Time.
func fromPgTimestamptz(ts pgtype.Timestamptz) time.Time {
	return ts.Time
}

// fromPgTimestamptzPtr converts a pgtype.Timestamptz to *time.Time (nil if not valid).
func fromPgTimestamptzPtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

// toPgText converts a string to a pgtype.Text.
func toPgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

// toPgTextPtr converts a *string to a pgtype.Text (null if nil).
func toPgTextPtr(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// fromPgTextPtr converts a pgtype.Text to *string (nil if not valid).
func fromPgTextPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

// toPgInt4Ptr converts a *int to a pgtype.Int4 (null if nil).
func toPgInt4Ptr(i *int) pgtype.Int4 {
	if i == nil {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: int32(*i), Valid: true}
}

// fromPgInt4Ptr converts a pgtype.Int4 to *int (nil if not valid).
func fromPgInt4Ptr(i pgtype.Int4) *int {
	if !i.Valid {
		return nil
	}
	v := int(i.Int32)
	return &v
}

// toJSON marshals a map to JSON bytes. Returns nil if the map is nil.
func toJSON(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

// fromJSON unmarshals JSON bytes into a map. Returns nil if data is nil or empty.
func fromJSON(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
