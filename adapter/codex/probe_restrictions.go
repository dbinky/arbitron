package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
)

const probeIdentityPrompt = "Reply with only your model identity on one line. Do not use tools or take any other action."
const minProbeVersion = "0.163.0"

// See testdata/probe-restrictions.md: empty tables deep-merge, so each inherited
// MCP server must be disabled by name. Plugin/app capabilities are independent.
func probeRestrictionArgs() []string {
	return append([]string{"--strict-config"}, probeConfigOverrides()...)
}

// probeConfigOverrides are the fixed restrictions without `--strict-config`,
// which 0.163.0 refuses for `codex mcp` (exit 1); the turn itself stays strict.
func probeConfigOverrides() []string {
	return []string{"-c", "features.plugins=false", "-c", "features.apps=false", "-c", `web_search="disabled"`, "-c", execNetworkOff}
}

// probeMCPOverride enumerates configuration, never model membership or proof.
// It uses the execution binary/account/cwd and the check's existing deadline.
// No listing output (which may contain credentials) reaches diagnostics.
func (c *core) probeMCPOverride(ctx context.Context, workDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(c.version) != 3 || compareVersion(c.version, mustParseVersion(minProbeVersion)) < 0 {
		return "", fmt.Errorf("%w: restricted probes require Codex >= %s", ErrModelEvidence, minProbeVersion)
	}
	args := append(probeConfigOverrides(), "mcp", "list", "--json")
	lines, wait, err := c.runner.Run(ctx, claudecli.RunSpec{Bin: c.binPath, Args: args, Dir: workDir, Env: codexEnv(), MaxResponseBytes: procenv.ProbeResponseBytes})
	if err != nil {
		return "", fmt.Errorf("%w: restriction configuration unavailable", ErrModelEvidence)
	}
	var raw strings.Builder
	var total int64
	first := true
	for line := range lines {
		separator := !first
		first = false
		total += int64(len(line))
		if separator {
			total++
		}
		if total <= procenv.ProbeResponseBytes {
			if separator {
				raw.WriteByte('\n')
			}
			raw.WriteString(line)
		}
	}
	if wait() != nil || ctx.Err() != nil || total > procenv.ProbeResponseBytes {
		return "", fmt.Errorf("%w: restriction configuration read failed", ErrModelEvidence)
	}
	var servers []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(raw.String()), &servers) != nil || servers == nil {
		return "", fmt.Errorf("%w: invalid restriction configuration", ErrModelEvidence)
	}
	names := make([]string, 0, len(servers))
	seen := map[string]bool{}
	for _, s := range servers {
		if s.Name == "" || seen[s.Name] {
			return "", fmt.Errorf("%w: invalid restriction configuration", ErrModelEvidence)
		}
		seen[s.Name] = true
		names = append(names, s.Name)
	}
	sort.Strings(names)
	overrides := make([]string, 0, len(names))
	for _, name := range names {
		// JSON basic-string escaping is also valid TOML basic-string escaping.
		quoted, _ := json.Marshal(name)
		overrides = append(overrides, string(quoted)+"={enabled=false}")
	}
	return "mcp_servers={" + strings.Join(overrides, ",") + "}", nil
}
