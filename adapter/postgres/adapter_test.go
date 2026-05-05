package postgres

import "testing"

func TestInterfaceSatisfaction(t *testing.T) {
	// Compile-time assertions are in assert.go. This test is a marker so the
	// test suite has an explicit pass/fail for interface compliance.
	t.Log("interface satisfaction verified by assert.go")
}
