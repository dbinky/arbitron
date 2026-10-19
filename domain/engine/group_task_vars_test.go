package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	gvParentWS = "/ws/instances/parent"
	gvTaskWS   = "/ws/instances/task-a"
)

func gvStart() map[string]any {
	return map[string]any{
		"repo":      "acme/widgets",
		"count":     float64(3),
		"cfg":       map[string]any{"k": map[string]any{"leaf": "v"}},
		"init":      map[string]any{"ticket": "T-1"},
		"workspace": gvParentWS,
	}
}

func TestComposeTaskVariables_BaseCopyMinusReservedPlusTypedInputs(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{
		"n":     "{{count}}",
		"whole": "{{cfg}}",
		"flag":  true,
		"label": "r={{repo}}",
		"repo":  "override/repo",
	}}

	got, err := engine.ComposeTaskVariables(gvStart(), task, gvTaskWS, gvParentWS)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"repo":  "override/repo",
		"count": float64(3),
		"cfg":   map[string]any{"k": map[string]any{"leaf": "v"}},
		"init":  map[string]any{"ticket": "T-1"},
		"n":     float64(3),
		"whole": map[string]any{"k": map[string]any{"leaf": "v"}},
		"flag":  true,
		"label": "r=acme/widgets",
	}, got, "inputs resolve against the START values, not against each other; reserved names never pass through")
	assert.IsType(t, float64(0), got["n"], "a whole-value reference keeps its type")
	assert.IsType(t, map[string]any{}, got["whole"])
	assert.NotContains(t, got, "workspace")
}

func TestComposeTaskVariables_WorkspaceTemplatesBindToTheTasksOwnArea(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{
		"work_dir": "{{workspace}}/repo",
		"ws":       "{{workspace}}",
	}}

	got, err := engine.ComposeTaskVariables(gvStart(), task, gvTaskWS, gvParentWS)
	require.NoError(t, err)
	assert.Equal(t, gvTaskWS+"/repo", got["work_dir"])
	assert.Equal(t, gvTaskWS, got["ws"])
}

func TestComposeTaskVariables_EveryCompositionIsDeeplyIndependent(t *testing.T) {
	start := gvStart()
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{"whole": "{{cfg}}"}}

	a, err := engine.ComposeTaskVariables(start, task, gvTaskWS, gvParentWS)
	require.NoError(t, err)
	b, err := engine.ComposeTaskVariables(start, task, "/ws/instances/task-b", gvParentWS)
	require.NoError(t, err)

	a["cfg"].(map[string]any)["k"].(map[string]any)["leaf"] = "A"
	a["whole"].(map[string]any)["k"].(map[string]any)["leaf"] = "A"

	assert.Equal(t, "v", b["cfg"].(map[string]any)["k"].(map[string]any)["leaf"])
	assert.Equal(t, "v", b["whole"].(map[string]any)["k"].(map[string]any)["leaf"])
	assert.Equal(t, "v", start["cfg"].(map[string]any)["k"].(map[string]any)["leaf"], "the stored start snapshot is never reachable from a task")
	assert.Equal(t, gvParentWS, start["workspace"], "the input map is not modified")
}

func TestComposeTaskVariables_MissingReferenceNamesTheTask(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{"x": "{{nope.path}}"}}

	got, err := engine.ComposeTaskVariables(gvStart(), task, gvTaskWS, gvParentWS)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	assert.Contains(t, err.Error(), `task "a"`)
	assert.Contains(t, err.Error(), "{{nope.path}}")
}

func TestComposeTaskVariables_WorkspaceReferenceWithoutATaskAreaIsMissing(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{"work_dir": "{{workspace}}/repo"}}

	_, err := engine.ComposeTaskVariables(gvStart(), task, "", "")
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference,
		"the parent's workspace is never a fallback for a task's {{workspace}}")
}

func TestComposeTaskVariables_ReservedInputRejected(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{"workspace": "/elsewhere"}}

	_, err := engine.ComposeTaskVariables(gvStart(), task, gvTaskWS, gvParentWS)
	assert.ErrorIs(t, err, workflow.ErrGroupReservedInput)
}

func TestComposeTaskVariables_NilStartAndInputsYieldAnEmptyMap(t *testing.T) {
	got, err := engine.ComposeTaskVariables(nil, workflow.GroupTask{Key: "a", Entry: "a1"}, "", "")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got)
}

