package codex

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCodexCheckVersion(t *testing.T) {
	for v, want := range map[string]bool{"0.170.0": true, "0.171.2": true, "1.0.0": true, "0.169.9": false, "0.137.0": false, "": false, "x": false} {
		ok, reason := CheckVersion(v)
		assert.Equal(t, want, ok, v)
		if want {
			assert.Empty(t, reason)
		} else {
			assert.NotEmpty(t, reason)
		}
	}
	ok, reason := CheckVersion("0.169.9")
	assert.False(t, ok)
	assert.Equal(t, "codex 0.169.9 is below 0.170.0 (rollout codex_error_info and token_count rate_limits)", reason)
}

func TestVersionEvidence(t *testing.T) {
	cases := []struct {
		name       string
		script     runScript
		version    string
		compatible bool
		reason     string
		missing    bool
	}{
		{"compatible", runScript{lines: []string{"codex-cli 0.170.0"}}, "0.170.0", true, "", false},
		{"below capability floor", runScript{lines: []string{"codex-cli 0.163.0"}}, "0.163.0", false, "codex 0.163.0 is below 0.170.0 (rollout codex_error_info and token_count rate_limits)", false},
		{"below execution floor", runScript{lines: []string{"codex-cli 0.100.0"}}, "0.100.0", false, "codex 0.100.0 is below the execution floor 0.137.0", false},
		{"unparseable", runScript{lines: []string{"codex dev build"}}, "", false, "codex --version output is not parseable", false},
		{"exit failure", runScript{waitErr: errors.New("exit status 1")}, "", false, "codex --version failed", false},
		{"missing", runScript{startErr: errors.New("exec: \"codex\": executable file not found in $PATH")}, "", false, "codex binary not found", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, ok, reason, err := VersionEvidence(context.Background(), Config{BinaryPath: "codex", Runner: &multiRunner{scripts: []runScript{tc.script}}})
			assert.Equal(t, tc.version, v)
			assert.Equal(t, tc.compatible, ok)
			assert.Equal(t, tc.reason, reason)
			if tc.missing {
				assert.ErrorIs(t, err, ErrCodexMissing)
				assert.NotContains(t, err.Error(), "$PATH", "sanitized")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
