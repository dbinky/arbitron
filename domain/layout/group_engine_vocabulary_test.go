package layout

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/require"
)

// declaredValues returns every string constant of the named type in the
// instance package's group run vocabulary, so a new engine value fails here.
func declaredValues(t *testing.T, typ string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "../instance/group_run.go", nil, 0)
	require.NoError(t, err)
	var out []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != typ {
				continue
			}
			for _, v := range vs.Values {
				s, err := strconv.Unquote(v.(*ast.BasicLit).Value)
				require.NoError(t, err)
				out = append(out, s)
			}
		}
	}
	require.NotEmpty(t, out, typ)
	return out
}

// Paired row 3: the live route renders engine panel values through the reviewed
// OverlayBox; a value outside the measured vocabulary or the review probes would
// render unreviewed "unsupported group overlay" text.
func TestGroupVocabulary_CoversEveryEngineEmittedOverlayValue(t *testing.T) {
	runStates := append(declaredValues(t, "GroupRunState"), engine.GroupPanelStateUnknown)
	taskStatuses := declaredValues(t, "GroupTaskStatus")
	liveStatuses := append(slices.Clone(taskStatuses), engine.LiveStateUnknown)
	for _, form := range []string{"branches", "workflows"} {
		t.Run(form, func(t *testing.T) {
			g, err := BuildGraph(capturedGroupWorkflow(form, false))
			require.NoError(t, err)
			keys := map[string]bool{}
			for _, item := range groupTextItems(g, DefaultContract()) {
				keys[item.Key] = true
			}
			probes := GroupProbeNames(g.Groups)
			require.NotEmpty(t, g.Groups.Order)
			for _, gid := range g.Groups.Order {
				cfg := g.Groups.Groups[gid].Config
				footer := GroupElementID(gid, "footer", "")
				for _, s := range runStates {
					require.True(t, keys[GroupTextKey(footer, "overlay:status:"+s)], "run state %q is not measured", s)
					require.Contains(t, probes, "group:run:"+s)
				}
				// countProgress counts each roster task at most once.
				for n := 0; n <= len(cfg.Tasks); n++ {
					for _, kind := range []string{"done", "active", "not started", "failed"} {
						require.True(t, keys[GroupTextKey(footer, fmt.Sprintf("overlay:progress:%s:%d", kind, n))], "%s count %d", kind, n)
					}
				}
				role := "lane"
				if form == "workflows" {
					role = "task"
				}
				for _, task := range cfg.Tasks {
					element := GroupElementID(gid, role, task.Key)
					require.True(t, keys[GroupTextKey(footer, "overlay:winner:"+task.Key)])
					for _, s := range liveStatuses {
						require.True(t, keys[GroupTextKey(element, "overlay:status:"+s)], "task status %q is not measured", s)
						require.Contains(t, probes, "group:task:"+s)
					}
					// FrozenTask.Status is a GroupTaskStatus; frozen is never "unknown".
					for _, s := range taskStatuses {
						require.True(t, keys[GroupTextKey(element, "overlay:frozen:"+s)], "frozen status %q is not measured", s)
					}
				}
			}
		})
	}
}
