package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Happy path: the "version" subcommand is registered on the root command,
// parallel to "serve", "mcp", and "connection".
func TestRootCommand_HasVersionSubcommand(t *testing.T) {
	versionCmd, _, err := newRootCmd().Find([]string{"version"})
	require.NoError(t, err)
	assert.Equal(t, "version", versionCmd.Name())
}

// Error path: version command has RunE wired (not accidentally left nil).
func TestVersionCommand_HasRunE(t *testing.T) {
	cmd := newVersionCmd()
	assert.NotNil(t, cmd.RunE, "version command must have a RunE handler wired")
}

// Happy path: the version command writes the ldflags-stamped version to its
// configured output stream. With the package vars at their unstamped defaults it
// prints "dev" plus the commit and date lines.
func TestVersionCommand_PrintsStampedVars(t *testing.T) {
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})

	require.NoError(t, cmd.Execute())

	got := out.String()
	assert.Contains(t, got, version, "output must contain the stamped version var")
	assert.Contains(t, got, commit, "output must contain the stamped commit var")
	assert.Contains(t, got, date, "output must contain the stamped date var")
	assert.NotEmpty(t, strings.TrimSpace(got), "version output must be non-empty")
}

// Default (unstamped) build: the package vars carry their dev sentinels so a
// `go run`/`go test` binary reports a sane, non-empty version rather than an
// empty string.
func TestVersionVars_DefaultToDevSentinels(t *testing.T) {
	assert.Equal(t, "dev", version, "unstamped version must default to dev")
	assert.Equal(t, "none", commit, "unstamped commit must default to none")
	assert.Equal(t, "unknown", date, "unstamped date must default to unknown")
}

// Wiring: the root command's cobra Version field is set so `--version` works,
// and the value tracks the stamped version var. Cobra auto-registers the
// `--version` flag (and prints via the version template) only once the command
// is executed, so we exercise it through Execute and assert the output.
func TestRootCommand_VersionFieldSet(t *testing.T) {
	root := newRootCmd()
	assert.Equal(t, version, root.Version, "root.Version must track the stamped version var")

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), version, "--version must print the stamped version")
}

// Output stream: the command must honor a redirected output stream (writes to
// cmd.OutOrStdout()), so the installer scripts can capture it deterministically.
func TestVersionCommand_HonorsOutputStream(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})

	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), version)
}

// Format contract: the output of versionString() must follow the exact pattern
// "arbitron VERSION (commit COMMIT, built DATE)" so that upgrade scripts can
// extract the version field with a consistent grep/sed without knowing the
// exact sentinel values.
func TestVersionString_Format(t *testing.T) {
	got := versionString()
	// Pattern: starts with "arbitron ", then a non-space token (the version),
	// then " (commit ", then a non-space token (the commit hash),
	// then ", built ", then a non-space token (the date), then ")".
	assert.Regexp(t, `^arbitron \S+ \(commit \S+, built \S+\)$`, strings.TrimSpace(got),
		"versionString format must be stable for upgrade script parsing")
}

// Parity: the version subcommand and --version flag must produce identical
// output because both surfaces use the same versionString() call via
// SetVersionTemplate.
func TestVersionCommand_And_FlagProduceSameOutput(t *testing.T) {
	root1 := newRootCmd()
	var out1 bytes.Buffer
	root1.SetOut(&out1)
	root1.SetArgs([]string{"version"})
	require.NoError(t, root1.Execute())

	root2 := newRootCmd()
	var out2 bytes.Buffer
	root2.SetOut(&out2)
	root2.SetArgs([]string{"--version"})
	require.NoError(t, root2.Execute())

	assert.Equal(t, strings.TrimSpace(out1.String()), strings.TrimSpace(out2.String()),
		"'version' subcommand and '--version' flag must print identical text")
}

// Ldflags simulation: stamp the package vars with realistic values (mirroring
// what the Makefile -ldflags injection would produce) and verify versionString
// still matches the format contract. This catches any regression where a
// stamped value (e.g. a semver with dots or a git hash with hex chars) would
// break the format regexp.
func TestVersionString_WithStampedValues(t *testing.T) {
	orig := version
	origC := commit
	origD := date
	t.Cleanup(func() { version = orig; commit = origC; date = origD })

	version = "1.2.3"
	commit = "abc1234"
	date = "2026-06-05T00:00:00Z"

	got := versionString()
	assert.Equal(t, "arbitron 1.2.3 (commit abc1234, built 2026-06-05T00:00:00Z)", got)
}

// Compile-time guard: newVersionCmd returns a *cobra.Command.
var _ = func() *cobra.Command { return newVersionCmd() }
