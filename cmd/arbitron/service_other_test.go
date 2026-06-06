//go:build !windows

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On non-Windows platforms the binary is never launched by the Windows SCM, so
// runUnderServiceManager must report handled == false, err == nil and must NOT
// invoke the serve function — the caller (runServe) drives the interactive serve
// path itself.
func TestRunUnderServiceManager_NoOpOnNonWindows(t *testing.T) {
	called := 0
	serve := func(context.Context) error {
		called++
		return nil
	}

	handled, err := runUnderServiceManager(context.Background(), serve)

	require.NoError(t, err, "non-Windows no-op must never error")
	assert.False(t, handled, "non-Windows must report not handled so the caller serves interactively")
	assert.Equal(t, 0, called, "the no-op path must never invoke serve; the caller drives serve")
}

// Robustness: a nil serve function must not be dereferenced on the no-op path
// (it is never called), so this must not panic.
func TestRunUnderServiceManager_NoOpDoesNotTouchServe(t *testing.T) {
	assert.NotPanics(t, func() {
		handled, err := runUnderServiceManager(context.Background(), nil)
		assert.False(t, handled)
		assert.NoError(t, err)
	})
}
