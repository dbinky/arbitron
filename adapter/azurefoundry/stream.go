package azurefoundry

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/arbitron/arbitron/domain/engine"
)

// sseDoneMarker is the OpenAI/Azure terminal SSE payload.
const sseDoneMarker = "[DONE]"

// streamSSE reads an SSE body line by line, emitting a Token chunk per content
// delta and a single terminal Done chunk (carrying accumulated usage) when the
// [DONE] marker or EOF is reached. A read or decode error is delivered as a
// chunk Error before returning. The bound on total bytes read protects against
// an unbounded upstream stream.
func streamSSE(ctx context.Context, body io.Reader, maxBytes int64, out chan<- engine.ChatChunk) {
	reader := bufio.NewReader(io.LimitReader(body, maxBytes+1))

	var tokensIn, tokensOut int
	var read int64

	flushDone := func() {
		sendChunk(ctx, out, engine.ChatChunk{Done: true, TokensIn: tokensIn, TokensOut: tokensOut})
	}

	for {
		select {
		case <-ctx.Done():
			sendChunk(ctx, out, engine.ChatChunk{Error: ctx.Err()})
			return
		default:
		}

		line, err := reader.ReadString('\n')
		read += int64(len(line))
		if read > maxBytes {
			sendChunk(ctx, out, engine.ChatChunk{Error: errors.New("azurefoundry: stream exceeds max bytes")})
			return
		}

		payload, isData := parseSSELine(line)
		if isData {
			if payload == sseDoneMarker {
				flushDone()
				return
			}
			if payload != "" {
				var frame streamFrame
				if derr := json.Unmarshal([]byte(payload), &frame); derr != nil {
					sendChunk(ctx, out, engine.ChatChunk{Error: fmt.Errorf("azurefoundry: decode stream frame: %w", derr)})
					return
				}
				for _, ch := range frame.Choices {
					if ch.Delta.Content != "" {
						sendChunk(ctx, out, engine.ChatChunk{Token: ch.Delta.Content})
					}
				}
				if frame.Usage != nil {
					tokensIn = frame.Usage.PromptTokens
					tokensOut = frame.Usage.CompletionTokens
				}
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				flushDone()
				return
			}
			sendChunk(ctx, out, engine.ChatChunk{Error: fmt.Errorf("azurefoundry: read stream: %w", err)})
			return
		}
	}
}
