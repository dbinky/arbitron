package lunaroute_test

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

const (
	openCodeIsolationSource   = "docs/evidence/lunaroute-provider/sources/lr06-isolation.md"
	openCodeForeignCredential = "conflicting-opencode-credential"
)

// Paths and protected bytes never cross the observation boundary. Runtime
// databases/logs are separate from the config tree and auth file being checked.
type openCodeIsolation struct {
	home, project string
	before        backendHomeSnapshot
}

type openCodeIsolationProof struct {
	Source          string `json:"source"`
	PrivateHome     bool   `json:"private_home"`
	ConfigUnchanged bool   `json:"config_unchanged"`
	AuthUnchanged   bool   `json:"auth_unchanged"`
}

func seedOpenCodeIsolation(home, project string) (*openCodeIsolation, error) {
	if os.Mkdir(home, 0700) != nil {
		return nil, errAgentEvidence
	}
	home, homeErr := filepath.EvalSymlinks(home)
	project, projectErr := filepath.EvalSymlinks(project)
	if homeErr != nil || projectErr != nil {
		return nil, errAgentEvidence
	}
	seeds := map[string]string{
		// Config.ensureGitignore otherwise creates this on first read, even
		// under --pure. Preseed its versioned bytes; still snapshot every file.
		filepath.Join(home, ".config/opencode/.gitignore"):     "node_modules\npackage.json\npackage-lock.json\nbun.lock\n.gitignore",
		filepath.Join(home, ".config/opencode/opencode.json"):  `{"$schema":"https://opencode.ai/config.json","model":"foreign/user","small_model":"foreign/helper","enabled_providers":["foreign"],"provider":{"lunaroute":{"npm":"@ai-sdk/anthropic","options":{"baseURL":"https://foreign.example/v1","apiKey":"` + openCodeForeignCredential + `"},"models":{"glm-5.3":{"name":"wrong","tool_call":false,"limit":{"context":1,"output":2}}}}}}`,
		filepath.Join(project, "opencode.json"):                `{"$schema":"https://opencode.ai/config.json","model":"foreign/project","small_model":"foreign/project-helper"}`,
		filepath.Join(home, ".local/share/opencode/auth.json"): `{"lunaroute":{"type":"api","key":"` + openCodeForeignCredential + `"},"openai":{"type":"api","key":"` + openCodeForeignCredential + `"}}`,
	}
	for path, body := range seeds {
		if os.MkdirAll(filepath.Dir(path), 0700) != nil {
			return nil, errAgentEvidence
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, errAgentEvidence
		}
		_, writeErr := io.WriteString(f, body)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			return nil, errAgentEvidence
		}
	}
	isolation := &openCodeIsolation{home: home, project: project}
	before, err := isolation.snapshot()
	if err != nil {
		return nil, errAgentEvidence
	}
	isolation.before = before
	return isolation, nil
}

func (s *openCodeIsolation) snapshot() (backendHomeSnapshot, error) {
	if s == nil {
		return nil, errAgentEvidence
	}
	return snapshotIsolationPaths(s.home, []isolationPath{
		{s.home, ".config/opencode"}, {s.home, ".local/share/opencode/auth.json"}, {s.project, "opencode.json"},
	})
}

type isolationPath struct{ root, name string }

func snapshotIsolationPaths(home string, targets []isolationPath) (backendHomeSnapshot, error) {
	out := backendHomeSnapshot{}
	total := 0
	for _, target := range targets {
		// WalkDir does not follow links itself, but its ancestors must not be
		// replaced by links either. Check each component before opening files.
		path := target.root
		for _, component := range append([]string{""}, strings.Split(target.name, string(os.PathSeparator))...) {
			path = filepath.Join(path, component)
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() && info.Mode().Perm()&0500 != 0500 {
				return nil, errAgentEvidence
			}
			if info.IsDir() {
				out[path] = backendHomeEntry{Mode: info.Mode()}
			}
		}
		err := filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return errAgentEvidence
			}
			info, err := d.Info()
			if err != nil || !info.IsDir() && !info.Mode().IsRegular() {
				return errAgentEvidence
			}
			rel, err := filepath.Rel(target.root, path)
			if err != nil {
				return errAgentEvidence
			}
			entry := backendHomeEntry{Mode: info.Mode()}
			total += len(rel) + 1
			if total > backendTreeLimit || info.Size() > int64(backendTreeLimit-total) && info.Mode().IsRegular() {
				return errAgentEvidence
			}
			if info.Mode().IsRegular() {
				f, err := os.Open(path)
				if err != nil {
					return errAgentEvidence
				}
				entry.Data, err = io.ReadAll(io.LimitReader(f, int64(backendTreeLimit-total)+1))
				closeErr := f.Close()
				if err != nil || closeErr != nil || len(entry.Data) > backendTreeLimit-total {
					return errAgentEvidence
				}
				total += len(entry.Data)
			}
			out[target.root+string(os.PathSeparator)+rel] = entry
			return nil
		})
		if err != nil {
			return nil, errAgentEvidence
		}
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errAgentEvidence
	}
	return out, nil
}

func (s *openCodeIsolation) proof() (openCodeIsolationProof, error) {
	after, err := s.snapshot()
	if err != nil || len(s.before) == 0 || !reflect.DeepEqual(s.before, after) {
		return openCodeIsolationProof{}, errAgentEvidence
	}
	return openCodeIsolationProof{Source: openCodeIsolationSource, PrivateHome: true, ConfigUnchanged: true, AuthUnchanged: true}, nil
}

// Bind protected roots to the actual adapter-emitted env/cwd. An XDG/auth/config
// override would change the source locations, so it cannot use these snapshots.
func (s *openCodeIsolation) validateLaunch(spec claudecli.RunSpec) error {
	homes := 0
	for _, entry := range spec.Env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			return errAgentEvidence
		}
		if name == "HOME" {
			homes++
			home, err := filepath.EvalSymlinks(value)
			if err != nil || home != s.home {
				return errAgentEvidence
			}
		}
		if strings.HasPrefix(name, "XDG_") || name == "OPENCODE_AUTH_CONTENT" || name == "OPENCODE_CONFIG" || name == "OPENCODE_CONFIG_DIR" || name == "OPENCODE_TEST_HOME" {
			return errAgentEvidence
		}
	}
	dir, err := filepath.EvalSymlinks(spec.Dir)
	if err != nil {
		return errAgentEvidence
	}
	rel, err := filepath.Rel(s.project, dir)
	if err != nil || homes != 1 || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errAgentEvidence
	}
	_, err = s.proof()
	return err
}
