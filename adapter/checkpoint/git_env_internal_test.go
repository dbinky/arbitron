// adapter/checkpoint/git_env_internal_test.go
package checkpoint

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGitEnv_DisablesLazyFetchAndRedirection(t *testing.T) {
	env := gitEnv([]string{"PATH=/bin", "GIT_DIR=/user/repo/.git", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/elsewhere",
		"GIT_OBJECT_DIRECTORY=/x", "GIT_INDEX_FILE=/y", "GIT_CONFIG_PARAMETERS='protocol.allow'='always'"})
	assert.Contains(t, env, "PATH=/bin")
	assert.Contains(t, env, "GIT_NO_LAZY_FETCH=1")
	assert.Contains(t, env, "GIT_TERMINAL_PROMPT=0")
	assert.Contains(t, env, "GIT_CONFIG_NOSYSTEM=1")
	for _, kv := range env {
		for _, banned := range []string{"GIT_DIR=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_OBJECT_DIRECTORY=", "GIT_INDEX_FILE=", "GIT_CONFIG_PARAMETERS="} {
			assert.False(t, strings.HasPrefix(kv, banned), "inherited %s must not redirect git", banned)
		}
	}
	assert.Equal(t, []string{"-c", "protocol.allow=never", "-c", "credential.helper=", "-c", "core.fsmonitor=false", "--git-dir", "/g", "rev-list"},
		gitAt("/g", "rev-list"))
}
