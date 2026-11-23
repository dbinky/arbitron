package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type consumerPinLookup struct {
	id    uuid.UUID
	set   provider.PinSet
	err   error
	calls int
}

func (l *consumerPinLookup) Lookup(_ context.Context, id uuid.UUID) (provider.PinSet, error) {
	l.calls++
	if id != l.id {
		return provider.PinSet{}, errors.New("unexpected run ID")
	}
	return l.set, l.err
}

func (*consumerPinLookup) Release(uuid.UUID) {}

// The sink records the model actually passed to Chat/RunAgent, rather than
// accepting an unused binding in a selector fake.
type consumerModelSink struct{ models []string }

func (s *consumerModelSink) Chat(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	s.models = append(s.models, req.Model)
	return &engine.ChatResponse{Model: req.Model, Output: `{"topic":"ready"}`}, nil
}

func (*consumerModelSink) StreamChat(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	return nil, errors.New("unexpected stream call")
}

func (s *consumerModelSink) RunAgent(_ context.Context, mission engine.AgentMission) (*engine.AgentResult, error) {
	s.models = append(s.models, mission.Model)
	return &engine.AgentResult{Model: mission.Model, Summary: "done"}, nil
}

func TestModelPinsBatchConsumersRequireRunContext(t *testing.T) {
	const pinned, live = "claude-opus-4-1", "claude-opus-4-5"
	lookupErr := errors.New("pin repository unavailable")
	for _, consumer := range []string{"llm", "agent", "child inputs"} {
		for _, state := range []string{"missing context", "zero ID", "snapshot", "legacy", "nil pins", "empty pins", "missing alias", "corrupt version", "lookup fault"} {
			t.Run(consumer+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				templateDir := filepath.Join(root, "shared", "wf")
				require.NoError(t, os.MkdirAll(templateDir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(templateDir, "p.md"), []byte("hello"), 0o600))
				kind := provider.ProfileKindChat
				if consumer == "agent" {
					kind = provider.ProfileKindAgent
				}
				cache := providersadapter.NewProfileCacheFromProfiles(provider.Profile{Alias: "worker", Kind: kind, Model: live})
				liveResolver := providersadapter.NewDynamicProfileResolver(cache, nil)
				id := uuid.New()
				ctx := engine.ContextWithInstanceID(context.Background(), id)
				lookup := &consumerPinLookup{id: id, set: provider.PinSet{Version: 1, Models: map[string]string{"worker": pinned}}}
				wantErr, wantModel := provider.ErrPinUnavailable, ""
				switch state {
				case "missing context":
					ctx = context.Background()
				case "zero ID":
					ctx = engine.ContextWithInstanceID(context.Background(), uuid.Nil)
				case "snapshot":
					wantErr, wantModel = nil, pinned
				case "legacy":
					lookup.set = provider.PinSet{Version: 0}
					wantErr, wantModel = nil, live
				case "nil pins":
					lookup.set.Models = nil
				case "empty pins":
					lookup.set.Models = map[string]string{}
				case "missing alias":
					lookup.set.Models = map[string]string{"other": pinned}
				case "corrupt version":
					lookup.set.Version = 9
				case "lookup fault":
					lookup.err, wantErr = lookupErr, lookupErr
				}
				pins := provider.NewPinningResolver(liveResolver, lookup, engine.InstanceIDFromContext)
				sink := &consumerModelSink{}
				factory := providersadapter.NewFactory(providersadapter.Deps{ClaudeChat: sink, ClaudeAgent: sink})
				consumers := newRunModelConsumers(filesystem.NewFileReader(root), factory, factory, pins, workflow.BackendClaudeCode)
				node := workflow.Node{ID: "node", Type: workflow.NodeTypeLLMCall,
					Config: workflow.NodeConfig{ModelProfile: "worker", TemplateFile: "p.md"}}
				var err error
				switch consumer {
				case "llm":
					_, err = consumers.llm.Execute(ctx, node, variable.NewStore(), "wf")
				case "agent":
					node.Type = workflow.NodeTypeAgentCall
					_, err = consumers.agent.Execute(ctx, node, variable.NewStore(), "wf")
				case "child inputs":
					var inputs map[string]any
					inputs, err = consumers.childInputs.Prepare(ctx, engine.ChildInputRequest{
						NodeID: "spawn", ChildWorkflow: "child", ProfileAlias: "worker",
						Inputs: []workflow.InputField{{Name: "topic", Type: "string", Required: true}},
					})
					if wantErr != nil {
						assert.Nil(t, inputs, "a refused preparation must return no partial child input")
					} else {
						assert.Equal(t, map[string]any{"topic": "ready"}, inputs)
					}
				}
				if wantErr != nil {
					assert.ErrorIs(t, err, wantErr)
					assert.Empty(t, sink.models, "a lost run context or pin fault must not invoke a provider")
				} else {
					require.NoError(t, err)
					assert.Equal(t, []string{wantModel}, sink.models)
				}
				if state == "missing context" || state == "zero ID" {
					assert.Zero(t, lookup.calls, "invalid context must fail before repository I/O")
				} else {
					assert.Equal(t, 1, lookup.calls)
				}
			})
		}
	}
}
