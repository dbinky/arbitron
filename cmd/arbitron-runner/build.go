package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	adapterprobe "github.com/arbitron/arbitron/adapter/probe"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/adapter/runneragent/executor"
	"github.com/arbitron/arbitron/adapter/runneragent/identity"
	"github.com/arbitron/arbitron/adapter/runneragent/sources"
	"github.com/arbitron/arbitron/adapter/runneragent/spool"
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
	return buildExecutorFor(cfg, local, nil)
}

func buildExecutorFor(cfg config.Config, local *providersadapter.Factory, advertised func(runner.CredentialKind, string) []runner.ExecutionCapability) (*executor.Executor, error) {
	var lr *executor.LunaRoute
	if cfg.LunaRoute.Enabled() {
		lr = &executor.LunaRoute{ConnectionRef: cfg.LunaRoute.ConnectionRef, APIKey: cfg.LunaRoute.APIKey}
	}
	return executor.New(local, executor.Config{ScratchRoot: cfg.ScratchRoot, LunaRoute: lr, Confirmations: runnerprobe.NewConfirmationExecutor(local), Advertised: advertised})
}

// buildAgent is the runner's composition root: every adapter is constructed
// here and handed to the domain agent.
func buildAgent(ctx context.Context, cfg config.Config, logger *slog.Logger) (*runneragent.Agent, error) {
	for _, d := range []string{cfg.ScratchRoot, cfg.SpoolDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("arbitron-runner: %w", err)
		}
	}
	rules, err := configadapter.LoadCapabilityRules("")
	if err != nil {
		return nil, fmt.Errorf("arbitron-runner: capability rules: %w", err)
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
	})

	var lrProbe provider.ConnectionProbe
	if cfg.LunaRoute.Enabled() {
		lrProbe, err = adapterprobe.NewProbeFactory(adapterprobe.Deps{HTTPClient: httpClient, Timeout: probeHTTPTimeout, MaxResponseBytes: 1 << 20}).
			ProbeFor(provider.ResolvedBinding{Provider: provider.CanonicalLunaRoute})
		if err != nil {
			return nil, fmt.Errorf("arbitron-runner: lunaroute probe: %w", err)
		}
	}
	discoverIn := sources.DiscoverInput{ClaudeBinary: cfg.ClaudeBinary, Runner: claudeRunner, LunaRoute: cfg.LunaRoute, LunaRouteProbe: lrProbe, Rules: rules, Confirmations: runnerprobe.NewConfirmationExecutor(factory)}
	specs, harness := sources.Discover(ctx, discoverIn)
	inv := sources.New(specs, harness, time.Now)
	inv.EnableClaudeVerification(sources.NewClaudeModelVerifier(runnerprobe.NewConfirmationExecutor(factory), cfg.ScratchRoot, logger), discoverIn)
	exec, err := buildExecutorFor(cfg, factory, inv.CapabilitiesFor)
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
	// RR-07: advertise the root only when it verifies; "" => hub marks chat_only.
	wsRoot, wsReason := config.VerifyWorkspaceRoot(cfg.WorkspaceRoot)
	if wsReason != "" {
		logger.Warn("workspace root not advertised; this runner is chat-only", "reason", wsReason)
	}
	return runneragent.New(runneragent.Config{
		OS: runtime.GOOS, Arch: runtime.GOARCH, ProtocolVersion: runnerproto.ProtocolVersion, RunnerVersion: version,
		HarnessVersions: inv.HarnessVersions(), WorkspaceRoot: wsRoot, ScratchRoot: cfg.ScratchRoot,
		Capabilities: []string{runneragent.OpChat, runneragent.OpStream},
		Heartbeat:    cfg.Heartbeat, ProbeInterval: cfg.ProbeInterval,
	}, runneragent.Deps{
		Conn: conn, Exec: exec, Sources: inv, Models: inv, Telemetry: telemetry.NewForRoot(cfg.WorkspaceRoot),
		Spool: sp, Identity: identity.New(cfg.MachineKeyPath()),
		Clock: runneragent.SystemClock{}, Rand: rand.Float64, Logger: logger,
	})
}
