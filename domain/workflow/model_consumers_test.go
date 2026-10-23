package workflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// declaredNodeTypes parses every NodeType constant declared in this package, so
// a node type added without a classification below fails this test.
func declaredNodeTypes(t *testing.T) map[NodeType]string {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", nil, 0)
	require.NoError(t, err)
	out := map[NodeType]string{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					typ, ok := vs.Type.(*ast.Ident)
					if !ok || typ.Name != "NodeType" {
						continue
					}
					for i, name := range vs.Names {
						lit, ok := vs.Values[i].(*ast.BasicLit)
						require.True(t, ok, name.Name)
						v, err := strconv.Unquote(lit.Value)
						require.NoError(t, err)
						out[NodeType(v)] = name.Name
					}
				}
			}
		}
	}
	require.NotEmpty(t, out)
	return out
}

// Every node type is classified: a model consumer with its mode and required
// kind, or explicitly not one. Pin capture, the path inventory and profile-kind
// validation all range over ModelConsumers, so this table is their contract.
func TestModelConsumersClassifiesEveryNodeType(t *testing.T) {
	type want struct {
		consumer bool
		mode     ConsumerMode
		kind     string
		source   ConsumerSource
	}
	table := map[NodeType]want{
		NodeTypeLLMCall:          {true, ConsumerModeBatch, ConsumerKindChat, ConsumerSourceNode},
		NodeTypeAgentCall:        {true, ConsumerModeBatch, ConsumerKindAgent, ConsumerSourceNode},
		NodeTypeInteractiveLLM:   {true, ConsumerModeInteractive, ConsumerKindChat, ConsumerSourceNode},
		NodeTypeInteractiveAgent: {true, ConsumerModeInteractive, ConsumerKindAgent, ConsumerSourceNode},
		NodeTypeSpawnWorkflow:    {true, ConsumerModeBatch, ConsumerKindChat, ConsumerSourceNode},
		NodeTypeParallelGroup:    {true, ConsumerModeInteractive, ConsumerKindAgent, ConsumerSourceGroupResolver},
		NodeTypeScript:           {},
		NodeTypeToolCall:         {},
		NodeTypeConditional:      {},
		NodeTypeTransform:        {},
		NodeTypeTimerDelay:       {},
		NodeTypeWebhookWait:      {},
	}
	for nt, name := range declaredNodeTypes(t) {
		w, ok := table[nt]
		if !assert.True(t, ok, "%s (%q) is not classified in ModelConsumers' contract table", name, nt) {
			continue
		}
		cfg := NodeConfig{ModelProfile: "p", Backend: "pi"}
		if nt == NodeTypeParallelGroup {
			cfg = NodeConfig{Group: &GroupConfig{ResolverProfile: "p"}}
		}
		got := ModelConsumers(&Workflow{Nodes: []Node{{ID: "n", Type: nt, Config: cfg}}})
		if !w.consumer {
			assert.Empty(t, got, "%s must not consume a model profile", name)
			continue
		}
		require.Len(t, got, 1, name)
		assert.Equal(t, "p", got[0].Alias, name)
		assert.Equal(t, w.mode, got[0].Mode, name)
		assert.Equal(t, w.kind, got[0].Kind, name)
		assert.Equal(t, w.source, got[0].Source, name)
		if nt == NodeTypeAgentCall {
			assert.Equal(t, "pi", got[0].NodeBackend, "agent_call keeps its node backend override")
		} else {
			assert.Empty(t, got[0].NodeBackend, "%s routes on the profile backend", name)
		}
	}
}

// A parallel_group without a resolver consumes nothing.
func TestModelConsumersGroupWithoutResolver(t *testing.T) {
	assert.Empty(t, ModelConsumers(&Workflow{Nodes: []Node{
		{ID: "g", Type: NodeTypeParallelGroup},
		{ID: "h", Type: NodeTypeParallelGroup, Config: NodeConfig{Group: &GroupConfig{GatherMode: GroupGatherResults}}},
	}}))
	assert.Nil(t, ModelConsumers(nil))
}
