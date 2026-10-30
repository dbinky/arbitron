package samples_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ownerFileChecker binds the owner-aware filesystem reader to the
// workflow.FileChecker shape, resolving @lib against the seeded shared/_lib.
type ownerFileChecker struct {
	reader *filesystem.FileReaderAdapter
	owner  uuid.UUID
}

func (c ownerFileChecker) FileExists(wf, rel string) bool {
	return c.reader.FileExists(c.owner, wf, rel)
}

type sampleWorkflows struct {
	repo  *filesystem.WorkflowRepo
	owner uuid.UUID
	names map[string]bool
}

func (s sampleWorkflows) WorkflowExists(name string) bool { return s.names[name] }

func (s sampleWorkflows) ChildInputs(name string) ([]workflow.InputField, error) {
	if !s.names[name] {
		return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	wf, err := s.repo.Load(context.Background(), workflow.Private(s.owner), name)
	if err != nil {
		return nil, err
	}
	return wf.Inputs, nil
}

type tableProfiles map[string]profileRow

func (p tableProfiles) ProfileKind(alias string) (string, bool) {
	r, ok := p[alias]
	return r.Kind, ok
}

func isErrorSeverity(e workflow.ValidationError) bool {
	return e.Severity == "" || e.Severity == "error"
}

// profiledNode returns the id of a node that sets model_profile, or "".
func profiledNode(wf *workflow.Workflow) string {
	for _, n := range wf.Nodes {
		if n.Config.ModelProfile != "" {
			return n.ID
		}
	}
	return ""
}

func TestProfiledNode(t *testing.T) {
	wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "b", Config: workflow.NodeConfig{ModelProfile: "p"}}}}
	if got := profiledNode(wf); got != "b" {
		t.Errorf("profiledNode = %q, want b", got)
	}
	if got := profiledNode(&workflow.Workflow{Nodes: wf.Nodes[:1]}); got != "" {
		t.Errorf("profiledNode = %q, want empty", got)
	}
}

func TestSamplesValidate(t *testing.T) {
	names := allSampleNames(t)
	base, owner := sampleRoot(t)
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	sw := sampleWorkflows{repo: filesystem.NewWorkflowRepo(base), owner: owner, names: known}
	files := ownerFileChecker{reader: filesystem.NewFileReader(base), owner: owner}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			wf, err := sw.repo.Load(context.Background(), workflow.Private(owner), name)
			if err != nil {
				t.Fatal(err)
			}
			var profiles workflow.ProfileChecker
			if tbl := profileTable(t, name); len(tbl) > 0 {
				profiles = tableProfiles(tbl)
			} else if n := profiledNode(wf); n != "" {
				t.Fatalf("node %s sets model_profile but %s has no PROFILES.md alias table; profile validation would be skipped", n, name)
			}
			findings := workflow.Validate(wf, files, profiles, nil,
				workflow.WithWorkflowChecker(sw), workflow.WithChildContractReader(sw))
			for _, f := range findings {
				if isErrorSeverity(f) {
					t.Errorf("%s: %s (%s)", f.Code, f.Message, f.NodeID)
				}
				if name == "feature-pipeline" && f.Code == "SPAWN_PREPARED_INPUTS" {
					t.Errorf("SPAWN_PREPARED_INPUTS: %s (%s)", f.Message, f.NodeID)
				}
			}
		})
	}
}

func TestNoInteractiveOrAwaitedSpawnOnCycle(t *testing.T) {
	for _, name := range allSampleNames(t) {
		if name == "legacy-feature-pipeline" { // legacy: known resolve-deps cycle
			continue
		}
		t.Run(name, func(t *testing.T) {
			wf := loadSample(t, name)
			for _, f := range workflow.LintWorkflow(wf) {
				if f.Code == "INTERACTIVE_IN_LOOP" {
					t.Errorf("%s: %s", f.Code, f.Message)
				}
			}
			for _, n := range wf.Nodes {
				interactive := workflow.IsInteractive(n.Type)
				awaited := n.Type == workflow.NodeTypeSpawnWorkflow && n.Config.SpawnMode == workflow.SpawnModeWaitForChild
				if (interactive || awaited) && onCycle(wf, n.ID) {
					t.Errorf("node %s (%s) sits on a cycle", n.ID, n.Type)
				}
			}
		})
	}
}

