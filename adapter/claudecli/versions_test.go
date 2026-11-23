package claudecli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckVersion(t *testing.T) {
	cases := []struct {
		v      string
		ok     bool
		reason string
	}{
		{"2.1.328", true, ""},
		{"2.1.284", true, ""},
		{"3.0.0", true, ""},
		{"2.1.283", false, "claude 2.1.283 is below 2.1.284 (assistant error kind and quotaLimits)"},
		{"1.9.999", false, "claude 1.9.999 is below 2.1.284 (assistant error kind and quotaLimits)"},
		{"", false, "claude version unknown"},
		{"banana", false, `claude version "banana" is not parseable`},
	}
	for _, tc := range cases {
		ok, reason := CheckVersion(tc.v)
		assert.Equal(t, tc.ok, ok, tc.v)
		assert.Equal(t, tc.reason, reason, tc.v)
	}
}

func TestSupportedVersions_FloorIsHighestMin(t *testing.T) {
	require.NotEmpty(t, SupportedVersions)
	highest := ""
	for _, f := range SupportedVersions {
		require.NotEmpty(t, f.Feature)
		hv, ok := parseSemver(f.Min)
		require.True(t, ok, f.Min)
		cur, _ := parseSemver(highest)
		if highest == "" || cmpSemver(hv, cur) > 0 {
			highest = f.Min
		}
	}
	ok, _ := CheckVersion(highest)
	assert.True(t, ok, "the highest floor itself is supported")
	for _, f := range SupportedVersions {
		if f.Min != highest {
			ok, _ := CheckVersion(f.Min)
			assert.False(t, ok, "a lower floor alone does not satisfy the table: %s", f.Min)
		}
	}
}

func TestProbeVersion(t *testing.T) {
	fr := &fakeRunner{lines: []string{"2.1.328 (Claude Code)"}}
	v, err := ProbeVersion(context.Background(), fr, "/opt/claude")
	require.NoError(t, err)
	assert.Equal(t, "2.1.328", v)
	assert.Equal(t, "/opt/claude", fr.gotSpec.Bin)
	assert.Equal(t, []string{"--version"}, fr.gotSpec.Args)

	_, err = ProbeVersion(context.Background(), &fakeRunner{lines: []string{"no digits here"}}, "claude")
	assert.ErrorIs(t, err, ErrVersionUnparseable)

	_, err = ProbeVersion(context.Background(), &fakeRunner{startErr: errors.New("exec: not found")}, "claude")
	require.Error(t, err)
	assert.Equal(t, "claudecli: version probe: harness process failed", err.Error())
}

func TestResolveBinaryFound(t *testing.T) {
	p, found := ResolveBinary("/explicit/claude")
	assert.Equal(t, "/explicit/claude", p)
	assert.True(t, found)

	_, found = resolveBinaryFound("", func(string) (string, error) { return "", errors.New("nope") }, func(string) (bool, error) { return false, nil })
	assert.False(t, found)

	p, found = resolveBinaryFound("", func(string) (string, error) { return "/usr/local/bin/claude", nil }, func(string) (bool, error) { return false, nil })
	assert.Equal(t, "/usr/local/bin/claude", p)
	assert.True(t, found)
}
