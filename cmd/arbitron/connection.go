package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	configadapter "github.com/arbitron/arbitron/adapter/config"
	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/provider"
)

// newConnectionCmd is the parent for connection operator subcommands. CRUD
// (create/update/delete) stays MCP-only; this group exists only to host
// `validate`, mirroring the newServeCmd/newMCPCmd registration pattern.
func newConnectionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connection",
		Short: "Operator commands for provider connections",
		Long: "Operator commands for provider connections. Creation/update/deletion are " +
			"MCP-only; this group hosts on-demand validation.",
	}
	cmd.AddCommand(newConnectionValidateCmd())
	return cmd
}

// newConnectionValidateCmd builds `arbitron connection validate <name>`.
func newConnectionValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <name>",
		Short: "Validate a connection's credentials + reachability and list its models",
		Long: "Resolve the named connection, authenticate against its provider, confirm reachability, " +
			"and list available models where the provider supports it. Never prints the secret. " +
			"Exit code 0 on success, non-zero on any validation failure.",
		Args: cobra.ExactArgs(1),
		// SilenceUsage so a validation failure prints the (secret-free) error, not
		// a usage dump that could echo args.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			deps, cleanup, err := buildValidateDeps(ctx)
			if err != nil {
				return err
			}
			defer cleanup()
			return runConnectionValidate(ctx, deps, args[0], cmd.OutOrStdout())
		},
	}
}

// validateDeps are the injected dependencies of runConnectionValidate, so the
// command core is unit-testable with fakes (no config/DB/network).
type validateDeps struct {
	store    provider.ConnectionStore
	selector provider.ProbeSelector
}

// buildValidateDeps assembles the production stack: config + master key ->
// SQLite ConnectionRepo (decryption path) -> ProbeFactory. It mirrors
// initDomain steps 1 + 8a–8d so the CLI shares one resolution/decryption path
// with the server. The returned cleanup closes the DB.
func buildValidateDeps(ctx context.Context) (validateDeps, func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return validateDeps{}, nil, fmt.Errorf("determining home directory: %w", err)
	}
	cfg, err := configadapter.Load(filepath.Join(home, ".arbitron", "config.yaml"))
	if err != nil {
		return validateDeps{}, nil, fmt.Errorf("loading config: %w", err)
	}
	db, err := adaptsqlite.Open(cfg.Database.Path)
	if err != nil {
		return validateDeps{}, nil, fmt.Errorf("opening database: %w", err)
	}
	if err := adaptsqlite.RunMigrations(db); err != nil {
		db.Close()
		return validateDeps{}, nil, fmt.Errorf("running migrations: %w", err)
	}
	// Master key: absent is tolerated only when zero connections exist.
	logger := slog.Default()
	masterKey, err := adaptcrypto.LoadMasterKey(logger)
	if err != nil && !errors.Is(err, provider.ErrMasterKeyMissing) {
		db.Close()
		return validateDeps{}, nil, fmt.Errorf("loading master key: %w", err)
	}
	haveKey := masterKey != nil
	var connCrypto provider.Crypto
	if haveKey {
		connCrypto, err = adaptcrypto.NewAESGCM(masterKey)
		if err != nil {
			db.Close()
			return validateDeps{}, nil, fmt.Errorf("building crypto: %w", err)
		}
	}
	store := adaptsqlite.NewConnectionRepo(db, connCrypto)
	count, err := store.Count(ctx)
	if err != nil {
		db.Close()
		return validateDeps{}, nil, fmt.Errorf("counting connections: %w", err)
	}
	if err := adaptcrypto.RequireMasterKeyIfConnections(haveKey, count); err != nil {
		db.Close()
		return validateDeps{}, nil, err
	}
	selector := adaptprobe.NewProbeFactory(adaptprobe.Deps{
		Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		MaxResponseBytes: cfg.LLM.MaxResponseBytes,
		ClaudeBinaryPath: cfg.LLM.BinaryPath,
	})
	return validateDeps{store: store, selector: selector}, func() { db.Close() }, nil
}

// bindingFromConnection turns a stored connection into a ResolvedBinding for
// probing, attaching the lazy RevealKey closure that shares the store's sole
// decryption path. Model is empty (probing lists/reaches; it never calls a model).
func bindingFromConnection(store provider.ConnectionStore, name string, c provider.Connection) provider.ResolvedBinding {
	return provider.ProbeBindingFromConnection(store, name, c)
}

// runConnectionValidate is the testable core: resolve name -> binding -> probe,
// then format the outcome. It returns a non-nil error on any validation failure
// (which cobra turns into a non-zero exit via os.Exit(1) in main()).
func runConnectionValidate(ctx context.Context, deps validateDeps, name string, out io.Writer) error {
	conn, err := deps.store.Get(ctx, name)
	if err != nil {
		if errors.Is(err, provider.ErrConnectionNotFound) {
			return fmt.Errorf("no such connection %q", name)
		}
		return fmt.Errorf("looking up connection %q: %w", name, err)
	}
	binding := bindingFromConnection(deps.store, name, conn)
	probe, err := deps.selector.ProbeFor(binding)
	if err != nil {
		return fmt.Errorf("validate %q: %w", name, err)
	}
	res, err := probe.Probe(ctx, binding)
	if err != nil {
		// The probe's classified error (credentials/unreachable/region) is
		// already secret-free; surface it as the command failure.
		return fmt.Errorf("validate %q: %w", name, err)
	}
	fmt.Fprint(out, formatProbeResult(name, conn.Provider, res))
	return nil
}

// formatProbeResult renders a successful probe outcome. It never includes the
// secret (it has no access to it). Three shapes: models listed, listing
// unsupported (reachability only), or reachable with an empty list.
func formatProbeResult(name, providerName string, res provider.ProbeResult) string {
	switch {
	case res.ModelsUnsupported:
		return fmt.Sprintf("connection %q (%s): reachable. model listing not supported for this provider.\n", name, providerName)
	case len(res.Models) == 0:
		return fmt.Sprintf("connection %q (%s): reachable, no models.\n", name, providerName)
	default:
		s := fmt.Sprintf("connection %q (%s): reachable. %d model(s):\n", name, providerName, len(res.Models))
		for _, m := range res.Models {
			s += "  - " + m + "\n"
		}
		return s
	}
}