func TestErrorRoutesResolve(t *testing.T) {
	for _, name := range allSampleNames(t) {
		t.Run(name, func(t *testing.T) {
			wf := loadSample(t, name)
			ids := map[string]bool{}
			for _, n := range wf.Nodes {
				ids[n.ID] = true
			}
			if id := wf.DefaultErrorBehavior.ErrorNodeID; id != "" && !ids[id] {
				t.Errorf("defaultErrorBehavior.errorNodeId %q names no node", id)
			}
			for _, n := range wf.Nodes {
				if id := n.ErrorBehavior.ErrorNodeID; id != "" && !ids[id] {
					t.Errorf("node %s errorBehavior.errorNodeId %q names no node", n.ID, id)
				}
			}
		})
	}
}

var templateRE = regexp.MustCompile(`\{\{[^{}]+\}\}`)

// argBraceProblem reports why a script arg would be mangled by the template
// expander: after removing whole {{name}} templates, no brace pair may remain.
func argBraceProblem(arg string) string {
	rest := templateRE.ReplaceAllString(arg, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		return "stray template braces"
	}
	return ""
}

func TestArgBraceProblem(t *testing.T) {
	ok := []string{"", "plain", "{{a}}", "x {{a.b}} y {{c}}", "{a:1}", ".x | {a: .b}", "{{a}}{{b}}"}
	bad := []string{"{{", "}}", "{a:{b:1}}", "{{a}} {{", "{{a}}}}", "{{ }}x}}"}
	for _, a := range ok {
		if p := argBraceProblem(a); p != "" {
			t.Errorf("arg %q: unexpected problem %q", a, p)
		}
	}
	for _, a := range bad {
		if argBraceProblem(a) == "" {
			t.Errorf("arg %q: expected a problem", a)
		}
	}
}

func TestArgvFiltersHaveNoTemplateBraces(t *testing.T) {
	inspected := 0
	for _, name := range allSampleNames(t) {
		wf := loadSample(t, name)
		for _, n := range wf.Nodes {
			for i, arg := range n.Config.Args {
				inspected++
				if p := argBraceProblem(arg); p != "" {
					t.Errorf("%s/%s args[%d] %q: %s", name, n.ID, i, arg, p)
				}
			}
			if n.Config.ScriptFile == "@lib/manifest-append.sh" && len(n.Config.Args) > 0 {
				last := n.Config.Args[len(n.Config.Args)-1]
				if strings.Contains(last, "{{") || strings.Contains(last, "}}") {
					t.Errorf("%s/%s: last manifest-append arg %q must carry no braces", name, n.ID, last)
				}
			}
		}
	}
	if inspected == 0 {
		t.Fatal("inspected zero argv elements; the guard is vacuous")
	}
}

// No shipped sample references @lib yet, so prove the checker would resolve it
// (and reject a missing library file) rather than trust an unexercised path.
func TestOwnerFileCheckerResolvesLib(t *testing.T) {
	base, owner := sampleRoot(t)
	files := ownerFileChecker{reader: filesystem.NewFileReader(base), owner: owner}
	if !files.FileExists("ralph-loop", "@lib/manifest-append.sh") {
		t.Error("@lib/manifest-append.sh must resolve against shared/_lib")
	}
	if files.FileExists("ralph-loop", "@lib/no-such-script.sh") {
		t.Error("a missing @lib file must not resolve")
	}
	if !files.FileExists("ralph-loop", "workflow.json") {
		t.Error("a workflow-local file must resolve")
	}
}
