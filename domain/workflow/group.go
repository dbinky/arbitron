package workflow

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
)

// Parallel group vocabulary. A parallel_group node fans out a FIXED, ordered
// set of tasks and gathers them before continuing through exactly one of its
// success/failure ports. These constants are the single source of truth for
// the allowed values, shared by validation (this package), the MCP authoring
// tools, the group executor/launcher (Phase 2) and the run views (Phase 4).
// Every value is exact-match lowercase; there are no inherited defaults.
const (
	// GroupFormBranches: each task is a closed, exclusively owned body of
	// nodes inside THIS workflow, entered through a "task" edge.
	GroupFormBranches = "branches"
	// GroupFormWorkflows: each task is an awaited child workflow run.
	GroupFormWorkflows = "workflows"

	// GroupRuleAllSucceed: the group succeeds when every task succeeds.
	GroupRuleAllSucceed = "all_succeed"
	// GroupRuleFirstSuccess: the first successful task wins.
	GroupRuleFirstSuccess = "first_success"

	// GroupUnfinishedWaitForRemaining (all_succeed only): after a task fails,
	// wait for the remaining tasks before taking the failure path.
	GroupUnfinishedWaitForRemaining = "wait_for_remaining"
	// GroupUnfinishedCancel (both rules): cancel unfinished tasks as soon as
	// the group is decided.
	GroupUnfinishedCancel = "cancel_unfinished"
	// GroupUnfinishedFinishIndependently (first_success only): let losing
	// tasks run to completion; they can no longer affect the parent.
	GroupUnfinishedFinishIndependently = "finish_independently"

	// GroupGatherResults gathers task outcomes and results only.
	GroupGatherResults = "results"
	// GroupGatherResultsAndFiles additionally merges eligible file changes into
	// the parent's working area (Phase 3); it requires a resolverProfile.
	GroupGatherResultsAndFiles = "results_and_files"

	// GroupPortTask names the edges from a branches-form group to each task's
	// entry node. Every task edge carries this SAME output name; the unique
	// entry target, not the port name, identifies the task.
	GroupPortTask = "task"
	// GroupPortSuccess / GroupPortFailure are the group's two continuations.
	GroupPortSuccess = "success"
	GroupPortFailure = "failure"
)

// GroupConfig is the configuration of one parallel_group node
// (NodeConfig.Group). Obtain a checked, deep-copied value with
// ParseGroupConfig; never execute from the raw manifest pointer.
type GroupConfig struct {
	Form               string      `json:"form"`
	Tasks              []GroupTask `json:"tasks"`
	CompletionRule     string      `json:"completionRule"`
	UnfinishedBehavior string      `json:"unfinishedBehavior"`
	GatherMode         string      `json:"gatherMode"`
	// RepoPath is an optional templated path, relative to the immediate
	// parent's {{workspace}}, naming an existing repository inside it.
	RepoPath string `json:"repoPath,omitempty"`
	// ResolverProfile is the agent-kind profile alias used for conflict
	// resolution; required when GatherMode is results_and_files.
	ResolverProfile string `json:"resolverProfile,omitempty"`
	// ResolverPromptFile is an optional workflow-relative prompt override.
	ResolverPromptFile string `json:"resolverPromptFile,omitempty"`
}

// GroupTask is one fixed task of a group, in author order.
type GroupTask struct {
	// Key is the task's unique, stable identity: nonempty, ^[A-Za-z0-9_-]+$.
	Key string `json:"key"`
	// Entry (branches form only) is the id of the body's entry node.
	Entry string `json:"entry,omitempty"`
	// Workflow (workflows form only) is the child workflow's name.
	Workflow string `json:"workflow,omitempty"`
	// Inputs are typed literals and/or {{templates}} applied to the task's
	// own copy of the group-start variables. "workspace" is reserved.
	Inputs map[string]any `json:"inputs,omitempty"`
}

