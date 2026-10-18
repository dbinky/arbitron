package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// yamlQuote renders s as a single-quoted YAML scalar. e2e tests splice temp-dir
// paths into hand-written config.yaml; inside DOUBLE quotes a Windows path such
// as C:\Users\RUNNER~1\... is read as escape sequences (\U is a unicode escape),
// so every such test failed to boot on windows CI with
// "yaml: line 7: did not find expected hexdecimal number". Single-quoted YAML
// has no escapes except a doubled quote.
func yamlQuote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += "''"
			continue
		}
		out += string(r)
	}
	return out + "'"
}

func yamlRoundTrip(t *testing.T, s string) string {
	t.Helper()
	var got struct {
		P string `yaml:"p"`
	}
	require.NoError(t, yaml.Unmarshal([]byte("p: "+yamlQuote(s)+"\n"), &got))
	return got.P
}

// HAPPY — a Windows temp path survives exactly.
func TestYAMLQuote_WindowsPathRoundTrips(t *testing.T) {
	p := `C:\Users\RUNNER~1\AppData\Local\Temp\TestSpawnE2E123\001\arbitron.db`
	assert.Equal(t, p, yamlRoundTrip(t, p))
}

// SUCCESS — a POSIX path is unchanged.
func TestYAMLQuote_PosixPathRoundTrips(t *testing.T) {
	p := "/var/folders/5x/T/TestSpawnE2E123/001/arbitron.db"
	assert.Equal(t, p, yamlRoundTrip(t, p))
}

// FAILURE — the old double-quoted splice really does break on that path, which
// is why the helper exists.
func TestYAMLQuote_DoubleQuotedWindowsPathIsBroken(t *testing.T) {
	var got struct {
		P string `yaml:"p"`
	}
	err := yaml.Unmarshal([]byte("p: \"C:\\Users\\RUNNER~1\\AppData\"\n"), &got)
	require.Error(t, err)
}

// EDGE — embedded single quotes and an empty string round-trip.
func TestYAMLQuote_QuotesAndEmpty(t *testing.T) {
	assert.Equal(t, "it's a 'path'", yamlRoundTrip(t, "it's a 'path'"))
	assert.Equal(t, "", yamlRoundTrip(t, ""))
}
