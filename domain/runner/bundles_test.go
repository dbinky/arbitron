// domain/runner/bundles_test.go
package runner_test

import (
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
)

func TestValidBundleHash(t *testing.T) {
	assert.True(t, runner.ValidBundleHash(strings.Repeat("ab", 32)))
	assert.False(t, runner.ValidBundleHash(strings.Repeat("AB", 32)), "upper-case is not canonical")
	assert.False(t, runner.ValidBundleHash("../../etc/passwd"))
	assert.False(t, runner.ValidBundleHash(strings.Repeat("a", 63)))
	assert.False(t, runner.ValidBundleHash(""))
}
