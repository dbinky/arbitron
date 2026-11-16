package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LunaRoute response identity is enforced for every call. A legacy (version-0)
// run resolves an ordinary live binding: Pinned is false and the chat request
// never sets RequireExecutedModel. Neither may switch the check off.
func TestLunaRouteChat_UnpinnedRequiresExecutedModel(t *testing.T) {
	for _, tc := range []struct {
		name, model string // reported model; "" means the field is absent
		ok          bool
	}{
		{"matching", "glm-5.3", true},
		{"foreign", "gpt-foreign", false},
		{"sibling", "glm-5.3-flash", false},
		{"missing", "", false},
	} {
		reply := func(stream bool) string {
			field := ""
			if tc.model != "" {
				field = fmt.Sprintf(`"model":%q,`, tc.model)
			}
			if stream {
				return "data: {" + field + "\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\ndata: [DONE]\n"
			}
			return "{" + field + "\"choices\":[{\"message\":{\"content\":\"pong\"}}]}"
		}
		t.Run("chat/"+tc.name, func(t *testing.T) {
			g := newChatGW()
			g.status, g.body = 200, reply(false)
			b := lrChatBinding("", lrChatKey, nil)
			require.False(t, b.Pinned)
			req := lrChatRequest()
			require.False(t, req.RequireExecutedModel)
			resp, err := chatProvider(t, g, b).Chat(context.Background(), req)
			if tc.ok {
				require.NoError(t, err)
				assert.Equal(t, "pong", resp.Output)
				return
			}
			require.ErrorContains(t, err, "execution evidence")
			assert.Nil(t, resp)
		})
		t.Run("stream/"+tc.name, func(t *testing.T) {
			g := newChatGW()
			g.expected["stream"] = true
			g.status, g.body = 200, reply(true)
			g.header = http.Header{"Content-Type": []string{"text/event-stream"}}
			b := lrChatBinding("", lrChatKey, nil)
			require.False(t, b.Pinned)
			ch, err := chatProvider(t, g, b).StreamChat(context.Background(), lrChatRequest())
			require.NoError(t, err)
			var chunkErr error
			var done bool
			var content string
			for c := range ch {
				content += c.Token
				if c.Error != nil {
					chunkErr = c.Error
				}
				done = done || c.Done
			}
			if tc.ok {
				require.NoError(t, chunkErr)
				assert.True(t, done)
				return
			}
			require.ErrorContains(t, chunkErr, "execution evidence")
			assert.False(t, done)
			assert.Empty(t, content, "no unverified token reaches the consumer")
		})
	}
}
