package pi

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This protocol fixture exercises the real runner and reads the actual private
// definition before performing a workspace command. It is not LR-08 live proof.
func TestLunaRoutePi_SubprocessDefinitionToWorkspace(t *testing.T) {
	tmp := isolatePiTemp(t)
	ws := t.TempDir()
	bin, err := os.Executable()
	require.NoError(t, err)
	launcher := filepath.Join(t.TempDir(), "pi")
	script := "#!/bin/sh\nexport ARBITRON_PI_FIXTURE=1\nexport ARBITRON_PI_WORKSPACE=" + strconv.Quote(ws) + "\nexec " + strconv.Quote(bin) + " -test.run='^TestLunaRoutePi_SubprocessChild$' -- \"$@\"\n"
	require.NoError(t, os.WriteFile(launcher, []byte(script), 0700))
	p := New(Config{Binding: lrPiBinding(), BinaryPath: launcher, Runner: claudecli.NewExecRunner(), CapabilityRules: lrRules(), MaxResponseBytes: 8192})
	m := piMission()
	m.WorkDir = ws
	res, err := p.RunAgent(context.Background(), m)
	require.NoError(t, err)
	assert.Equal(t, "done", res.Summary)
	require.Equal(t, []string{"tool_call", "assistant"}, []string{res.Steps[0].Kind, res.Steps[1].Kind})
	require.Equal(t, "glm-5.3", res.ModelEvidence[0].Model)
	proof, err := os.ReadFile(filepath.Join(ws, "proof.txt"))
	require.NoError(t, err)
	assert.Equal(t, "PI-LUNAROUTE-OK", string(proof))
	assert.Empty(t, piLeftovers(t, tmp))
}
func TestLunaRoutePi_SubprocessChild(t *testing.T) {
	if os.Getenv("ARBITRON_PI_FIXTURE") != "1" {
		return
	}
	fail := func() { os.Exit(41) }
	args := os.Args[slices.Index(os.Args, "--")+1:]
	dir := os.Getenv(PiAgentDirVar)
	if dir == "" {
		fail()
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0700 {
		fail()
	}
	if reflect.DeepEqual(args, []string{"--version"}) {
		if os.Getenv(LunaRouteKeyVar) != "" {
			fail()
		}
		io.WriteString(os.Stdout, minPiVersion+"\n")
		os.Exit(0)
	}
	if !reflect.DeepEqual(args, []string{"--print", "--mode", "json", "--no-session", "--model", "lunaroute/glm-5.3"}) || os.Getenv(LunaRouteKeyVar) != lrPiKey || os.Getenv("OPENAI_API_KEY") != "" {
		fail()
	}
	cwd, err := os.Getwd()
	expectedCwd, pathErr := filepath.EvalSymlinks(os.Getenv("ARBITRON_PI_WORKSPACE"))
	if err != nil || pathErr != nil || cwd != expectedCwd {
		fail()
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err != nil || string(input) != "create a file" {
		fail()
	}
	path := filepath.Join(dir, "models.json")
	st, err = os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		fail()
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), lrPiKey) {
		fail()
	}
	var def piModelsFile
	if json.Unmarshal(raw, &def) != nil {
		fail()
	}
	want := piModelsFile{Providers: map[string]piCustomProvider{"lunaroute": {BaseURL: "https://gw.lunaroute.com/v1", API: "openai-completions", APIKey: "$LUNAROUTE_API_KEY", AuthHeader: true, Models: []piCustomModel{{ID: "glm-5.3", Name: "glm-5.3", Reasoning: true, Input: []string{"text"}, ContextWindow: 202752, MaxTokens: 32768}}}}}
	if !reflect.DeepEqual(def, want) {
		fail()
	}
	// A real command is the final sink, not a canned summary or precreated file.
	lines, wait, err := claudecli.NewExecRunner().Run(context.Background(), claudecli.RunSpec{Bin: "/bin/sh", Args: []string{"-c", "printf PI-LUNAROUTE-OK > proof.txt"}, Dir: cwd, Env: []string{"PATH=/usr/bin:/bin"}, MaxResponseBytes: 1024})
	if err != nil {
		fail()
	}
	for range lines {
	}
	if wait() != nil {
		fail()
	}
	io.WriteString(os.Stdout, `{"type":"message_end","message":{"role":"assistant","provider":"lunaroute","model":"glm-5.3","stopReason":"toolUse","content":[{"type":"toolCall","name":"bash"}]}}`+"\n"+lrAssistant+"\n")
	os.Exit(0)
}
