package main

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// COMPOSITION-ROOT WIRING PROBE for instanceEnricher.SetSpawnLinks(instanceRepo)
// (cmd/arbitron/main.go). batchChildCounts is a documented no-op when
// e.spawnLinks is nil (domain/engine/enricher.go): every row would report
// ChildCount 0 and no expander would render. Every existing enricher test
// (domain/engine/enricher_child_count_test.go) drives that behavior through a
// fake SpawnLinkRepository the test itself constructs — none of them prove
// main.go actually wires the real one. This test boots the REAL composition
// root, spawns a real linked child, and reads the parent back through
// Engine.ListEnriched (the sink an operator's dashboard would hit) rather than
// through domain/engine's own fakes.
func TestEnricherChildCountE2E_ParentReportsRealChildCountThroughCompositionRoot(t *testing.T) {
	ds := bootSpawnStack(t)

	parentID, _, err := ds.eng.Trigger(context.Background(), "spawn-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(childrenOf(t, ds, parentID)) == 1
	}, 20*time.Second, 100*time.Millisecond,
		"the spawn node must create exactly one linked child before ChildCount can be observed")

	ctx := adminCtxForLocalUser(t, ds)
	rows, err := ds.eng.ListEnriched(ctx, instance.ListFilters{Limit: 50})
	require.NoError(t, err)

	var parentRow *instance.EnrichedInstanceSummary
	for i := range rows {
		if rows[i].ID == parentID {
			parentRow = &rows[i]
			break
		}
	}
	require.NotNil(t, parentRow, "the parent must appear in the enriched list")
	assert.Equal(t, 1, parentRow.ChildCount,
		"ChildCount must reflect the real spawned child through the wired instanceEnricher.SetSpawnLinks(instanceRepo); "+
			"if the composition root dropped that call this would read 0 and no expander would render")
}
