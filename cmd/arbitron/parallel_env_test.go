package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// t.Setenv refuses a parallel test even when the write would change nothing.
// These variants skip a variable that already holds the wanted state, so a
// parent test can pin a fixture's process-wide environment once and run its
// independent subtests in parallel; a test without such a parent still sets
// and restores exactly as t.Setenv does.

func setenvForTest(t *testing.T, key, value string) {
	t.Helper()
	if cur, ok := os.LookupEnv(key); ok && cur == value {
		return
	}
	t.Setenv(key, value)
}

func unsetenvForTest(t *testing.T, key string) {
	t.Helper()
	if _, ok := os.LookupEnv(key); !ok {
		return
	}
	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}

// pinLayoutE2EEnv clears every environment override the layout e2e config
// file must win against (see newLayoutE2EEnv).
func pinLayoutE2EEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"ARBITRON_LAYOUT_MAXATTEMPTS", "ARBITRON_LAYOUT_BROWSERPATH", "ARBITRON_LAYOUT_ARTIFACTDIR", "ARBITRON_LAYOUT_BROWSERNOSANDBOX", "ARBITRON_SAMPLES_SEED", "ARBITRON_SAMPLES_SOURCEDIR"} {
		unsetenvForTest(t, key)
	}
}

// pinSinkRootEnv sets the master key every bootSinkRoot stack boots with.
func pinSinkRootEnv(t *testing.T) {
	t.Helper()
	setenvForTest(t, "ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5d}, 32)))
}
