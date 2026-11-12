package lunaroute_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentEvidenceClaudeTitleAdoptionReaderBoundary(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	project := "/private/mission"
	dir := filepath.Join(home, ".claude/projects", claudeProjectKeyUnsafe.ReplaceAllString(project, "-"))
	require.NoError(t, os.MkdirAll(dir, 0700))
	// Same effective path, but it cannot be a canonical actual launch root.
	unclean := home + "/../" + filepath.Base(home)
	done := make(chan error, 1)
	go func() {
		_, err := readClaudeTitleAdoption(&claudeIsolation{home: unclean, project: project, session: "ses_factory"}, time.Now().Add(-time.Second), time.Now())
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("noncanonical root must refuse without unbounded ancestor traversal")
	}
}

func TestAgentEvidenceClaudeTitleAdoptionTranscript(t *testing.T) {
	project := "/private/mission"
	conversation := `{"type":"user","sessionId":"ses_factory","cwd":"/private/mission","message":{"role":"user","content":"private prompt"}}`
	title := `{"type":"ai-title","sessionId":"ses_factory","aiTitle":"Private fixture title"}`
	proof, err := collectClaudeTitleAdoption([]byte(conversation+"\n"+title+"\n"), "ses_factory", project)
	require.NoError(t, err)
	assert.Equal(t, "ses_factory", proof.session)
	assert.Equal(t, "Private fixture title", proof.title)
	raw, err := json.Marshal(proof)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(raw), "private comparison material must not serialize")
	// The title writer is asynchronous; persisted order alone is not its trigger.
	_, err = collectClaudeTitleAdoption([]byte(title+"\n"+conversation+"\n"), "ses_factory", project)
	require.NoError(t, err)

	for name, lines := range map[string][]string{
		"missing":                   {conversation},
		"no-conversation":           {title},
		"duplicate":                 {conversation, title, title},
		"foreign-session":           {conversation, strings.Replace(title, "ses_factory", "ses_other", 1)},
		"foreign-conversation":      {strings.Replace(conversation, "ses_factory", "ses_other", 1), title},
		"foreign-workspace":         {strings.Replace(conversation, project, "/foreign/mission", 1), title},
		"sidechain":                 {strings.Replace(conversation, `"cwd":`, `"isSidechain":true,"cwd":`, 1), title},
		"custom-rename":             {conversation, `{"type":"custom-title","sessionId":"ses_factory","customTitle":"Private fixture title"}`, title},
		"sdk-event-is-not-ai-title": {conversation, `{"type":"system","subtype":"session_title_changed","session_id":"ses_factory","title":"Private fixture title"}`},
		"sidechain-title":           {conversation, strings.Replace(title, `"aiTitle":`, `"isSidechain":true,"aiTitle":`, 1)},
		"secret-title":              {conversation, strings.Replace(title, "Private fixture title", captureFixtureKey, 1)},
		"empty-title":               {conversation, strings.Replace(title, "Private fixture title", "", 1)},
		"unsafe-title":              {conversation, strings.Replace(title, "Private fixture title", `two\nlines`, 1)},
		"zero-width-title":          {conversation, strings.Replace(title, "Private fixture title", `private\u200btitle`, 1)},
		"long-title":                {conversation, strings.Replace(title, "Private fixture title", strings.Repeat("x", 201), 1)},
		"non-string-title":          {conversation, strings.Replace(title, `"Private fixture title"`, `null`, 1)},
		"duplicate-key":             {conversation, strings.Replace(title, `"aiTitle":`, `"aiTitle":"hidden","aiTitle":`, 1)},
		"oversized":                 {conversation, strings.Repeat(" ", captureBodyLimit+1), title},
		"malformed":                 {conversation, "{", title},
		"trailing-json":             {conversation, title + `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			proof, err := collectClaudeTitleAdoption([]byte(strings.Join(lines, "\n")), "ses_factory", project)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), captureFixtureKey)
			assert.Empty(t, proof.title)
		})
	}
}
