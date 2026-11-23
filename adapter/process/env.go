package process

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/variable"
)

// passthroughSet returns the effective passthrough names: trimmed, non-empty,
// and never a base-allowlist name (a base name cannot be shadowed).
func passthroughSet(passthrough []string) map[string]bool {
	set := make(map[string]bool, len(passthrough))
	for _, name := range passthrough {
		name = strings.TrimSpace(name)
		if name == "" || procenv.IsBaseName(name) {
			continue
		}
		set[name] = true
	}
	return set
}

// validateNodeEnvNames rejects a node-env key colliding with a base or
// passthrough name, preventing secret/base spoofing. It is pure on names, so
// executors run it on the server before dispatch and every runner runs it
// again in buildEnvFromResolved.
func validateNodeEnvNames(passthrough []string, nodeEnv map[string]string) error {
	passSet := passthroughSet(passthrough)
	keys := make([]string, 0, len(nodeEnv))
	for k := range nodeEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if procenv.IsBaseName(k) || passSet[k] {
			return fmt.Errorf("node env %q collides with a base or passthrough variable name", k)
		}
	}
	return nil
}

// resolveNodeEnv substitutes {{var}} templates in node env values. It runs on
// the server, where the variable store lives.
func resolveNodeEnv(nodeEnv map[string]string, vars *variable.VariableStore) map[string]string {
	if len(nodeEnv) == 0 {
		return nil
	}
	out := make(map[string]string, len(nodeEnv))
	for k, v := range nodeEnv {
		out[k] = variable.Resolve(v, vars)
	}
	return out
}

// buildEnvFromResolved constructs the environment on the EXECUTING machine:
// the base allowlist, plus passthrough values read from this process's env, plus
// the already-resolved node env. Collisions are rejected exactly as before.
func buildEnvFromResolved(passthrough []string, nodeEnv map[string]string) ([]string, error) {
	if err := validateNodeEnvNames(passthrough, nodeEnv); err != nil {
		return nil, err
	}
	env := procenv.BaseEnv()
	names := make([]string, 0)
	for name := range passthroughSet(passthrough) {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if val, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+val)
		}
	}
	keys := make([]string, 0, len(nodeEnv))
	for k := range nodeEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+nodeEnv[k])
	}
	return env, nil
}
