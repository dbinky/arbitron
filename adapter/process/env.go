package process

import (
	"fmt"
	"os"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/variable"
)

// buildEnv constructs the environment for a process node: the base allowlist,
// plus operator-approved passthrough vars (by name, from the engine env), plus
// the node's literal env (with {{var}} substitution). A node-env key colliding
// with a base or passthrough name is rejected to prevent secret/base spoofing.
func buildEnv(passthrough []string, nodeEnv map[string]string, vars *variable.VariableStore) ([]string, error) {
	env := procenv.BaseEnv()

	passSet := make(map[string]bool, len(passthrough))
	for _, name := range passthrough {
		name = strings.TrimSpace(name)
		if name == "" || procenv.IsBaseName(name) {
			continue
		}
		passSet[name] = true
		if val, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+val)
		}
	}

	for k, v := range nodeEnv {
		if procenv.IsBaseName(k) || passSet[k] {
			return nil, fmt.Errorf("node env %q collides with a base or passthrough variable name", k)
		}
		env = append(env, k+"="+variable.Resolve(v, vars))
	}
	return env, nil
}
