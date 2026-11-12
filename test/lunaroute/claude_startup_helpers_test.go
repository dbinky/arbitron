package lunaroute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
)

const claudeStartupVersion = "2.1.322"

// This is the experiment's main-startup check, not effective helper/limit
// evidence. The source and its deliberately narrow scope are in
// sources/lr05-startup.md. Raw startup/transcript values stay in memory.
func claudeStartupSelection(spec claudecli.RunSpec) (string, error) {
	if spec.Dir == "" || spec.Bin == "" || spec.MaxResponseBytes <= 0 || spec.MaxResponseBytes > captureBodyLimit {
		return "", errAgentEvidence
	}
	env := map[string]string{}
	for _, entry := range spec.Env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			return "", errAgentEvidence
		}
		if _, exists := env[name]; exists {
			return "", errAgentEvidence
		}
		env[name] = value
	}
	model := env["ANTHROPIC_MODEL"]
	if env["ANTHROPIC_BASE_URL"] != hostedRoot || !provider.ValidLunaRouteKey(env["ANTHROPIC_AUTH_TOKEN"]) {
		return "", errAgentEvidence
	}
	if _, err := explicitLiveModel(func(string) (string, bool) { return model, true }, "model"); err != nil {
		return "", errAgentEvidence
	}
	models, isolated := 0, 0
	for i, arg := range spec.Args {
		switch {
		case arg == "--model":
			models++
			if i+1 >= len(spec.Args) || spec.Args[i+1] != model {
				return "", errAgentEvidence
			}
		case arg == "--setting-sources":
			isolated++
			if i+1 >= len(spec.Args) || spec.Args[i+1] != "" {
				return "", errAgentEvidence
			}
		case strings.HasPrefix(arg, "--model=") || strings.HasPrefix(arg, "--setting-sources=") || arg == "--settings" || strings.HasPrefix(arg, "--settings="):
			return "", errAgentEvidence
		}
	}
	if models != 1 || isolated != 1 {
		return "", errAgentEvidence
	}
	return model, nil
}

func validateClaudeStartup(spec claudecli.RunSpec, version string, lines []string) error {
	model, err := claudeStartupSelection(spec)
	if err != nil || version != claudeStartupVersion || len(lines) == 0 {
		return errAgentEvidence
	}
	cwd, err := filepath.EvalSymlinks(spec.Dir)
	if err != nil {
		return errAgentEvidence
	}
	var session string
	initialized, terminal := false, false
	total := int64(0)
	for _, line := range lines {
		total += int64(len(line)) + 1
		if total > spec.MaxResponseBytes || terminal {
			return errAgentEvidence
		}
		decoder := json.NewDecoder(bytes.NewBufferString(line))
		var extra any
		if !uniqueJSONFields(decoder) || decoder.Decode(&extra) != io.EOF {
			return errAgentEvidence
		}
		var event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Model   string `json:"model"`
			Version string `json:"claude_code_version"`
			CWD     string `json:"cwd"`
			Session string `json:"session_id"`
			IsError *bool  `json:"is_error"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || event.Type == "" {
			return errAgentEvidence
		}
		if event.Type == "system" && event.Subtype == "init" {
			actual, err := filepath.EvalSymlinks(event.CWD)
			if initialized || err != nil || actual != cwd || event.Model != model || event.Version != version || !safeAgentField(event.Session, "") {
				return errAgentEvidence
			}
			initialized = true
			session = event.Session
			continue
		}
		switch event.Type {
		case "assistant", "user", "result":
			if !initialized {
				return errAgentEvidence
			}
		}
		if event.Type == "result" {
			if event.Session != session || event.IsError == nil || *event.IsError {
				return errAgentEvidence
			}
			terminal = true
		}
	}
	if !initialized || !terminal {
		return errAgentEvidence
	}
	return nil
}

// Constructed at the experiment Factory, around the actual runner. Both the
// bounded version query and the main process receive the adapter-emitted
// executable/env/cwd. A failed startup cannot become an accepted mission.
type claudeStartupRunner struct{ next claudecli.CommandRunner }

func (r claudeStartupRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	print, isolated := false, false
	for _, arg := range spec.Args {
		print = print || arg == "--print"
		isolated = isolated || arg == "--setting-sources"
	}
	lunaRoute := isolated
	for _, entry := range spec.Env {
		lunaRoute = lunaRoute || entry == "ANTHROPIC_BASE_URL="+hostedRoot
	}
	if !print || !lunaRoute {
		return r.next.Run(ctx, spec)
	}
	fail := func() (<-chan string, func() error, error) {
		return nil, nil, provider.LocalLunaRouteError("main startup", errAgentEvidence)
	}
	if _, err := claudeStartupSelection(spec); err != nil {
		return fail()
	}
	query := spec
	query.Args = []string{"--version"}
	query.Stdin = ""
	query.MaxResponseBytes = agentVersionLimit
	queryCtx, cancel := context.WithTimeout(ctx, agentVersionTimeout)
	versions, err := runCapturedLines(queryCtx, r.next, query)
	cancel()
	if err != nil || len(versions) != 1 || strings.TrimSpace(versions[0]) != claudeStartupVersion+" (Claude Code)" {
		return fail()
	}
	lines, err := runCapturedLines(ctx, r.next, spec)
	if err != nil {
		// Preserve the production diagnosis seam for an actual process failure,
		// but never pass through its raw stderr or exec error.
		return nil, nil, errAgentEvidence
	}
	if validateClaudeStartup(spec, claudeStartupVersion, lines) != nil {
		return fail()
	}
	out := make(chan string, len(lines))
	for _, line := range lines {
		out <- line
	}
	close(out)
	return out, func() error { return nil }, nil
}
