package engine_test

import (
	"context"
	"errors"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestConversation_FollowupAppendsPlacement(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			ir, nr, llm, bus, q, id := conversationTestSetup(t)
			h := buildConversationHandler(ir, nr, llm, bus, q)
			before, err := nr.GetByInstanceAndNode(convoCtx(), id, "interact")
			require.NoError(t, err)
			oldSource := "original-source"
			before.SourceID = &oldSource
			before.PlacementHistory = []instance.PlacementRecord{{JobID: "first", RunnerID: "builtin", SourceID: oldSource}}
			require.NoError(t, nr.Update(convoCtx(), before))
			llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
				dispatchFact(t, ctx, "", "followup", "completed", 1)
				if fail {
					return nil, errors.New("turn failed")
				}
				return cannedStream([]string{"reply"}, 1, 1)(ctx, req)
			}
			err = h.HandleMessage(convoCtx(), id, "new text")
			if fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			got, err := nr.GetByInstanceAndNode(convoCtx(), id, "interact")
			require.NoError(t, err)
			require.Len(t, got.PlacementHistory, 2)
			require.Equal(t, "first", got.PlacementHistory[0].JobID)
			require.Nil(t, got.SourceID)
			if fail {
				require.Equal(t, before.LLMConversation, got.LLMConversation)
			}
		})
	}
}

func TestGroupResolution_FirstTurnCarriesPlacement(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			f := newBridgeFixture(t)
			f.agent.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
				dispatchFact(t, ctx, "source", "first", "completed", 1)
				if fail {
					return nil, errors.New("turn failed")
				}
				return cannedStreamWithSession([]string{"reply"}, "s")(ctx, req)
			}
			_, err := f.start(1)
			if fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			rows, err := f.nodeExecs.ListByInstance(context.Background(), f.parentID)
			require.NoError(t, err)
			var placed []instance.NodeExecution
			for _, row := range rows {
				if len(row.PlacementHistory) > 0 {
					placed = append(placed, row)
				}
			}
			require.Len(t, placed, 1)
			require.Equal(t, "source", *placed[0].SourceID)
			if fail {
				require.Empty(t, placed[0].LLMConversation)
				require.Equal(t, instance.NodeStatusFailed, placed[0].Status)
			}
		})
	}
}

func TestGroupResolution_FollowupPlacement(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			f := newGroupConvoFixture(t, true, true)
			before := f.exec(f.sessionRow)
			f.agent.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
				dispatchFact(t, ctx, "source", "followup", "completed", 1)
				if fail {
					return nil, errors.New("turn failed")
				}
				return cannedStream([]string{"reply"}, 1, 1)(ctx, req)
			}
			err := f.handler.HandleMessage(convoCtx(), f.parentID, "new text")
			if fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			got := f.exec(f.sessionRow)
			require.Len(t, got.PlacementHistory, 1)
			if fail {
				require.Equal(t, before.LLMConversation, got.LLMConversation)
			}
		})
	}
}