// Sentinels ParseGroupConfig reports. Each problem is a *GroupConfigProblem
// wrapping exactly one of these, so validation can map each to its own
// ValidationError code while the executor wraps them verbatim — one set of
// checks, two presentations (the ParseSpawnConfig pattern).
var (
	ErrGroupConfigMissing          = errors.New("parallel_group has no group configuration")
	ErrGroupFormInvalid            = errors.New("unrecognized group form")
	ErrGroupNoTasks                = errors.New("parallel_group has no tasks")
	ErrGroupTooManyTasks           = errors.New("parallel_group has too many tasks")
	ErrGroupTaskKeyInvalid         = errors.New("invalid task key")
	ErrGroupTaskKeyDuplicate       = errors.New("duplicate task key")
	ErrGroupTaskTargetInvalid      = errors.New("task target does not match the group form")
	ErrGroupTaskEntryDuplicate     = errors.New("duplicate task entry")
	ErrGroupReservedInput          = errors.New("task input name is reserved")
	ErrGroupRuleInvalid            = errors.New("unrecognized completionRule")
	ErrGroupUnfinishedInvalid      = errors.New("invalid unfinishedBehavior")
	ErrGroupGatherInvalid          = errors.New("unrecognized gatherMode")
	ErrGroupResolverProfileMissing = errors.New("resolverProfile is required for results_and_files")
)

// MaxGroupTasks is the server-owned ceiling on a parallel_group's task count.
// Every task gets an eager full copy of the parent's working area before the
// queue applies any concurrency limit, so the count bounds a launch's fan-out.
// It is enforced by ParseGroupConfig (validation and the executor alike) and
// no workflow setting can raise it. It does not bound storage on its own: the
// launcher also enforces a byte budget (engine.GroupLauncher.SetLaunchByteBudget).
const MaxGroupTasks = 32

// GroupConfigProblem is one problem found by ParseGroupConfig. It unwraps to
// its sentinel (errors.Is works on the joined error) and carries the offending
// task, when the problem is task-scoped, so validation can fill
// ValidationError.TaskKey without re-deriving the checks.
type GroupConfigProblem struct {
	Err       error  // one of the ErrGroup* sentinels
	TaskIndex int    // 0-based position of the offending task; -1 when not task-scoped
	TaskKey   string // the offending task's key ("" when not task-scoped or the key itself is empty)
	Detail    string // corrective specifics
}

// Error renders the problem as one actionable sentence.
func (p *GroupConfigProblem) Error() string {
	msg := p.Err.Error()
	if p.TaskIndex >= 0 {
		if p.TaskKey != "" {
			msg = "task " + strconv.Quote(p.TaskKey) + ": " + msg
		} else {
			msg = "task #" + strconv.Itoa(p.TaskIndex+1) + ": " + msg
		}
	}
	if p.Detail != "" {
		msg += ": " + p.Detail
	}
	return msg
}

// Unwrap exposes the sentinel to errors.Is / errors.As.
func (p *GroupConfigProblem) Unwrap() error { return p.Err }

// GroupConfigProblems flattens an error returned by ParseGroupConfig into its
// individual problems, in report order. It returns nil for a nil error or an
// error that carries no *GroupConfigProblem.
func GroupConfigProblems(err error) []*GroupConfigProblem {
	var out []*GroupConfigProblem
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if p, ok := e.(*GroupConfigProblem); ok {
			out = append(out, p)
			return
		}
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, inner := range j.Unwrap() {
				walk(inner)
			}
			return
		}
		walk(errors.Unwrap(e))
	}
	walk(err)
	return out
}

// groupTaskKeyRE is the allowed task-key charset. Keys appear in variable
// paths ({{group.byKey.<key>.status}}) and git refs, so dots, slashes and
// whitespace are excluded.
var groupTaskKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// groupReservedInputName is reseeded by the engine for every task (each task
// gets its own working area); an author override would point a task at a
// shared directory.
const groupReservedInputName = "workspace"

