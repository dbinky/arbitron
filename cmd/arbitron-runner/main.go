// Command arbitron-runner connects a Mac to an Arbitron server and executes
// the chat steps the server places on it.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/arbitron/arbitron/adapter/runneragent/config"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{Use: "arbitron-runner", Short: "Arbitron remote runner", SilenceUsage: true}
	root.AddCommand(newRunCmd(), newVersionCmd())
	return root
}

func newRunCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Connect to the Arbitron server and execute placed steps",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cfgPath == "" {
				p, err := config.DefaultConfigPath()
				if err != nil {
					return err
				}
				cfgPath = p
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			agent, err := buildAgent(ctx, cfg, logger)
			if err != nil {
				return err
			}
			logger.Info("arbitron-runner starting", "version", version, "server", cfg.ServerURL)
			if err := agent.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config.yaml (default ~/.arbitron-runner/config.yaml)")
	return cmd
}
