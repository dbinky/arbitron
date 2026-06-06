package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// resolveConfigPath picks the config-file path by precedence:
// --config flag > ARBITRON_CONFIG env > ~/.arbitron/config.yaml default.
func resolveConfigPath(flagVal, envVal, homeDir string) string {
	if flagVal != "" {
		return flagVal
	}
	if envVal != "" {
		return envVal
	}
	return filepath.Join(homeDir, ".arbitron", "config.yaml")
}

// configPathFromCmd resolves the effective config path for a cobra command,
// reading the --config flag and the ARBITRON_CONFIG env var.
//
// ARBITRON_CONFIG is read here via raw os.Getenv rather than folded into the
// koanf loader's ARBITRON_SERVER_* env-binding precedence by chicken-and-egg
// necessity: that env-binding lives inside the loader, but the config PATH is
// the input that tells the loader which file to read, so it must resolve before
// the loader runs — you cannot bootstrap the file location from a mechanism that
// only exists once the file is loaded.
func configPathFromCmd(cmd *cobra.Command) (string, error) {
	flagVal, _ := cmd.Flags().GetString("config")
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	return resolveConfigPath(flagVal, os.Getenv("ARBITRON_CONFIG"), home), nil
}