// ParseGroupConfig extracts and checks a parallel_group node's contract.
//
// It is the ONE parser shared by author-time validation and the Phase 2
// executor: an instance executes from a WorkflowSnapshot that may predate the
// validation that would have caught a problem, so the executor must re-check,
// and must re-check identically.
//
// EVERY problem is reported (errors.Join of *GroupConfigProblem values) rather
// than the first. On error the zero GroupConfig is returned — a caller must
// never launch from half a contract. On success the result is a DEEP copy:
// Tasks and every nested Inputs map/slice are freshly allocated, so neither
// the caller nor the manifest can mutate the other.
func ParseGroupConfig(cfg NodeConfig) (GroupConfig, error) {
	if cfg.Group == nil {
		return GroupConfig{}, &GroupConfigProblem{
			Err:       ErrGroupConfigMissing,
			TaskIndex: -1,
			Detail:    "set config.group with form, tasks, completionRule, unfinishedBehavior and gatherMode",
		}
	}
	g := cloneGroupConfig(*cfg.Group)

	var problems []error
	add := func(sentinel error, idx int, key, detail string) {
		problems = append(problems, &GroupConfigProblem{Err: sentinel, TaskIndex: idx, TaskKey: key, Detail: detail})
	}

	formKnown := g.Form == GroupFormBranches || g.Form == GroupFormWorkflows
	if !formKnown {
		add(ErrGroupFormInvalid, -1, "", fmt.Sprintf("%s (must be exactly %s or %s)",
			strconv.Quote(g.Form), strconv.Quote(GroupFormBranches), strconv.Quote(GroupFormWorkflows)))
	}

	if len(g.Tasks) == 0 {
		add(ErrGroupNoTasks, -1, "", "declare at least one task")
	}
	if len(g.Tasks) > MaxGroupTasks {
		add(ErrGroupTooManyTasks, -1, "", fmt.Sprintf("%d tasks declared; a group may have at most %d", len(g.Tasks), MaxGroupTasks))
	}

	seenKeys := make(map[string]bool, len(g.Tasks))
	seenEntries := make(map[string]string, len(g.Tasks))
	for i, t := range g.Tasks {
		switch {
		case t.Key == "":
			add(ErrGroupTaskKeyInvalid, i, "", "every task needs a nonempty key matching [A-Za-z0-9_-]+")
		case !groupTaskKeyRE.MatchString(t.Key):
			add(ErrGroupTaskKeyInvalid, i, t.Key, strconv.Quote(t.Key)+" must match [A-Za-z0-9_-]+")
		case seenKeys[t.Key]:
			add(ErrGroupTaskKeyDuplicate, i, t.Key, "task keys must be unique within a group")
		}
		if t.Key != "" {
			seenKeys[t.Key] = true
		}

		if formKnown {
			switch g.Form {
			case GroupFormBranches:
				if t.Entry == "" {
					add(ErrGroupTaskTargetInvalid, i, t.Key, "a branches task must name its body's entry node")
				}
				if t.Workflow != "" {
					add(ErrGroupTaskTargetInvalid, i, t.Key, "a branches task must not name a workflow; use form "+strconv.Quote(GroupFormWorkflows)+" for child workflows")
				}
				if t.Entry != "" {
					if first, dup := seenEntries[t.Entry]; dup {
						add(ErrGroupTaskEntryDuplicate, i, t.Key, "entry "+strconv.Quote(t.Entry)+" is already the entry of task "+strconv.Quote(first))
					} else {
						seenEntries[t.Entry] = t.Key
					}
				}
			case GroupFormWorkflows:
				if t.Workflow == "" {
					add(ErrGroupTaskTargetInvalid, i, t.Key, "a workflows task must name its child workflow")
				}
				if t.Entry != "" {
					add(ErrGroupTaskTargetInvalid, i, t.Key, "a workflows task must not name an entry node; use form "+strconv.Quote(GroupFormBranches)+" for in-workflow bodies")
				}
			}
		}

		if _, reserved := t.Inputs[groupReservedInputName]; reserved {
			add(ErrGroupReservedInput, i, t.Key, "\"workspace\" is reseeded by the engine for every task; remove it from inputs")
		}
	}

	ruleKnown := g.CompletionRule == GroupRuleAllSucceed || g.CompletionRule == GroupRuleFirstSuccess
	if !ruleKnown {
		add(ErrGroupRuleInvalid, -1, "", fmt.Sprintf("%s (must be exactly %s or %s)",
			strconv.Quote(g.CompletionRule), strconv.Quote(GroupRuleAllSucceed), strconv.Quote(GroupRuleFirstSuccess)))
	}
	if detail := unfinishedProblem(g.CompletionRule, g.UnfinishedBehavior, ruleKnown); detail != "" {
		add(ErrGroupUnfinishedInvalid, -1, "", detail)
	}

	switch g.GatherMode {
	case GroupGatherResults:
	case GroupGatherResultsAndFiles:
		if g.ResolverProfile == "" {
			add(ErrGroupResolverProfileMissing, -1, "", "file gathering needs an agent-kind profile alias to resolve merge conflicts")
		}
	default:
		add(ErrGroupGatherInvalid, -1, "", fmt.Sprintf("%s (must be exactly %s or %s)",
			strconv.Quote(g.GatherMode), strconv.Quote(GroupGatherResults), strconv.Quote(GroupGatherResultsAndFiles)))
	}

	if len(problems) > 0 {
		return GroupConfig{}, errors.Join(problems...)
	}
	return g, nil
}

