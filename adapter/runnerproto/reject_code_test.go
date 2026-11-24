package runnerproto

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReject_CodeIsBoundedVocabulary(t *testing.T) {
	for _, ok := range []string{"", "command_unavailable", "capability_unavailable", "workspace_preparation_failed", "future_code"} {
		assert.NoError(t, (&Reject{Reason: "r", Code: ok}).Validate(), ok)
	}
	for _, bad := range []string{"Command", "has space", "semi;colon", "_leading", strings.Repeat("a", 65)} {
		assert.Error(t, (&Reject{Reason: "r", Code: bad}).Validate(), bad)
	}
}

func TestWireOutcome_PullFailedIsAResultOnlyClass(t *testing.T) {
	assert.NoError(t, (&WireOutcome{Class: "pull_failed"}).Validate())
	assert.Error(t, (&WireHealth{LastOutcome: "pull_failed"}).Validate(), "a pull failure is never a source health state")
	assert.Error(t, (&WireOutcome{Class: "preparation_failed"}).Validate(), "preparation failures arrive only as typed rejects")
}
