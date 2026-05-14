package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewServeCmd(t *testing.T) {
	cmd := newServeCmd()
	assert.Equal(t, "serve", cmd.Use)
}
