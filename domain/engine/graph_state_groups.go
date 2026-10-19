package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Waiting-reason vocabulary. These exact strings are the wire contract the
// dashboard renders; when several apply they appear in this order. Zero
// outstanding tasks never means "ready": a decision, a file gather, a
// resolution session or the continuation itself can still be pending.
const (
	WaitingLaunching           = "launching"
	WaitingForTasksPrefix      = "waiting_for_tasks:"
	WaitingCancellingPrefix    = "cancelling:"
	WaitingPreparingFiles      = "preparing_files"
	WaitingGathering           = "gathering"
	WaitingResolvingConflicts  = "resolving_conflicts"
	WaitingRollbackFailed      = "rollback_failed"
	WaitingContinuationPending = "continuation_pending"
)

// GroupPanelStateUnknown is the panel state when the group record is
// unavailable. An unavailable record is never presented as a completed gather.
const GroupPanelStateUnknown = "unknown"

// buildGroupPanels returns one panel per group run (latest attempt) owned by a
// visible context, in context order then GroupGraph.Order. A group node that
// was reached but has no readable record becomes an "unknown" panel with a
// warning, and its live node state is forced to unknown.
func (t *graphTraversal) buildGroupPanels() []GroupPanel {
	panels := []GroupPanel{}
	for _, e := range t.entries {
		if e.inst == nil || e.wf == nil || e.gg == nil {
			continue
		}
		latest := latestRunsByNode(e.runs)
		for _, gid := range e.gg.Order {
			if !e.nodeSet[gid] {
				continue // owned by another context (a branch body)
			}
			if run, ok := latest[gid]; ok && e.runsRead && e.runsErr == nil {
				panels = append(panels, t.panelFor(run))
				continue
			}
			if !t.nodeReached(e, gid) {
				continue
			}
			panels = append(panels, t.unknownPanel(e, gid))
		}
	}
	return panels
}

func latestRunsByNode(runs []instance.GroupRun) map[string]instance.GroupRun {
	out := map[string]instance.GroupRun{}
	for _, run := range runs {
		cur, ok := out[run.NodeID]
		if !ok || run.Attempt > cur.Attempt || (run.Attempt == cur.Attempt && run.CreatedAt.After(cur.CreatedAt)) {
			out[run.NodeID] = run
		}
	}
	return out
}

func (t *graphTraversal) nodeReached(e *graphEntry, nodeID string) bool {
	for _, n := range t.contexts[e.index].Nodes {
		if n.NodeID == nodeID {
			return n.State != LiveStatePending && n.State != ""
		}
	}
	return false
}

func (t *graphTraversal) markNodeUnknown(e *graphEntry, nodeID, reason string) {
	nodes := t.contexts[e.index].Nodes
	for i := range nodes {
		if nodes[i].NodeID == nodeID {
			nodes[i].State = LiveStateUnknown
			nodes[i].Reason = reason
		}
	}
}

func (t *graphTraversal) unknownPanel(e *graphEntry, gid string) GroupPanel {
	p := GroupPanel{
		InstanceID:     e.inst.ID,
		NodeID:         gid,
		State:          GroupPanelStateUnknown,
		Tasks:          []GroupPanelTask{},
		WaitingReasons: []string{},
	}
	if scope := e.gg.Groups[gid]; scope != nil {
		cfg := scope.Config
		p.Form, p.CompletionRule, p.UnfinishedBehavior, p.GatherMode = cfg.Form, cfg.CompletionRule, cfg.UnfinishedBehavior, cfg.GatherMode
		for _, task := range cfg.Tasks {
			kind, origin := string(instance.GroupTaskKindWorkflow), task.Workflow
			if cfg.Form == workflow.GroupFormBranches {
				kind, origin = string(instance.GroupTaskKindBranch), task.Entry
			}
			p.Tasks = append(p.Tasks, GroupPanelTask{Key: task.Key, Kind: kind, Origin: origin, LiveStatus: LiveStateUnknown})
		}
	}
	p.Progress.Total = len(p.Tasks)
	t.uncertain = true
	t.warn(fmt.Sprintf("parallel group record for node %s of run %s is unavailable", gid, e.inst.ID))
	t.markNodeUnknown(e, gid, "group record unavailable")
	return p
}

