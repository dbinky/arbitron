package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initDomain supplies persisted pins, actual node routing, credentials/config,
// factory and process runner. Only the opencode executable is substituted.
func TestUsabilityOpenCodeExactModelRoot(t *testing.T) {
	for _, tc := range []struct {
		name, model, session, message string
		providerID                    string
		exportFails                   bool
		before, after                 string
	}{
		{name: "exact", model: "gpt-5-sol", session: "ses_run", message: "msg_answer"},
		{name: "fallback", model: "fallback", session: "ses_run", message: "msg_answer"},
		{name: "missing model", session: "ses_run", message: "msg_answer"},
		// Paired row 5: the right model exported under another provider is not X.
		{name: "wrong provider", model: "gpt-5-sol", session: "ses_run", message: "msg_answer", providerID: "openrouter"},
		{name: "wrong session", model: "gpt-5-sol", session: "ses_other", message: "msg_answer"},
		{name: "wrong turn", model: "gpt-5-sol", session: "ses_run", message: "msg_old"},
		{name: "export fails", model: "gpt-5-sol", session: "ses_run", message: "msg_answer", exportFails: true},
		{name: "truncated event before exact", model: "gpt-5-sol", session: "ses_run", message: "msg_answer", before: `"type":"error","message":"opencode-root-secret"}`},
		{name: "array after exact", model: "gpt-5-sol", session: "ses_run", message: "msg_answer", after: `[{"type":"error"}]`},
		{name: "scalar after exact", model: "gpt-5-sol", session: "ses_run", message: "msg_answer", after: `null`},
		{name: "text after exact", model: "gpt-5-sol", session: "ses_run", message: "msg_answer", after: `opencode-root-secret`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.providerID == "" {
				tc.providerID = "openai"
			}
			dir := t.TempDir()
			argvLog := filepath.Join(dir, "argv.log")
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then echo 'opencode 1.19.2'; exit 0; fi
printf '%%s\n' "$*" >> %q
[ "$OPENAI_API_KEY" = 'opencode-root-secret' ] || exit 43
case "$PWD" in *workspaces/instances/*) ;; *) exit 44 ;; esac
case "$OPENCODE_CONFIG_CONTENT" in *'"baseURL":"https://api.openai.com"'*) ;; *) exit 45 ;; esac
case "$OPENCODE_CONFIG_CONTENT" in *'"permission":{"*":"deny","read":"allow"}'*) ;; *) exit 46 ;; esac
if [ "$1" = 'run' ]; then
 [ "$2" = 'hello' ] || exit 47
 case " $* " in *' -m openai/gpt-5-sol --dangerously-skip-permissions --format json '*) ;; *) exit 48 ;; esac
 printf '%%s\n' '%s'
 printf '%%s\n' '{"type":"text","sessionID":"ses_run","part":{"id":"prt_text","messageID":"msg_answer","sessionID":"ses_run","type":"text","text":"verified output"}}'
 printf '%%s\n' '%s'
 exit 0
fi
[ "$1" = 'export' ] && [ "$2" = 'ses_run' ] && [ "$#" = 2 ] || exit 49
[ %t = false ] || exit 50
printf '%%s\n' '{"info":{"id":%q},"messages":[{"info":{"id":%q,"sessionID":%q,"role":"assistant","providerID":%q,"modelID":%q,"time":{"created":1,"completed":2}},"parts":[]}]}'
`, argvLog, tc.before, tc.after, tc.exportFails, tc.session, tc.message, tc.session, tc.providerID, tc.model)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ds := bootSinkRoot(t, "", func(workspace string) {
				writeWorkflow(t, workspace, "usability-opencode", `{"name":"usability-opencode","nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"backend":"opencode","model_profile":"agent","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "usability-opencode")
			})
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "opencode-api", Provider: "openai", BaseURL: "https://api.openai.com"}, "opencode-root-secret"))
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "agent", Kind: provider.ProfileKindAgent, Backend: "claude-code", ConnectionRef: "opencode-api", Model: "openai/gpt-5-sol", AllowedTools: []string{"Read"}})
			require.NoError(t, err)
			id, _, err := ds.eng.Trigger(ctx, "usability-opencode", nil, "normal")
			require.NoError(t, err)
			saved := waitTerminal(t, ds, id)
			trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
			require.NoError(t, err)
			require.Len(t, trace, 1)
			assert.Equal(t, map[string]string{"agent": "openai/gpt-5-sol"}, saved.ModelPins)
			if tc.name == "exact" {
				require.Equal(t, instance.StateCompleted, saved.State, "trace: %+v", trace)
				assert.Equal(t, instance.NodeStatusSucceeded, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Output), "verified output")
				require.NotNil(t, trace[0].LLMModel)
				assert.Equal(t, "openai/gpt-5-sol", *trace[0].LLMModel)
			} else {
				require.Equal(t, instance.StateFailed, saved.State, "unverified output reached durable sink: %+v", trace)
				assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Error), "model execution evidence")
				assert.Empty(t, trace[0].Output, "unverified output must not be persisted")
				assert.Empty(t, trace[0].LLMConversation, "unverified assistant trace must not be accepted")
			}
			calls := readArgvLog(t, argvLog)
			if tc.before != "" || tc.after != "" {
				require.Len(t, calls, 1, "malformed run evidence must reject before export")
			} else {
				require.Len(t, calls, 2, "matching output must be verified by a separate correlated export")
				assert.Equal(t, "export ses_run", calls[1])
			}
			assert.NotContains(t, fmt.Sprint(trace), "opencode-root-secret")
		})
	}
}
