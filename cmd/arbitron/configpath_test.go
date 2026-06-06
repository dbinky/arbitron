package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveConfigPath(t *testing.T) {
	home := "/home/u"
	def := filepath.Join(home, ".arbitron", "config.yaml")

	// Flag wins over env and default.
	assert.Equal(t, "/flag/c.yaml", resolveConfigPath("/flag/c.yaml", "/env/c.yaml", home))
	// Env wins over default when no flag.
	assert.Equal(t, "/env/c.yaml", resolveConfigPath("", "/env/c.yaml", home))
	// Default when neither set.
	assert.Equal(t, def, resolveConfigPath("", "", home))
}
