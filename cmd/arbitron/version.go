package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Build-time version metadata. These are overridden at build time with
// -ldflags "-X main.version=… -X main.commit=… -X main.date=…" (see the
// Makefile dist/release targets and the release workflow). When unstamped (a
// plain `go build`/`go run`/`go test`) they carry dev sentinels so the binary
// always reports a sane, non-empty version.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// versionString renders the human-readable version line embedded in the binary.
// It is the single source of truth shared by the `version` subcommand and the
// root `--version` template so both surfaces print identical text.
func versionString() string {
	return fmt.Sprintf("arbitron %s (commit %s, built %s)", version, commit, date)
}

// newVersionCmd builds the `version` subcommand. It prints the ldflags-stamped
// build metadata to the command's output stream so installer scripts can read
// the installed version for upgrade comparison.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the arbitron version, commit, and build date",
		Long:  "Print the build metadata (version, git commit, build date) stamped into this binary.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), versionString())
			return nil
		},
	}
}
