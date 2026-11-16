package openrouter

import (
	"context"
	"net/http"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	streamNoDone   = "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n"
	streamNoChoice = "data: {\"model\":\"m\"}\n\ndata: [DONE]\n"
	streamErrFrame = "data: {\"error\":{\"message\":\"boom\"}}\n"
	streamOK       = "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n"
)

func drainStream(t *testing.T, p *Provider) (tokens string, done bool, err error) {
	t.Helper()
	ch, serr := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, serr)
	for c := range ch {
		tokens += c.Token
		done = done || c.Done
		if c.Error != nil {
			err = c.Error
		}
	}
	return
}

func classifier(calls *int) func(int, string, error, string, ...string) error {
	return func(int, string, error, string, ...string) error { *calls++; return errHooked }
}

// StrictStream selects the stream protocol and ClassifyError only maps errors;
// each is changed with the other held fixed.
func TestStrictStream_IndependentOfClassifier(t *testing.T) {
	for _, tc := range []struct {
		name         string
		strict, hook bool
		body         string
		wantErr      bool
	}{
		{"strict without classifier rejects missing DONE", true, false, streamNoDone, true},
		{"strict without classifier rejects choice-less frame", true, false, streamNoChoice, true},
		{"strict without classifier recognises error frame", true, false, streamErrFrame, true},
		{"strict without classifier accepts complete stream", true, false, streamOK, false},
		{"ordinary with classifier accepts missing DONE", false, true, streamNoDone, false},
		{"ordinary with classifier accepts choice-less frame", false, true, streamNoChoice, false},
		{"ordinary with classifier ignores error frames", false, true, streamErrFrame, false},
		{"ordinary with classifier accepts complete stream", false, true, streamOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			cfg := Config{
				Binding:      staticBinding("https://gw.example/v1", "m"),
				HTTPClient:   &http.Client{Transport: &fakeTransport{resp: sseResponse(200, tc.body)}},
				StrictStream: tc.strict,
			}
			if tc.hook {
				cfg.ClassifyError = classifier(&calls)
			}
			_, done, err := drainStream(t, New(cfg))
			if tc.wantErr {
				require.Error(t, err)
				assert.False(t, done)
			} else {
				require.NoError(t, err)
				assert.True(t, done)
			}
			if !tc.hook {
				assert.Zero(t, calls)
			}
		})
	}
}

// Executed-model identity is enforced for every call when configured, with or
// without a pin, a request flag or an error classifier.
func TestAlwaysRequireExecutedModel(t *testing.T) {
	for _, hook := range []bool{false, true} {
		for _, tc := range []struct {
			name, reported string
			always, ok     bool
		}{
			{"match", "m", true, true},
			{"foreign", "gpt-foreign", true, false},
			{"missing", "", true, false},
			{"off keeps ordinary behaviour", "gpt-foreign", false, true},
		} {
			chat := `{"model":"` + tc.reported + `","choices":[{"message":{"content":"x"}}]}`
			stream := "data: {\"model\":\"" + tc.reported + "\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n"
			build := func(body string, sse bool) *Provider {
				resp := jsonResponse(200, body)
				if sse {
					resp = sseResponse(200, body)
				}
				var n int
				cfg := Config{Binding: staticBinding("https://gw.example/v1", "m"), HTTPClient: &http.Client{Transport: &fakeTransport{resp: resp}}, AlwaysRequireExecutedModel: tc.always}
				if hook {
					cfg.ClassifyError = classifier(&n)
				}
				return New(cfg)
			}
			t.Run(tc.name, func(t *testing.T) {
				_, err := build(chat, false).Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
				_, _, serr := drainStream(t, build(stream, true))
				if tc.ok {
					require.NoError(t, err)
					require.NoError(t, serr)
				} else {
					require.ErrorIs(t, err, errModelExecutionEvidence)
					require.ErrorIs(t, serr, errModelExecutionEvidence)
				}
			})
		}
	}
}
