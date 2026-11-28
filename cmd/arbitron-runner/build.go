package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	adapterprobe "github.com/arbitron/arbitron/adapter/probe"
	"github.com/arbitron/arbitron/adapter/process"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/adapter/runneragent/copies"
	"github.com/arbitron/arbitron/adapter/runneragent/executor"
	"github.com/arbitron/arbitron/adapter/runneragent/identity"
	"github.com/arbitron/arbitron/adapter/runneragent/sources"
	"github.com/arbitron/arbitron/adapter/runneragent/spool"
	"github.com/arbitron/arbitron/adapter/runneragent/transfer"
	"github.com/arbitron/arbitron/adapter/runneragent/workspace"
	"github.com/arbitron/arbitron/adapter/runneragent/wsclient"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/adapter/telemetry"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

const (
	maxResponseBytes = 16 << 20
	chatHTTPTimeout  = 10 * time.Minute
	probeHTTPTimeout = 30 * time.Second
)

// buildExecutor uses this machine's native verifier and local credentials.
func buildExecutor(cfg config.Config, local *providersadapter.Factory) (*executor.Executor, error) {
	return buildExecutorFor(cfg, local, runnerprobe.NewConfirmationExecutor(local), nil, nil)
}

func buildExecutorFor(cfg config.Config, local *providersadapter.Factory, confirmations runner.ConfirmationExecutor, advertised func(runner.CredentialKind, string) []runner.ExecutionCapability, ws *executor.WorkspaceJobs) (*executor.Executor, error) {
	var lr *executor.LunaRoute
	if cfg.LunaRoute.Enabled() {
		lr = &executor.LunaRoute{ConnectionRef: cfg.LunaRoute.ConnectionRef, APIKey: cfg.LunaRoute.APIKey}
	}
	return executor.New(local, executor.Config{ScratchRoot: cfg.ScratchRoot, LunaRoute: lr, Confirmations: confirmations, Advertised: advertised, Workspace: ws})
}