func (t *graphTraversal) panelFor(run instance.GroupRun) GroupPanel {
	tasks := append([]instance.GroupTask(nil), run.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Position < tasks[j].Position })
	frozen := map[string]instance.GroupTaskStatus{}
	if run.Decision != nil {
		for _, ft := range run.Decision.Tasks {
			frozen[ft.Key] = ft.Status
		}
	}
	p := GroupPanel{
		GroupRunID:         run.ID,
		InstanceID:         run.ParentInstanceID,
		NodeID:             run.NodeID,
		Form:               run.Form,
		CompletionRule:     run.CompletionRule,
		UnfinishedBehavior: run.UnfinishedBehavior,
		GatherMode:         run.GatherMode,
		State:              string(run.State),
		Tasks:              make([]GroupPanelTask, 0, len(tasks)),
	}
	hiddenKeys := map[string]bool{}
	for _, task := range tasks {
		card := t.taskCard(task)
		if fs, ok := frozen[task.Key]; ok {
			card.FrozenStatus = string(fs)
		}
		// Frozen child data needs the same authorization proof as live data.
		if card.Redacted || (task.LaunchState == instance.GroupTaskLaunched && card.ChildInstanceID == nil) {
			hiddenKeys[task.Key] = true
		}
		countProgress(&p.Progress, card.LiveStatus)
		p.Tasks = append(p.Tasks, card)
	}
	p.Progress.Total = len(p.Tasks)
	if run.Decision != nil {
		p.Winner = run.Decision.WinnerKey
		p.Decision = redactDecision(run.Decision, hiddenKeys)
	}
	if run.FinalOutput != nil {
		p.FinalOutput = redactGroupOutput(run.FinalOutput, hiddenKeys)
		if gather, ok := p.FinalOutput["gather"].(map[string]any); ok {
			redactGatherOutput(gather, hiddenKeys)
			delete(gather, "handle")
			if session, ok := gather["activeSession"].(map[string]any); ok {
				id, _ := session["instanceId"].(string)
				source, err := uuid.Parse(id)
				if err != nil || t.visible[source] == nil {
					delete(gather, "activeSession")
				}
			}
		}
	}
	if run.Gather != nil {
		g := run.Gather.Clone()
		redactGatherReport(&g, hiddenKeys)
		g.Handle = nil // staging / parent directories are server paths
		g.ActiveSession = nil
		if s := run.Gather.ActiveSession; s != nil && t.visible[s.InstanceID] != nil {
			ref := *s
			g.ActiveSession = &ref
			link := ref
			p.ActiveSession = &link
		}
		p.Gather = &g
	}
	p.WaitingReasons = waitingReasons(run, p.Tasks)
	return p
}

// Gather diagnostics can contain child paths, including a group-wide error
// copied into every task's rollback reason. Free text has no task provenance,
// so withhold it whenever any task is hidden. Keep structured outcomes and
// ordered history, and disclose conflict paths only for authorized tasks.
const hiddenGatherDetail = "gather details hidden"

func redactGatherReport(g *instance.GatherReport, hidden map[string]bool) {
	if len(hidden) == 0 {
		return
	}
	if g.Error != "" {
		g.Error = hiddenGatherDetail
	}
	for i := range g.Tasks {
		task := &g.Tasks[i]
		if task.Reason != "" {
			task.Reason = hiddenGatherDetail
		}
		if hidden[task.Key] {
			task.ConflictPaths = nil
		}
	}
	for i := range g.Attempts {
		if hidden[g.Attempts[i].TaskKey] {
			// The dashboard reads .length on this required array.
			g.Attempts[i].ConflictPaths = []string{}
		}
	}
}

// The frozen output carries a separate JSON-shaped copy of the report. It
// must be scrubbed even when the live Gather record is absent.
func redactGatherOutput(g map[string]any, hidden map[string]bool) {
	if len(hidden) == 0 {
		return
	}
	if detail, _ := g["error"].(string); detail != "" {
		g["error"] = hiddenGatherDetail
	}
	if tasks, ok := g["tasks"].([]any); ok {
		for _, raw := range tasks {
			if task, ok := raw.(map[string]any); ok {
				if detail, _ := task["reason"].(string); detail != "" {
					task["reason"] = hiddenGatherDetail
				}
				if key, _ := task["key"].(string); hidden[key] {
					delete(task, "conflictPaths")
				}
			}
		}
	}
	if attempts, ok := g["attempts"].([]any); ok {
		for _, raw := range attempts {
			if attempt, ok := raw.(map[string]any); ok {
				if key, _ := attempt["taskKey"].(string); hidden[key] {
					attempt["conflictPaths"] = []any{}
				}
			}
		}
	}
}

// taskCard derives one card. Live status comes from the authorized child
// instance; a hidden child yields a redacted card carrying only the parent's
// own roster status; a child that was never traversed falls back to the
// roster only when the roster status is terminal.
func (t *graphTraversal) taskCard(task instance.GroupTask) GroupPanelTask {
	card := GroupPanelTask{
		Key:                     task.Key,
		Kind:                    string(task.Kind),
		Origin:                  task.Origin,
		Blocking:                task.Blocking,
		ContinuingIndependently: task.ContinuingIndependently,
		CancelRequested:         task.CancelRequested,
	}
	if task.ChildInstanceID != uuid.Nil {
		if child, ok := t.visible[task.ChildInstanceID]; ok {
			id := task.ChildInstanceID
			card.ChildInstanceID = &id
			card.LiveStatus = string(instance.GroupTaskStatusFromState(child.State))
			card.LaunchError = task.LaunchError
			return card
		}
		if t.hidden[task.ChildInstanceID] {
			card.Redacted = true
			card.LiveStatus = rosterLiveStatus(task)
			if t.unreadable[task.ChildInstanceID] {
				card.LiveStatus = LiveStateUnknown
			}
			return card
		}
	}
	card.LiveStatus = rosterFallbackStatus(task)
	card.LaunchError = task.LaunchError
	return card
}

