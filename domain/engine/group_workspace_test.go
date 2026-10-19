package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
)

// The engine sentinel and the instance sentinel are ONE error value, so an
// adapter that wraps instance.ErrUnsupportedSnapshotContent (it cannot import
// this package) is recognized by engine code testing
// ErrUnsupportedSnapshotContent, and vice versa.
func TestErrUnsupportedSnapshotContentIsTheInstanceSentinel(t *testing.T) {
	assert.Equal(t, "unsupported content in working area snapshot", ErrUnsupportedSnapshotContent.Error())

	fromAdapter := fmt.Errorf("filesystem: snapshot /ws: %w: %q is a symlink", instance.ErrUnsupportedSnapshotContent, "leak")
	assert.True(t, errors.Is(fromAdapter, ErrUnsupportedSnapshotContent))

	fromEngine := fmt.Errorf("prepare group: %w", ErrUnsupportedSnapshotContent)
	assert.True(t, errors.Is(fromEngine, instance.ErrUnsupportedSnapshotContent))

	assert.False(t, errors.Is(errors.New("unsupported content in working area snapshot"), ErrUnsupportedSnapshotContent),
		"identity, not message text, is what matches")
}
