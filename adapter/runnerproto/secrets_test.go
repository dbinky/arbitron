// adapter/runnerproto/secrets_test.go
package runnerproto

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var forbiddenField = regexp.MustCompile(`(?i)(revealkey|baseurl|apikey|secret|password|connectionref|extras|authorization|header|^env$|^environ|^key$|^token$)`)

// walkOwnStructs visits every struct type of THIS package reachable from root.
func walkOwnStructs(t *testing.T, root reflect.Type, visit func(path string, f reflect.StructField)) {
	t.Helper()
	pkg := reflect.TypeOf(Dispatch{}).PkgPath()
	seen := map[reflect.Type]bool{}
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() == reflect.Func || rt.Kind() == reflect.Interface || rt.Kind() == reflect.Chan {
			t.Errorf("%s has kind %s; dispatch frames must be pure data", path, rt.Kind())
			return
		}
		if rt.Kind() != reflect.Struct || rt.PkgPath() != pkg || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			visit(path+"."+f.Name, f)
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(root, root.Name())
}

// The canonical {{workspace}} path is identical on every runner (design D2),
// so only these exact string fields may name a work dir on the wire. Any other
// work-dir-like name (ServerWorkDir, LocalWorkDir, ...) is forbidden.
var (
	workDirField      = regexp.MustCompile(`(?i)workdir`)
	canonicalWorkDirs = map[string]bool{"WorkDir": true, "OriginWorkDir": true}
)

func TestDispatch_HasNoServerCredentialFields(t *testing.T) {
	walkOwnStructs(t, reflect.TypeOf(Dispatch{}), func(path string, f reflect.StructField) {
		assert.False(t, forbiddenField.MatchString(f.Name), "%s must not exist on the wire dispatch", path)
		if workDirField.MatchString(f.Name) {
			assert.True(t, canonicalWorkDirs[f.Name], "%s: only the canonical WorkDir/OriginWorkDir may travel (D2)", path)
			assert.Equal(t, reflect.String, f.Type.Kind(), "%s must be the canonical path string", path)
		}
	})
}

// Any new WireChat field must be added here deliberately (and reviewed for secrets).
func TestWireChat_JSONKeysAreExactlyTheAllowlist(t *testing.T) {
	c := WireChat{Model: "m", SystemPrompt: "s", Prompt: "p", MaxTokens: 1, Temperature: 0.5,
		OutputSchema: json.RawMessage(`{"type":"object"}`), SessionID: "sid", RequireExecutedModel: true,
		Stream: true, CredentialKind: "lunaroute", Backend: "codex", ContextWindow: "1m", Effort: "high", Tools: []string{"Read"}}
	b, err := json.Marshal(Dispatch{Op: "chat", Chat: &c, LeaseUntil: time.Unix(0, 0)})
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &top))
	var chat map[string]any
	require.NoError(t, json.Unmarshal(top["chat"], &chat))
	keys := make([]string, 0, len(chat))
	for k := range chat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"backend", "contextWindow", "credentialKind", "effort", "maxTokens", "model",
		"outputSchema", "prompt", "requireExecutedModel", "sessionId", "stream", "systemPrompt", "temperature", "tools"}, keys)
	assert.ElementsMatch(t, []string{"op", "chat", "leaseUntil"}, mapKeys(top))
}

func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