func rosterLiveStatus(task instance.GroupTask) string {
	switch task.LaunchState {
	case instance.GroupTaskPending:
		return string(instance.GroupTaskStatusNotStarted)
	case instance.GroupTaskLaunchFailed:
		return string(instance.GroupTaskStatusFailed)
	}
	if task.LiveStatus != "" {
		return string(task.LiveStatus)
	}
	return LiveStateUnknown
}

func rosterFallbackStatus(task instance.GroupTask) string {
	switch task.LaunchState {
	case instance.GroupTaskPending:
		return string(instance.GroupTaskStatusNotStarted)
	case instance.GroupTaskLaunchFailed:
		return string(instance.GroupTaskStatusFailed)
	}
	if isTerminalTaskStatus(string(task.LiveStatus)) {
		return string(task.LiveStatus)
	}
	return LiveStateUnknown
}

func isTerminalTaskStatus(s string) bool {
	return instance.GroupTaskStatus(s).IsTerminal()
}

func countProgress(p *GroupProgress, status string) {
	switch instance.GroupTaskStatus(status) {
	case instance.GroupTaskStatusCompleted:
		p.Completed++
	case instance.GroupTaskStatusFailed:
		p.Failed++
	case instance.GroupTaskStatusCancelled:
		p.Cancelled++
	case instance.GroupTaskStatusNotStarted:
		p.NotStarted++
	case instance.GroupTaskStatusQueued, instance.GroupTaskStatusRunning, instance.GroupTaskStatusWaiting, instance.GroupTaskStatusPaused:
		p.Active++
	}
}

// waitingReasons explains exactly what the gather is waiting on.
func waitingReasons(run instance.GroupRun, cards []GroupPanelTask) []string {
	var outstanding, cancelling []string
	for _, c := range cards {
		terminal := isTerminalTaskStatus(c.LiveStatus)
		if c.Blocking && !terminal {
			outstanding = append(outstanding, c.Key)
		}
		if c.CancelRequested && !terminal {
			cancelling = append(cancelling, c.Key)
		}
	}
	files := run.GatherMode == workflow.GroupGatherResultsAndFiles
	reasons := []string{}
	if run.State == instance.GroupRunLaunching {
		reasons = append(reasons, WaitingLaunching)
	}
	if run.State == instance.GroupRunRunning && len(outstanding) > 0 {
		reasons = append(reasons, WaitingForTasksPrefix+strings.Join(outstanding, ","))
	}
	if len(cancelling) > 0 {
		reasons = append(reasons, WaitingCancellingPrefix+strings.Join(cancelling, ","))
	}
	switch run.State {
	case instance.GroupRunDecided:
		if files {
			reasons = append(reasons, WaitingPreparingFiles)
		}
	case instance.GroupRunGathering:
		reasons = append(reasons, WaitingGathering)
	case instance.GroupRunResolvingConflicts:
		reasons = append(reasons, WaitingResolvingConflicts)
	}
	if run.Gather != nil && run.Gather.RollbackStatus == instance.RollbackStatusFailed {
		reasons = append(reasons, WaitingRollbackFailed)
	}
	pending := false
	switch run.State {
	case instance.GroupRunRunning:
		pending = len(outstanding) == 0 // every blocking task finished, no decision yet
	case instance.GroupRunDecided:
		pending = !files // results-mode finalize not yet applied
	case instance.GroupRunDone:
		pending = !run.ContinuationDelivered
	}
	if pending {
		reasons = append(reasons, WaitingContinuationPending)
	}
	return reasons
}

// redactDecision deep-copies the frozen decision and strips the results,
// failures and child ids of hidden tasks. The stored record is never mutated.
func redactDecision(d *instance.GroupDecision, hidden map[string]bool) *instance.GroupDecision {
	cp := *d
	cp.Tasks = make([]instance.FrozenTask, len(d.Tasks))
	for i, ft := range d.Tasks {
		ft.Result = variable.DeepCopyMap(ft.Result)
		ft.Failure = variable.DeepCopyMap(ft.Failure)
		if hidden[ft.Key] {
			ft.Result, ft.Failure, ft.ChildInstanceID = nil, nil, ""
		}
		cp.Tasks[i] = ft
	}
	return &cp
}

// redactGroupOutput deep-copies the frozen group output (BuildGroupOutput
// shape) and scrubs hidden tasks' entries in "tasks" and "byKey".
func redactGroupOutput(out map[string]any, hidden map[string]bool) map[string]any {
	cp := variable.DeepCopyMap(out)
	if len(hidden) == 0 {
		return cp
	}
	scrub := func(m map[string]any) {
		delete(m, "result")
		delete(m, "failure")
		m["childInstanceId"] = ""
	}
	if tasks, ok := cp["tasks"].([]any); ok {
		for _, raw := range tasks {
			if m, ok := raw.(map[string]any); ok {
				if key, _ := m["key"].(string); hidden[key] {
					scrub(m)
				}
			}
		}
	}
	if byKey, ok := cp["byKey"].(map[string]any); ok {
		for key, raw := range byKey {
			if m, ok := raw.(map[string]any); ok && hidden[key] {
				scrub(m)
			}
		}
	}
	return cp
}