func TestComposeTaskVariables_SharedPathRejection(t *testing.T) {
	cases := []struct {
		name    string
		extra   map[string]any
		inputs  map[string]any
		parent  string
		wantErr bool
		wantAt  string
	}{
		{name: "inherited top-level path inside the parent's area", extra: map[string]any{"work_dir": gvParentWS + "/repo"},
			parent: gvParentWS, wantErr: true, wantAt: `"work_dir"`},
		{name: "inherited path equal to the parent's area", extra: map[string]any{"root": gvParentWS},
			parent: gvParentWS, wantErr: true, wantAt: `"root"`},
		{name: "unclean inherited path still detected", extra: map[string]any{"work_dir": gvParentWS + "/./repo/"},
			parent: gvParentWS, wantErr: true, wantAt: `"work_dir"`},
		{name: "explicit nested input inside the parent's area",
			inputs: map[string]any{"opts": map[string]any{"paths": []any{"/tmp/ok", gvParentWS + "/data"}}},
			parent: gvParentWS, wantErr: true, wantAt: `"opts.paths.1"`},
		{name: "explicit string-slice input inside the parent's area",
			inputs: map[string]any{"dirs": []string{gvParentWS + "/x"}}, parent: gvParentWS, wantErr: true, wantAt: `"dirs.0"`},
		{name: "an explicit override of the inherited path fixes it", extra: map[string]any{"work_dir": gvParentWS + "/repo"},
			inputs: map[string]any{"work_dir": "{{workspace}}/repo"}, parent: gvParentWS},
		{name: "a sibling directory sharing a name prefix is not inside", extra: map[string]any{"other": gvParentWS + "-other/x"},
			parent: gvParentWS},
		{name: "relative paths are not inferred to be paths", extra: map[string]any{"rel": "repo/sub"}, parent: gvParentWS},
		{name: "inherited nested map path is rejected",
			extra: map[string]any{"meta": map[string]any{"dir": gvParentWS + "/x"}}, parent: gvParentWS, wantErr: true, wantAt: `"meta.dir"`},
		{name: "inherited array path is rejected",
			extra: map[string]any{"meta": []any{map[string]any{"dir": gvParentWS + "/x"}}}, parent: gvParentWS, wantErr: true, wantAt: `"meta.0.dir"`},
		{name: "explicit task-local replacement repairs an inherited nested path",
			extra:  map[string]any{"meta": map[string]any{"dir": gvParentWS + "/x"}},
			inputs: map[string]any{"meta": map[string]any{"dir": "{{workspace}}/x"}}, parent: gvParentWS},
		{name: "inherited typed map-slice path is rejected",
			extra: map[string]any{"meta": []map[string]any{{"dir": gvParentWS + "/x"}}}, parent: gvParentWS, wantErr: true, wantAt: `"meta.0.dir"`},
		{name: "filesystem root contains every absolute path",
			extra: map[string]any{"dir": "/tmp/data"}, parent: "/", wantErr: true, wantAt: `"dir"`},
		{name: "no parent area disables the check", extra: map[string]any{"work_dir": gvParentWS + "/repo"}, parent: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := gvStart()
			for k, v := range c.extra {
				start[k] = v
			}
			taskWS := gvTaskWS
			if c.parent == "" {
				taskWS = ""
			}
			got, err := engine.ComposeTaskVariables(start, workflow.GroupTask{Key: "a", Entry: "a1", Inputs: c.inputs}, taskWS, c.parent)
			if !c.wantErr {
				require.NoError(t, err)
				require.NotNil(t, got)
				return
			}
			require.Error(t, err)
			assert.Nil(t, got)
			assert.ErrorIs(t, err, engine.ErrGroupSharedPath)
			assert.Contains(t, err.Error(), c.wantAt)
			assert.Contains(t, err.Error(), "{{workspace}}", "the error tells the author how to fix it")
		})
	}
}

func TestComposeTaskVariables_SharedPathSymlinkAliases(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	require.NoError(t, os.Mkdir(parent, 0o700))
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(parent, alias))
	dangling := filepath.Join(root, "new-file-alias")
	require.NoError(t, os.Symlink(filepath.Join(parent, "new-file"), dangling))
	sibling := filepath.Join(root, "parent-other")
	require.NoError(t, os.Mkdir(sibling, 0o700))
	siblingAlias := filepath.Join(root, "sibling-alias")
	require.NoError(t, os.Symlink(sibling, siblingAlias))

	for _, tc := range []struct {
		name, parent, value string
		wantErr             bool
	}{
		{"value aliases parent", parent, alias, true},
		{"missing descendant of alias", parent, filepath.Join(alias, "new", "file"), true},
		{"dangling file alias", parent, dangling, true},
		{"parent uses alias", alias, parent, true},
		{"parent and value use different spellings", alias, filepath.Join(parent, "new", "file"), true},
		{"sibling alias is independent", parent, filepath.Join(siblingAlias, "new", "file"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := engine.ComposeTaskVariables(map[string]any{
				"opts": map[string]any{"paths": []any{tc.value}},
			}, workflow.GroupTask{Key: "a", Entry: "a1"}, gvTaskWS, tc.parent)
			if !tc.wantErr {
				require.NoError(t, err)
				require.NotNil(t, got)
				return
			}
			require.ErrorIs(t, err, engine.ErrGroupSharedPath)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), `"opts.paths.0"`)
		})
	}
}
