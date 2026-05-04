package variable

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveSimplePath(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")

	result := Resolve("{{name}}", store)
	assert.Equal(t, "alice", result)
}

func TestResolveNodeOutput(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("validate", map[string]any{"score": 85})

	result := Resolve("{{validate.score}}", store)
	assert.Equal(t, "85", result)
}

func TestResolveDeepPath(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("analyze", map[string]any{
		"result": map[string]any{
			"items": []any{
				map[string]any{"name": "first"},
				map[string]any{"name": "second"},
			},
		},
	})

	result := Resolve("{{analyze.result.items.0.name}}", store)
	assert.Equal(t, "first", result)
}

func TestResolveUnresolved(t *testing.T) {
	store := NewStore()

	result := Resolve("{{missing}}", store)
	assert.Equal(t, "{{missing}}", result)
}

func TestResolveMultipleSubstitutions(t *testing.T) {
	store := NewStore()
	store.SetInput("first", "alice")
	store.SetInput("last", "smith")

	result := Resolve("{{first}} {{last}}", store)
	assert.Equal(t, "alice smith", result)
}

func TestResolveMidText(t *testing.T) {
	store := NewStore()
	store.SetInput("score", 85)

	result := Resolve("Score: {{score}}/100", store)
	assert.Equal(t, "Score: 85/100", result)
}

func TestResolveNoMarkers(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")

	result := Resolve("hello world", store)
	assert.Equal(t, "hello world", result)
}

func TestResolveEmptyStore(t *testing.T) {
	store := NewStore()

	result := Resolve("{{a}} and {{b}}", store)
	assert.Equal(t, "{{a}} and {{b}}", result)
}

func TestResolveBooleanTrue(t *testing.T) {
	store := NewStore()
	store.SetInput("flag", true)

	result := Resolve("{{flag}}", store)
	assert.Equal(t, "true", result)
}

func TestResolveBooleanFalse(t *testing.T) {
	store := NewStore()
	store.SetInput("flag", false)

	result := Resolve("{{flag}}", store)
	assert.Equal(t, "false", result)
}

func TestResolveObject(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("node", map[string]any{"key": "value"})

	result := Resolve("{{node}}", store)
	assert.Equal(t, `{"key":"value"}`, result)
}

func TestResolveArray(t *testing.T) {
	store := NewStore()
	store.SetInput("items", []any{1, 2, 3})

	result := Resolve("{{items}}", store)
	assert.Equal(t, "[1,2,3]", result)
}

func TestResolveNull(t *testing.T) {
	store := NewStore()
	store.SetInput("empty", nil)

	result := Resolve("{{empty}}", store)
	assert.Equal(t, "null", result)
}

func TestResolveAdjacent(t *testing.T) {
	store := NewStore()
	store.SetInput("a", "hello")
	store.SetInput("b", "world")

	result := Resolve("{{a}}{{b}}", store)
	assert.Equal(t, "helloworld", result)
}

func TestResolveTripleBraces(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")

	// {{{name}}} → the outer { is literal, then {{name}} resolves, then } is literal
	result := Resolve("{{{name}}}", store)
	assert.Equal(t, "{alice}", result)
}

func TestResolveEmptyString(t *testing.T) {
	store := NewStore()

	result := Resolve("", store)
	assert.Equal(t, "", result)
}

func TestResolveIntegerNoDecimals(t *testing.T) {
	store := NewStore()
	store.SetInput("count", 42)

	result := Resolve("{{count}}", store)
	assert.Equal(t, "42", result)
}

func TestResolveFloat(t *testing.T) {
	store := NewStore()
	store.SetInput("rate", 3.14)

	result := Resolve("{{rate}}", store)
	assert.Equal(t, "3.14", result)
}

func TestResolveFloatWholeNumber(t *testing.T) {
	store := NewStore()
	store.SetInput("val", float64(7))

	result := Resolve("{{val}}", store)
	assert.Equal(t, "7", result)
}

func TestResolveStringValue(t *testing.T) {
	store := NewStore()
	store.SetInput("msg", "hello world")

	// Strings are raw, no quotes.
	result := Resolve("{{msg}}", store)
	assert.Equal(t, "hello world", result)
}

func TestResolveMixedResolvedAndUnresolved(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")

	result := Resolve("Hello {{name}}, your id is {{id}}", store)
	assert.Equal(t, "Hello alice, your id is {{id}}", result)
}

func TestResolveUnclosedBrace(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")

	// Unclosed {{ — no matching }}, so the whole string is returned as-is.
	result := Resolve("{{unclosed", store)
	assert.Equal(t, "{{unclosed", result)
}

func TestResolveDanglingClose(t *testing.T) {
	store := NewStore()

	// Dangling }} with no matching {{ — returned verbatim.
	result := Resolve("text}}", store)
	assert.Equal(t, "text}}", result)
}

func TestResolveInt64Value(t *testing.T) {
	store := NewStore()
	store.SetInput("count", int64(1234567890123))

	result := Resolve("{{count}}", store)
	assert.Equal(t, "1234567890123", result)
}

func TestResolveNestedObjectInText(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("resp", map[string]any{
		"data": map[string]any{"items": []any{"a", "b"}},
	})

	result := Resolve("Result: {{resp.data}}", store)
	assert.Equal(t, `Result: {"items":["a","b"]}`, result)
}
