package main

import (
	"slices"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every built-in workflow launch must be recorded under its actual assignment.
// Discovery and native probes run outside assignments and stay unsupervised.
func TestRunnerWiring_BuiltInLaunchesAreSupervisedUnderTheirAssignment(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	rwTouch(t, fake, "release-all")
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)

	id, _, err := ds.eng.Trigger(ctx, "rr-local", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)

	rows, err := ds.db.Query(`SELECT j.node_id, p.state FROM builtin_processes p
		JOIN runner_jobs j ON j.id = p.job_id AND j.generation = p.generation
		WHERE j.run_id = ? ORDER BY j.node_id`, id.String())
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var node, state string
		require.NoError(t, rows.Scan(&node, &state))
		got[node] = state
	}
	require.NoError(t, rows.Err())
	nodes := make([]string, 0, len(got))
	for n := range got {
		nodes = append(nodes, n)
	}
	slices.Sort(nodes)
	assert.Equal(t, []string{"ask", "run"}, nodes, "SINK: each placed launch is recorded under its own job fence")
	var orphans int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM builtin_processes p
		LEFT JOIN runner_jobs j ON j.id = p.job_id AND j.generation = p.generation WHERE j.id IS NULL`).Scan(&orphans))
	assert.Zero(t, orphans, "no supervised launch outside a real assignment")
}
