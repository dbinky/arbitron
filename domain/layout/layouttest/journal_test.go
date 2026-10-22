package layouttest

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJournalQuarantinePreservesReceiptAndBytes(t *testing.T) {
	ctx := context.Background()
	req := workflow.ArrangementRequest{Revision: "r", Manifest: []byte("immutable bytes")}
	j := NewFakeJournal(req)
	require.NoError(t, j.MarkCommitted(ctx, "r"))
	require.NoError(t, j.Quarantine(ctx, "r", "not-committed"))
	q, ok := j.quarantine["r"]
	require.True(t, ok)
	assert.Equal(t, "not-committed", q.reason)
	assert.Equal(t, workflow.JournalCommitted, q.entry.State)
	assert.Equal(t, req.Manifest, q.entry.Request.Manifest)
	assert.False(t, j.Has("r"))
}
