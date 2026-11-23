package claudecli

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// claudeBackend is the effective-backend identity the claude-cli adapters pass to
// CapabilityRules.Emit when a binding carries no resolved Backend (e.g. the chat
// path, whose ChatRequest carries no axes). Emit treats "claude-code" and
// "claude-cli" identically — both drive the `claude` CLI — so this is the safe
// default that keeps chat + agent on one emission contract.
const claudeBackend = "claude-code"

// applyCapabilityEnv appends the capability-derived env (context window, effort)
// to an existing env slice, computed from the binding via the capability rules.
// Backend is the EFFECTIVE backend already resolved onto the binding; an empty
// Backend falls back to claudeBackend (both claude-cli adapters drive the `claude`
// CLI, and Emit maps the two identically). A default tier + default effort emits
// nothing, so this is a no-op for unconfigured bindings.
//
// SECURITY: the appended vars are non-secret policy knobs (effort level, context
// window) — safe to log by key. They ride the SAME per-call env slice as the
// credential vars and are appended AFTER them, never replacing the key.
func applyCapabilityEnv(env []string, binding provider.ResolvedBinding, rules provider.CapabilityRules) ([]string, error) {
	// No capability policy configured (adapter built without rules / tests): emit
	// nothing rather than treating every backend as unknown.
	if rules.Empty() {
		return env, nil
	}
	backend := binding.Backend
	if backend == "" {
		backend = claudeBackend
	}
	// The ResolvedBinding axes use "" as the unset/default sentinel (Go zero value);
	// normalize to the explicit default values Emit understands so an unset axis is
	// a no-op rather than ErrInvalid*.
	tier := binding.ContextWindow
	if tier == "" {
		tier = provider.ContextDefault
	}
	effort := binding.Effort
	if effort == "" {
		effort = provider.EffortDefault
	}
	em, err := rules.Emit(backend, binding.Model, tier, effort)
	if err != nil {
		return nil, err
	}
	for k, v := range em.Env {
		env = append(env, k+"="+v)
	}
	return env, nil
}

// validateWorkDir enforces the per-call WorkDir invariants shared by every
// claude-cli adapter:
//   - non-empty (avoid the silent "inherit engine cwd" fallback that would
//     hand the agent the launch directory containing arbitron.db, config.yaml,
//     and other secrets — PR #8 thread 3291944671 item 1)
//   - resolves to an absolute path under workspaceRoot when one is configured
//     (PR #8 thread 3291944671 item 2)
//
// An empty workspaceRoot disables the pin check (test convenience only).
func validateWorkDir(workDir, workspaceRoot string) error {
	if workDir == "" {
		return ErrWorkDirRequired
	}
	if workspaceRoot == "" {
		return nil
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("claudecli: resolve WorkDir: %w", err)
	}
	abs = filepath.Clean(abs)
	root := filepath.Clean(workspaceRoot)
	// Allow root itself, or any subdir of root. Use a separator-terminated
	// prefix check to avoid /root/wsX shadowing /root/ws.
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q not under %q", ErrWorkDirEscapesRoot, abs, root)
	}
	return nil
}

// chatEnvWithBinding builds the tool-suppressed chat subprocess env: the
// controlled base env (procenv.BaseEnv — allowlist-filtered, no engine secrets)
// with the capability env appended via the same applyCapabilityEnv helper the
// agent path uses, so chat and agent share ONE emission seam and cannot drift.
//
// A zero binding produces no capability additions (default tier/effort emit
// nothing) — this is the behavior for the subscription/no-profile path.
// A binding with non-default axes (e.g. Context200K + EffortHigh) emits the
// matching env vars exactly as the agent path does.
//
// applyCapabilityEnv only errors on a non-empty ruleset with an unknown backend;
// a zero/empty binding falls back to claudeBackend (known), so this cannot error
// in practice. A defensive error is logged at WARN (with the backend+model) and
// then the bare base env is returned, because the chat spec builder has no error
// channel and a missing non-secret policy knob must never block a chat turn. The
// WARN log makes the misconfiguration observable on the chat path, matching the
// agent path (which surfaces the error) — same input must not fail two different,
// invisible ways. (Bridge: when the resolver owns flooring the emit step becomes
// total and this swallow disappears entirely.)
func (c *adapterCore) chatEnvWithBinding(binding provider.ResolvedBinding) []string {
	env, err := applyCapabilityEnv(procenv.BaseEnv(), binding, c.rules)
	if err != nil {
		slog.Warn("claudecli: chat capability env emission failed; proceeding with the base env (no axis additions)",
			slog.String("backend", binding.Backend),
			slog.String("model", binding.Model),
			slog.String("error", err.Error()))
		return procenv.BaseEnv()
	}
	return env
}

