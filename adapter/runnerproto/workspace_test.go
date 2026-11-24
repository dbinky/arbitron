// adapter/runnerproto/workspace_test.go
package runnerproto_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspacePayloads_RoundTripInsideDispatch(t *testing.T) {
	d := runnerproto.Dispatch{
		Op: "process",
		Process: &runnerproto.WireProcess{
			Kind: "script", Command: "/bin/sh", Args: []string{"./run.sh", "x"}, Stdin: []byte(`{"a":1}`),
			NodeEnv: map[string]string{"MODE": "fast"}, Passthrough: []string{"GITHUB_TOKEN"}, TimeoutSecs: 30,
			WorkDir: "/srv/ws/instances/1", BundleHash: strings.Repeat("a", 64), BundleEntrypoint: "run.sh", BundleToken: "tok-b",
		},
		Workspace: &runnerproto.WireWorkspace{RunID: "11111111-1111-1111-1111-111111111111", WorkDir: "/srv/ws/instances/1",
			HomeGeneration: 3, CheckpointVersion: 7, ManifestHash: "mh", PullToken: "tok-pull", PushToken: "tok-push", PartBytes: 64 << 20},
		LeaseUntil: time.Unix(1700000000, 0).UTC(),
	}
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"workspace":{"runId":"11111111-1111-1111-1111-111111111111"`)
	assert.Contains(t, string(raw), `"bundleHash":"`)
	var back runnerproto.Dispatch
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, d.Process, back.Process)
	assert.Equal(t, d.Workspace, back.Workspace)
}

// No wire payload may carry server credentials or endpoints.
func TestWorkspacePayloads_HaveNoSecretFields(t *testing.T) {
	forbidden := []string{"RevealKey", "BaseURL", "ConnectionRef", "Extras", "APIKey", "Key", "Secret", "Password"}
	for _, v := range []any{runnerproto.WireAgent{}, runnerproto.WireProcess{}, runnerproto.WireWorkspace{}} {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			for _, f := range forbidden {
				assert.NotEqual(t, f, typ.Field(i).Name, "%s must not carry %s", typ.Name(), f)
			}
		}
	}
}
