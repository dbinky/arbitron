package opencode

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// The image's OpenCode must satisfy the probe isolation floor, or automatic
// discovery refuses every OpenCode probe in the shipped container.
func TestDockerfileOpencodePinMeetsProbeFloor(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	require.NoError(t, err)
	m := regexp.MustCompile(`npm install -g opencode-ai@(\d+\.\d+\.\d+)\b`).FindSubmatch(body)
	require.NotNil(t, m, "the Dockerfile pins an exact opencode-ai version")
	pinned := string(m[1])
	require.GreaterOrEqual(t, compareVersion(mustParseVersion(pinned), mustParseVersion(minOpencodeProbeVersion)), 0,
		"Dockerfile opencode-ai@%s is below minOpencodeProbeVersion %s", pinned, minOpencodeProbeVersion)
	require.GreaterOrEqual(t, compareVersion(mustParseVersion(pinned), mustParseVersion(minOpencodeVersion)), 0)
}