// buildAgent is the runner's composition root: every adapter is constructed
// here and handed to the domain agent.
func buildAgent(ctx context.Context, cfg config.Config, logger *slog.Logger) (*runneragent.Agent, error) {
	for _, d := range []string{cfg.ScratchRoot, cfg.SpoolDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("arbitron-runner: %w", err)
		}
	}
	rules, err := configadapter.LoadCapabilityRules(filepath.Join(cfg.ConfigDir, "capabilities.yaml"))
	if err != nil {
		return nil, fmt.Errorf("arbitron-runner: capability rules: %w", err)
	}
	if rules.Empty() {
		return nil, errors.New("arbitron-runner: capability rules: empty ruleset")
	}
	httpClient := &http.Client{}
	claudeRunner := claudecli.NewExecRunner()
	claudeBin, _ := claudecli.ResolveBinary(cfg.ClaudeBinary)
	factory := providersadapter.NewFactory(providersadapter.Deps{
		ClaudeChatConfig:  claudecli.Config{BinaryPath: claudeBin, WorkspaceRoot: cfg.ScratchRoot, MaxResponseBytes: maxResponseBytes, Runner: claudeRunner, CapabilityRules: rules},
		ClaudeAgentConfig: claudecli.Config{BinaryPath: claudeBin, WorkspaceRoot: cfg.ScratchRoot, MaxResponseBytes: maxResponseBytes, Runner: claudeRunner, CapabilityRules: rules},
		HTTPClient:        httpClient,
		HTTPTimeout:       chatHTTPTimeout,
		MaxResponseBytes:  maxResponseBytes,
		CapabilityRules:   rules,
		CodexBinaryPath:   cfg.CodexBinary,
	})

	var lrProbe provider.ConnectionProbe
	if cfg.LunaRoute.Enabled() {
		lrProbe, err = adapterprobe.NewProbeFactory(adapterprobe.Deps{HTTPClient: httpClient, Timeout: probeHTTPTimeout, MaxResponseBytes: 1 << 20}).
			ProbeFor(provider.ResolvedBinding{Provider: provider.CanonicalLunaRoute})
		if err != nil {
			return nil, fmt.Errorf("arbitron-runner: lunaroute probe: %w", err)
		}
	}
	wsRoot, wsReason := config.VerifyWorkspaceRoot(cfg.WorkspaceRoot)
	if wsReason != "" {
		logger.Warn("workspace root not advertised; this runner is chat-only", "reason", wsReason)
	}
	var wsJobs *executor.WorkspaceJobs
	var pulls runneragent.MaintenancePuller
	var janitor runneragent.CopyJanitor
	if wsRoot != "" {
		var syncer *workspace.Syncer
		if wsJobs, syncer, err = buildWorkspaceJobs(cfg, wsRoot, httpClient, claudeRunner, claudeBin, rules); err != nil {
			return nil, err
		}
		pulls, janitor = syncer, syncer
	}
	confirmations := runnerprobe.NewConfirmationExecutor(factory)
	codexCfg := codex.Config{BinaryPath: cfg.CodexBinary, Runner: claudeRunner, MaxResponseBytes: maxResponseBytes, CapabilityRules: rules}
	discoverIn := sources.DiscoverInput{
		ClaudeBinary: cfg.ClaudeBinary, Runner: claudeRunner, LunaRoute: cfg.LunaRoute, LunaRouteProbe: lrProbe,
		Rules: rules, Confirmations: confirmations,
		Native: sources.NativeExecutionInput{Codex: codexCfg, Rules: rules, WorkspaceEnabled: wsJobs != nil && wsReason == ""},
	}
	verifier := sources.NewClaudeModelVerifier(confirmations, cfg.ScratchRoot, logger)
	inv := sources.NewRefreshing(ctx, sources.VerifiedDiscover(discoverIn, verifier), time.Now)
	inv.EnableClaudeVerification(verifier, discoverIn)
	capabilities := []string{runneragent.OpChat, runneragent.OpStream}
	if wsJobs != nil && wsReason == "" {
		wsJobs.SetInventory(inv)
		capabilities = append(capabilities, runneragent.OpProcess, runneragent.OpAgent)
	}
	exec, err := buildExecutorFor(cfg, factory, confirmations, inv.CapabilitiesFor, wsJobs)
	if err != nil {
		return nil, err
	}

	sp, err := spool.Open(cfg.SpoolDir, cfg.OutputBufferFrames)
	if err != nil {
		return nil, err
	}
	conn, err := wsclient.New(wsclient.Options{ServerURL: cfg.ServerURL, RunnerKey: cfg.RunnerKey, Headers: cfg.ExtraHeaders, HTTPClient: httpClient})
	if err != nil {
		return nil, err
	}
	return runneragent.New(runneragent.Config{
		OS: runtime.GOOS, Arch: runtime.GOARCH, ProtocolVersion: runnerproto.ProtocolVersion, RunnerVersion: version,
		HarnessVersions: inv.HarnessVersions(), WorkspaceRoot: wsRoot, ScratchRoot: cfg.ScratchRoot,
		Capabilities: capabilities,
		Heartbeat:    cfg.Heartbeat, ProbeInterval: cfg.ProbeInterval,
	}, runneragent.Deps{
		Copies: janitor, Pulls: pulls, Conn: conn, Exec: exec, Sources: inv, Models: inv, Telemetry: telemetry.NewForRoot(cfg.WorkspaceRoot),
		Spool: sp, Identity: identity.New(cfg.MachineKeyPath()),
		Clock: runneragent.SystemClock{}, Rand: rand.Float64, Logger: logger,
	})
}

// buildWorkspaceJobs wires the P3 T9 process/agent executor: copy records and
// bundles live under the runner's config dir; local copies live at the verified
// workspace root; agent jobs use the same loaded capability rules as discovery.
func buildWorkspaceJobs(cfg config.Config, wsRoot string, httpClient *http.Client, claudeRunner claudecli.CommandRunner, claudeBin string, rules provider.CapabilityRules) (*executor.WorkspaceJobs, *workspace.Syncer, error) {
	copyStore, err := copies.NewFileStore(filepath.Join(cfg.ConfigDir, "copies"))
	if err != nil {
		return nil, nil, fmt.Errorf("arbitron-runner: copy store: %w", err)
	}
	xfer := transfer.NewClient(cfg.ServerURL, cfg.RunnerKey, httpClient, cfg.ExtraHeaders)
	syncer, err := workspace.NewSyncer(workspace.SyncerDeps{WorkspaceRoot: wsRoot, LocalRoot: wsRoot, Transfer: xfer, Copies: copyStore})
	if err != nil {
		return nil, nil, fmt.Errorf("arbitron-runner: %w", err)
	}
	bundleCache, err := executor.NewBundleCache(filepath.Join(cfg.ConfigDir, "bundles"), xfer)
	if err != nil {
		return nil, nil, fmt.Errorf("arbitron-runner: bundle cache: %w", err)
	}
	if claudeBin == "" {
		claudeBin = cfg.ClaudeBinary
	}
	jobs, err := executor.NewWorkspaceJobs(executor.WorkspaceJobsDeps{
		Syncer: syncer, Bundles: bundleCache, Process: process.NewLocalProcessRunner(),
		ClaudeBinary: claudeBin, CodexBinary: cfg.CodexBinary, MaxResponseBytes: maxResponseBytes,
		HarnessRunner: claudeRunner, Rules: rules,
	})
	return jobs, syncer, err
}
