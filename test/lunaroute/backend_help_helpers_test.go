package lunaroute_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// Help establishes CLI spellings only, not effective configuration, auth,
// metadata consumption, helper routing or a successful inference.
type backendHelpProof struct {
	Backend string
	Version string
	Options []string
}

var backendHelpVersion = regexp.MustCompile(`^(codex-cli )?[0-9]+\.[0-9]+\.[0-9]+$`)

func parseBackendHelp(backend string, version, help []byte) (backendHelpProof, error) {
	fail := func() (backendHelpProof, error) { return backendHelpProof{}, errBackendEvidence }
	v := strings.TrimSpace(string(version))
	if len(version) > agentVersionLimit || !backendHelpVersion.MatchString(v) || len(help) == 0 || len(help) > backendOutputLimit {
		return fail()
	}
	var required []string
	switch backend {
	case "codex":
		if !strings.HasPrefix(v, "codex-cli ") || !strings.Contains("\n"+string(help), "\nUsage: codex exec ") {
			return fail()
		}
		required = []string{"--config", "--model", "--dangerously-bypass-approvals-and-sandbox", "--cd", "--json"}
	case "pi":
		if strings.HasPrefix(v, "codex-cli ") || !strings.Contains("\n"+string(help), "\nUsage:\n  pi ") {
			return fail()
		}
		required = []string{"--model", "--mode", "--print", "--no-session", "--thinking"}
	default:
		return fail()
	}
	options := map[string]bool{}
	inOptions := false
	for _, line := range strings.Split(string(help), "\n") {
		if line == "Options:" {
			inOptions = true
			continue
		}
		if inOptions && line != "" && line[0] != ' ' && line[0] != '\t' {
			break
		}
		if !inOptions {
			continue
		}
		for _, field := range strings.Fields(line) {
			if !strings.HasPrefix(field, "-") {
				break
			}
			options[strings.TrimSuffix(field, ",")] = true
		}
	}
	for _, option := range required {
		if !options[option] {
			return fail()
		}
	}
	slices.Sort(required)
	return backendHelpProof{Backend: backend, Version: v, Options: required}, nil
}

// Both queries use the same resolved executable and private environment. The
// allowlist never forwards provider credentials, proxies or runtime injection
// settings from the parent process. No inference is launched by these queries.
func collectBackendHelp(ctx context.Context, backend, bin, root string) (backendHelpProof, error) {
	if backend != "codex" && backend != "pi" || !filepath.IsAbs(bin) || os.Getenv("PATH") == "" {
		return backendHelpProof{}, errBackendEvidence
	}
	home, err := os.MkdirTemp(root, "backend-help-")
	if err != nil {
		return backendHelpProof{}, errBackendEvidence
	}
	collect := func() (backendHelpProof, error) {
		env := []string{"PATH=" + os.Getenv("PATH")}
		for _, entry := range []struct{ key, name string }{
			{"HOME", "home"}, {"TMPDIR", "tmp"}, {"XDG_CONFIG_HOME", "config"},
			{"XDG_DATA_HOME", "data"}, {"XDG_CACHE_HOME", "cache"},
		} {
			path := filepath.Join(home, entry.name)
			if os.Mkdir(path, 0700) != nil {
				return backendHelpProof{}, errBackendEvidence
			}
			env = append(env, entry.key+"="+path)
		}
		private, workspace := filepath.Join(home, "agent"), filepath.Join(home, "workspace")
		if os.Mkdir(private, 0700) != nil || os.Mkdir(workspace, 0700) != nil {
			return backendHelpProof{}, errBackendEvidence
		}
		helpArgs := []string{"exec", "--help"}
		if backend == "codex" {
			env = append(env, "CODEX_HOME="+private)
		} else {
			env = append(env, "PI_CODING_AGENT_DIR="+private, "PI_OFFLINE=1")
			helpArgs = []string{"--help"}
		}
		query := func(args []string, cap int64) ([]byte, error) {
			queryCtx, cancel := context.WithTimeout(ctx, agentVersionTimeout)
			defer cancel()
			lines, err := runCapturedLines(queryCtx, claudecli.NewExecRunner(), claudecli.RunSpec{
				Bin: bin, Args: args, Env: env, Dir: workspace, MaxResponseBytes: cap,
			})
			if err != nil {
				return nil, errBackendEvidence
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
		version, err := query([]string{"--version"}, agentVersionLimit)
		if err != nil {
			return backendHelpProof{}, errBackendEvidence
		}
		help, err := query(helpArgs, backendOutputLimit)
		if err != nil {
			return backendHelpProof{}, errBackendEvidence
		}
		return parseBackendHelp(backend, version, help)
	}
	proof, collectErr := collect()
	if err := os.RemoveAll(home); err != nil || collectErr != nil {
		return backendHelpProof{}, errBackendEvidence
	}
	return proof, nil
}
