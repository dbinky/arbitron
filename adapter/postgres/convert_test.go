package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToPgUUID(t *testing.T) {
	id := uuid.New()
	pg := toPgUUID(id)
	assert.True(t, pg.Valid)
	assert.Equal(t, [16]byte(id), pg.Bytes)
}

func TestFromPgUUID(t *testing.T) {
	id := uuid.New()
	pg := pgtype.UUID{Bytes: id, Valid: true}
	result := fromPgUUID(pg)
	assert.Equal(t, id, result)
}

func TestUUIDRoundTrip(t *testing.T) {
	id := uuid.New()
	assert.Equal(t, id, fromPgUUID(toPgUUID(id)))
}

func TestToPgTimestamptz(t *testing.T) {
	now := time.Now()
	pg := toPgTimestamptz(now)
	assert.True(t, pg.Valid)
	assert.Equal(t, now, pg.Time)
}

func TestFromPgTimestamptz(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		now := time.Now()
		pg := pgtype.Timestamptz{Time: now, Valid: true}
		assert.Equal(t, now, fromPgTimestamptz(pg))
	})
	t.Run("invalid returns zero time", func(t *testing.T) {
		pg := pgtype.Timestamptz{Valid: false}
		assert.Equal(t, time.Time{}, fromPgTimestamptz(pg))
	})
}

func TestTimestamptzRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Microsecond)
	assert.Equal(t, now, fromPgTimestamptz(toPgTimestamptz(now)))
}

func TestToPgTimestamptzPtr(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		pg := toPgTimestamptzPtr(nil)
		assert.False(t, pg.Valid)
	})
	t.Run("non-nil", func(t *testing.T) {
		now := time.Now()
		pg := toPgTimestamptzPtr(&now)
		assert.True(t, pg.Valid)
		assert.Equal(t, now, pg.Time)
	})
}

func TestFromPgTimestamptzPtr(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		pg := pgtype.Timestamptz{Valid: false}
		result := fromPgTimestamptzPtr(pg)
		assert.Nil(t, result)
	})
	t.Run("valid", func(t *testing.T) {
		now := time.Now()
		pg := pgtype.Timestamptz{Time: now, Valid: true}
		result := fromPgTimestamptzPtr(pg)
		require.NotNil(t, result)
		assert.Equal(t, now, *result)
	})
}

func TestToPgText(t *testing.T) {
	pg := toPgText("hello")
	assert.True(t, pg.Valid)
	assert.Equal(t, "hello", pg.String)
}

func TestToPgTextPtr(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		pg := toPgTextPtr(nil)
		assert.False(t, pg.Valid)
	})
	t.Run("non-nil", func(t *testing.T) {
		s := "hello"
		pg := toPgTextPtr(&s)
		assert.True(t, pg.Valid)
		assert.Equal(t, "hello", pg.String)
	})
}

func TestFromPgTextPtr(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		pg := pgtype.Text{Valid: false}
		result := fromPgTextPtr(pg)
		assert.Nil(t, result)
	})
	t.Run("valid", func(t *testing.T) {
		pg := pgtype.Text{String: "hello", Valid: true}
		result := fromPgTextPtr(pg)
		require.NotNil(t, result)
		assert.Equal(t, "hello", *result)
	})
}

func TestToPgInt4Ptr(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		pg := toPgInt4Ptr(nil)
		assert.False(t, pg.Valid)
	})
	t.Run("non-nil", func(t *testing.T) {
		v := 42
		pg := toPgInt4Ptr(&v)
		assert.True(t, pg.Valid)
		assert.Equal(t, int32(42), pg.Int32)
	})
}

func TestFromPgInt4Ptr(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		pg := pgtype.Int4{Valid: false}
		result := fromPgInt4Ptr(pg)
		assert.Nil(t, result)
	})
	t.Run("valid", func(t *testing.T) {
		pg := pgtype.Int4{Int32: 42, Valid: true}
		result := fromPgInt4Ptr(pg)
		require.NotNil(t, result)
		assert.Equal(t, 42, *result)
	})
}

func TestToJSON(t *testing.T) {
	t.Run("nil map", func(t *testing.T) {
		data, err := toJSON(nil)
		require.NoError(t, err)
		assert.Nil(t, data)
	})
	t.Run("non-nil map", func(t *testing.T) {
		m := map[string]any{"key": "value", "num": float64(42)}
		data, err := toJSON(m)
		require.NoError(t, err)
		assert.NotNil(t, data)
		assert.Contains(t, string(data), `"key":"value"`)
	})
}

func TestFromJSON(t *testing.T) {
	t.Run("nil data", func(t *testing.T) {
		m, err := fromJSON(nil)
		require.NoError(t, err)
		assert.Nil(t, m)
	})
	t.Run("empty data", func(t *testing.T) {
		m, err := fromJSON([]byte{})
		require.NoError(t, err)
		assert.Nil(t, m)
	})
	t.Run("valid JSON", func(t *testing.T) {
		data := []byte(`{"key":"value","num":42}`)
		m, err := fromJSON(data)
		require.NoError(t, err)
		assert.Equal(t, "value", m["key"])
		assert.Equal(t, float64(42), m["num"])
	})
	t.Run("invalid JSON", func(t *testing.T) {
		data := []byte(`not json`)
		_, err := fromJSON(data)
		assert.Error(t, err)
	})
}

func TestJSONRoundTrip(t *testing.T) {
	original := map[string]any{
		"name":   "test",
		"count":  float64(5),
		"nested": map[string]any{"inner": "value"},
	}

	data, err := toJSON(original)
	require.NoError(t, err)

	result, err := fromJSON(data)
	require.NoError(t, err)
	assert.Equal(t, original, result)
}
