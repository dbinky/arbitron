package layout

import (
	"slices"
	"unicode/utf16"

	"github.com/arbitron/arbitron/domain/workflow"
)

// GroupProbeNames enumerates the captured group's required runtime cases.
func GroupProbeNames(groups *workflow.GroupGraph) []string {
	if groups == nil || len(groups.Order) == 0 {
		return nil
	}
	var names []string
	for _, state := range []string{"not_started", "launching", "running", "decided", "gathering", "resolving_conflicts", "done", "unknown"} {
		names = append(names, "group:run:"+state)
	}
	for _, state := range []string{"not_started", "queued", "running", "waiting", "paused", "completed", "failed", "cancelled", "unfinished", "unknown"} {
		names = append(names, "group:task:"+state)
	}
	names = append(names, "group:badges", "group:redacted", "group:selected-running")
	ids := append([]string(nil), groups.Order...)
	// Match JavaScript's default UTF-16 ordering, including astral group IDs.
	slices.SortFunc(ids, func(a, b string) int {
		return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
	})
	for _, id := range ids {
		for _, task := range groups.Groups[id].Config.Tasks {
			names = append(names, groupIDJSON("group-probe", "winner", id, task.Key))
		}
	}
	return names
}
