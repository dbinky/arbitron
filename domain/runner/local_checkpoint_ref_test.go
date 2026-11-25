package runner_test

import (
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalCheckpointRef_RoundTrip(t *testing.T) {
	ref := runner.LocalCheckpointRef{RunID: uuid.New(), Version: 7, ManifestHash: strings.Repeat("ab", 32)}
	s := ref.String()
	assert.True(t, strings.HasPrefix(s, "local:"), "a local ref can never be mistaken for a catalog CheckpointRef")
	got, err := runner.ParseLocalCheckpointRef(s)
	require.NoError(t, err)
	assert.Equal(t, ref, got)
	assert.Equal(t, "", runner.LocalCheckpointRef{}.String())
	assert.True(t, runner.LocalCheckpointRef{}.IsZero())
}

func TestLocalCheckpointRef_RejectsMalformed(t *testing.T) {
	run := uuid.New().String()
	hash := strings.Repeat("a", 64)
	for _, s := range []string{
		"",
		run + "@1:" + hash,            // catalog shape, no prefix
		"local:" + run + "@0:" + hash, // versions start at 1
		"local:" + run + "@-2:" + hash,
		"local:" + run + "@x:" + hash,
		"local:" + run + "@1:",    // empty hash
		"local:" + run + "@1:XYZ", // not lowercase hex
		"local:" + uuid.Nil.String() + "@1:" + hash,
		"local:not-a-uuid@1:" + hash,
		"local:" + run + "1:" + hash, // no @
	} {
		_, err := runner.ParseLocalCheckpointRef(s)
		assert.Error(t, err, "%q must be rejected", s)
	}
}
