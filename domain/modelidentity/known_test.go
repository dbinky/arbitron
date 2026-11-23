package modelidentity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKnownFamilies(t *testing.T) {
	assert.Equal(t, []Family{"anthropic", "openai", "deepseek", "zhipuai", "moonshotai"}, KnownFamilies())
	a := KnownFamilies()
	a[0] = "mutated"
	assert.Equal(t, Family("anthropic"), KnownFamilies()[0], "returns a fresh slice")
}
