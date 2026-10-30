package workflow

import (
	"errors"
	"testing"
)

func TestParseLibPath_Valid(t *testing.T) {
	cases := map[string]string{
		"@lib/a.md":        "a.md",
		"@lib/a/b/c.sh":    "a/b/c.sh",
		"@lib/dir/.hidden": "dir/.hidden",
	}
	for in, want := range cases {
		rel, isLib, err := ParseLibPath(in)
		if err != nil || !isLib || rel != want {
			t.Errorf("ParseLibPath(%q) = (%q, %v, %v), want (%q, true, nil)", in, rel, isLib, err, want)
		}
	}
}

func TestParseLibPath_NonLib(t *testing.T) {
	for _, in := range []string{"scripts/x.sh", "@LIB/x", "./@lib/x", "lib/x"} {
		rel, isLib, err := ParseLibPath(in)
		if rel != "" || isLib || err != nil {
			t.Errorf("ParseLibPath(%q) = (%q, %v, %v), want (\"\", false, nil)", in, rel, isLib, err)
		}
	}
}

func TestParseLibPath_Rejected(t *testing.T) {
	for _, in := range []string{
		"@lib", "@lib/", "@lib/../x", "@lib/a/../../x", "@lib/./x", "@lib//x",
		"@lib//etc/passwd", `@lib/a\b`, "@lib/a\x00b", "@lib/a/",
		"@lib/workflow.json", "@lib/Layout.JSON",
	} {
		_, isLib, err := ParseLibPath(in)
		if !isLib || !errors.Is(err, ErrInvalidLibPath) {
			t.Errorf("ParseLibPath(%q): isLib=%v err=%v, want isLib and ErrInvalidLibPath", in, isLib, err)
		}
	}
}

func TestParseLibPath_RejectsBeforeClean(t *testing.T) {
	// path.Clean("../x") would not yield "x", but "a/../x" would; the
	// parser must reject rather than normalize.
	for _, in := range []string{"@lib/../x", "@lib/a/../x"} {
		rel, isLib, err := ParseLibPath(in)
		if rel != "" || !isLib || !errors.Is(err, ErrInvalidLibPath) {
			t.Errorf("ParseLibPath(%q) = (%q, %v, %v), want rejection", in, rel, isLib, err)
		}
	}
}

func TestValidLibRel(t *testing.T) {
	for _, ok := range []string{"a.md", "a/b/c.sh", ".hidden", "sub/workflow.json"} {
		if err := ValidLibRel(ok); err != nil {
			t.Errorf("ValidLibRel(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "/a", "a//b", "a/", "./a", "a/.", "..", "a\\b", "a\x00b", "WORKFLOW.json", "layout.json"} {
		if err := ValidLibRel(bad); !errors.Is(err, ErrInvalidLibPath) {
			t.Errorf("ValidLibRel(%q) = %v, want ErrInvalidLibPath", bad, err)
		}
	}
}

func TestIsReservedWorkflowName(t *testing.T) {
	for _, n := range []string{"_lib", "_LIB", "_Lib", "@lib", "@LIB"} {
		if !IsReservedWorkflowName(n) {
			t.Errorf("IsReservedWorkflowName(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"lib", "_lib2", "my_lib", ""} {
		if IsReservedWorkflowName(n) {
			t.Errorf("IsReservedWorkflowName(%q) = true, want false", n)
		}
	}
}