// collectPolicy applies route-specific safety and identity rules before a
// terminal result is accepted, including calls without a legacy pin.
type collectPolicy struct {
	sanitize     bool
	acceptModels func(expected string, reported []string) bool
}

// collectResult retains the existing non-LunaRoute protocol: drain and reap,
// surface failures, and (when required) prove the main model while permitting
// helper models. Several results occur when background subagents re-wake the
// session; each must pass and the last one carries the final answer.
// Route-specific callers use collectResultWithPolicy instead.
func collectResult(ctx context.Context, runner CommandRunner, spec RunSpec, wantModel string) (streamEvent, []streamEvent, error) {
	return collectResultWithPolicy(ctx, runner, spec, wantModel, collectPolicy{})
}

// singleResult reports whether the route requires exactly one terminal result.
// Ordinary pinned turns accept one result per background-subagent wake (the
// last wins); a route-specific identity policy (LunaRoute) stays fail-closed on
// any second result, because its proof must come from one unambiguous event.
func (p collectPolicy) singleResult() bool { return p.acceptModels != nil }

func (p collectPolicy) accepts(want string, used []string) bool {
	if p.acceptModels != nil {
		return p.acceptModels(want, used)
	}
	return executedModelMatches(want, used)
}

func collectResultWithPolicy(ctx context.Context, runner CommandRunner, spec RunSpec, wantModel string, policy collectPolicy) (streamEvent, []streamEvent, error) {
	verified := wantModel != "" || policy.acceptModels != nil
	sanitize := verified || policy.sanitize
	lines, wait, err := runner.Run(ctx, spec)
	if err != nil {
		if sanitize {
			err = SanitizeProcessError(err)
		}
		return streamEvent{}, nil, fmt.Errorf("claudecli: call: %w", err)
	}

	var tracker outcomeTracker
	var result streamEvent
	var haveResult bool
	var parseErr error
	var steps []streamEvent
	for line := range lines {
		if parseErr != nil {
			continue // keep draining; don't process further
		}
		ev, ok, perr := parseEvent([]byte(line))
		if perr != nil {
			if policy.sanitize {
				perr = SanitizeProcessError(perr)
			}
			parseErr = perr
			continue
		}
		if !ok {
			continue
		}
		tracker.observe(ev)
		if ev.Kind == eventRateLimit {
			continue
		}
		if ev.Kind == eventResult {
			// Background-subagent wakes emit one result each; every one must
			// prove the pinned model, and the last (the final answer) wins.
			// A route-specific policy still rejects any second result.
			if verified && ((haveResult && policy.singleResult()) || ev.IsError || !policy.accepts(wantModel, ev.ModelUsage)) {
				parseErr = errModelExecutionEvidence
				continue
			}
			// The last result carries the answer; usage is per wake, so sum it
			// (as streamState does) rather than keep only the final wake's.
			if haveResult {
				ev.TokensIn += result.TokensIn
				ev.TokensOut += result.TokensOut
			}
			result, haveResult = ev, true
			continue
		}
		steps = append(steps, ev)
	}
	if waitErr := wait(); waitErr != nil {
		waitErr = tracker.attach(waitErr)
		if sanitize {
			waitErr = SanitizeProcessError(waitErr)
		} else {
			LogStderrTail("claudecli", waitErr)
		}
		// Keep cancellation classifiable even when the process reports only its
		// exit signal. Verified calls retain only the sanitized process error.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return streamEvent{}, nil, fmt.Errorf("claudecli: mission timed out: %w (%v)", ctxErr, waitErr)
		}
		return streamEvent{}, nil, fmt.Errorf("claudecli: claude exited with error: %w", waitErr)
	}
	if parseErr != nil {
		err := tracker.attach(parseErr)
		if _, classified := tracker.classify(); sanitize && classified {
			err = SanitizeProcessError(err)
		}
		return streamEvent{}, nil, err
	}
	if !haveResult {
		return streamEvent{}, nil, fmt.Errorf("claudecli: no result event in claude output")
	}
	if result.IsError {
		if policy.sanitize {
			return streamEvent{}, nil, SanitizeProcessError(tracker.attach(errModelExecutionEvidence))
		}
		return streamEvent{}, nil, tracker.attach(fmt.Errorf("claudecli: claude reported an error: %s", result.Text))
	}
	tracker.reportSuccess(ctx)
	return result, steps, nil
}