// unfinishedProblem returns "" when behavior is acceptable for rule. An
// unknown behavior is always reported; a KNOWN behavior under an unknown rule
// is not (compatibility is undefined until the rule is fixed, and the rule is
// already reported).
func unfinishedProblem(rule, behavior string, ruleKnown bool) string {
	switch behavior {
	case GroupUnfinishedWaitForRemaining, GroupUnfinishedCancel, GroupUnfinishedFinishIndependently:
	default:
		return fmt.Sprintf("%s (must be exactly %s, %s or %s)", strconv.Quote(behavior),
			strconv.Quote(GroupUnfinishedWaitForRemaining), strconv.Quote(GroupUnfinishedCancel),
			strconv.Quote(GroupUnfinishedFinishIndependently))
	}
	if !ruleKnown {
		return ""
	}
	switch {
	case rule == GroupRuleAllSucceed && behavior == GroupUnfinishedFinishIndependently:
		return "all_succeed accepts only wait_for_remaining or cancel_unfinished"
	case rule == GroupRuleFirstSuccess && behavior == GroupUnfinishedWaitForRemaining:
		return "first_success accepts only cancel_unfinished or finish_independently"
	}
	return ""
}

// cloneGroupConfig returns a structurally independent copy of g.
func cloneGroupConfig(g GroupConfig) GroupConfig {
	out := g
	if g.Tasks != nil {
		out.Tasks = make([]GroupTask, len(g.Tasks))
		for i, t := range g.Tasks {
			out.Tasks[i] = t
			if t.Inputs != nil {
				out.Tasks[i].Inputs = cloneGroupValue(t.Inputs).(map[string]any)
			}
		}
	}
	return out
}

// cloneGroupValue deep-copies a task input value. map[string]any and []any
// (what JSON decoding produces) take a fast path; any other map or slice type
// is copied via reflection with its static type preserved. Scalars, pointers
// and structs are returned as-is — task inputs are JSON-shaped data.
func cloneGroupValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if t == nil {
			return t
		}
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = cloneGroupValue(e)
		}
		return out
	case []any:
		if t == nil {
			return t
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneGroupValue(e)
		}
		return out
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneReflectElem(iter.Value(), rv.Type().Elem()))
		}
		return out.Interface()
	case reflect.Slice:
		if rv.IsNil() {
			return v
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(cloneReflectElem(rv.Index(i), rv.Type().Elem()))
		}
		return out.Interface()
	}
	return v
}

// cloneReflectElem deep-copies one container element, converting back to the
// container's element type (an interface-typed nil element stays a zero value).
func cloneReflectElem(e reflect.Value, elemType reflect.Type) reflect.Value {
	if (e.Kind() == reflect.Interface || e.Kind() == reflect.Map || e.Kind() == reflect.Slice) && e.IsNil() {
		return reflect.Zero(elemType)
	}
	c := reflect.ValueOf(cloneGroupValue(e.Interface()))
	if !c.IsValid() {
		return reflect.Zero(elemType)
	}
	return c.Convert(elemType)
}
