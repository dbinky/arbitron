// Package config reads the runner's config.yaml and its 0600 runner.env.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	koanffile "github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

const (
	// EnvFileName holds the runner's secrets next to config.yaml (mode 0600).
	EnvFileName = "runner.env"
	// MachineKeyFileName is the ed25519 machine key next to config.yaml.
	MachineKeyFileName = "machine_ed25519"

	defaultHeartbeat          = 5 * time.Second
	defaultProbeInterval      = 60 * time.Second
	defaultOutputBufferFrames = 4096
)

var allowedKeys = map[string]bool{
	"workspace_root": true, "scratch_root": true, "spool_dir": true,
	"heartbeat_seconds": true, "probe_interval_seconds": true, "output_buffer_frames": true,
	"claude_binary": true, "lunaroute.connection_ref": true, "lunaroute.families": true, "lunaroute.models": true,
}

var (
	familyRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	headerNameRE = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]+$`)
	forbiddenHdr = map[string]bool{
		"Authorization": true, "Host": true, "Connection": true, "Upgrade": true,
		"Sec-Websocket-Key": true, "Sec-Websocket-Version": true, "Sec-Websocket-Extensions": true, "Sec-Websocket-Protocol": true,
	}
)

// LunaRoute is this runner's own LunaRoute source. APIKey comes only from
// runner.env and never leaves the machine.
type LunaRoute struct {
	ConnectionRef string
	Families      []modelidentity.Family
	Models        []string
	APIKey        string
}

// Enabled reports whether the runner can advertise and serve LunaRoute.
func (l LunaRoute) Enabled() bool {
	return l.APIKey != "" && l.ConnectionRef != "" && len(l.Families) > 0
}

// Config is the validated runner configuration.
type Config struct {
	ConfigDir          string
	ServerURL          string
	RunnerKey          string
	ExtraHeaders       http.Header
	WorkspaceRoot      string
	ScratchRoot        string
	SpoolDir           string
	Heartbeat          time.Duration
	ProbeInterval      time.Duration
	OutputBufferFrames int
	ClaudeBinary       string
	CodexBinary        string
	LunaRoute          LunaRoute
}

// MachineKeyPath is the machine identity key location.
func (c Config) MachineKeyPath() string { return filepath.Join(c.ConfigDir, MachineKeyFileName) }

type fileConfig struct {
	WorkspaceRoot        string `koanf:"workspace_root"`
	ScratchRoot          string `koanf:"scratch_root"`
	SpoolDir             string `koanf:"spool_dir"`
	HeartbeatSeconds     int    `koanf:"heartbeat_seconds"`
	ProbeIntervalSeconds int    `koanf:"probe_interval_seconds"`
	OutputBufferFrames   int    `koanf:"output_buffer_frames"`
	ClaudeBinary         string `koanf:"claude_binary"`
	CodexBinary          string `koanf:"codex_binary"`
	LunaRoute            struct {
		ConnectionRef string   `koanf:"connection_ref"`
		Families      []string `koanf:"families"`
		Models        []string `koanf:"models"`
	} `koanf:"lunaroute"`
}

// DefaultConfigPath is ~/.arbitron-runner/config.yaml.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("runner config: home directory: %w", err)
	}
	return filepath.Join(home, ".arbitron-runner", "config.yaml"), nil
}

// Load reads configPath and the runner.env beside it.
func Load(configPath string) (Config, error) {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("runner config: %w", err)
	}
	k := koanf.New(".")
	if err := k.Load(koanffile.Provider(abs), yaml.Parser()); err != nil {
		return Config{}, fmt.Errorf("runner config %s: %w", abs, err)
	}
	for _, key := range k.Keys() {
		if !allowedKeys[key] {
			return Config{}, fmt.Errorf("runner config %s: unknown key %q", abs, key)
		}
	}
	var fc fileConfig
	if err := k.Unmarshal("", &fc); err != nil {
		return Config{}, fmt.Errorf("runner config %s: %w", abs, err)
	}

	dir := filepath.Dir(abs)
	cfg := Config{ConfigDir: dir, ClaudeBinary: strings.TrimSpace(fc.ClaudeBinary), CodexBinary: strings.TrimSpace(fc.CodexBinary)}
	if cfg.WorkspaceRoot, err = absPath("workspace_root", fc.WorkspaceRoot, ""); err != nil {
		return Config{}, err
	}
	if cfg.ScratchRoot, err = absPath("scratch_root", fc.ScratchRoot, filepath.Join(dir, "scratch")); err != nil {
		return Config{}, err
	}
	if cfg.SpoolDir, err = absPath("spool_dir", fc.SpoolDir, filepath.Join(dir, "spool")); err != nil {
		return Config{}, err
	}
	if cfg.Heartbeat, err = seconds(k, "heartbeat_seconds", fc.HeartbeatSeconds, defaultHeartbeat, 1, 60); err != nil {
		return Config{}, err
	}
	if cfg.ProbeInterval, err = seconds(k, "probe_interval_seconds", fc.ProbeIntervalSeconds, defaultProbeInterval, 5, 3600); err != nil {
		return Config{}, err
	}
	cfg.OutputBufferFrames = defaultOutputBufferFrames
	if k.Exists("output_buffer_frames") {
		if fc.OutputBufferFrames < 16 || fc.OutputBufferFrames > 1_000_000 {
			return Config{}, fmt.Errorf("runner config: output_buffer_frames must be 16..1000000, got %d", fc.OutputBufferFrames)
		}
		cfg.OutputBufferFrames = fc.OutputBufferFrames
	}

	env, err := readEnvFile(filepath.Join(dir, EnvFileName))
	if err != nil {
		return Config{}, err
	}
	if cfg.ServerURL, err = validateServerURL(env["ARBITRON_SERVER_URL"]); err != nil {
		return Config{}, err
	}
	key := env["ARBITRON_RUNNER_KEY"]
	if !strings.HasPrefix(key, "ark_") || len(key) <= len("ark_") {
		return Config{}, errors.New("runner config: ARBITRON_RUNNER_KEY in runner.env must be an ark_ runner key")
	}
	cfg.RunnerKey = key
	if cfg.ExtraHeaders, err = ParseExtraHeaders(env["ARBITRON_EXTRA_HEADERS"]); err != nil {
		return Config{}, fmt.Errorf("runner config: %w", err)
	}
	if cfg.LunaRoute, err = lunaRoute(fc, env["LUNAROUTE_API_KEY"]); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func absPath(name, val, def string) (string, error) {
	v := strings.TrimSpace(val)
	if v == "" {
		v = def
	}
	if v == "" {
		return "", fmt.Errorf("runner config: %s is required", name)
	}
	if !filepath.IsAbs(v) {
		return "", fmt.Errorf("runner config: %s must be an absolute path, got %q", name, v)
	}
	return filepath.Clean(v), nil
}

func seconds(k *koanf.Koanf, key string, v int, def time.Duration, lo, hi int) (time.Duration, error) {
	if !k.Exists(key) {
		return def, nil
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("runner config: %s must be %d..%d, got %d", key, lo, hi, v)
	}
	return time.Duration(v) * time.Second, nil
}

func lunaRoute(fc fileConfig, apiKey string) (LunaRoute, error) {
	lr := LunaRoute{ConnectionRef: strings.TrimSpace(fc.LunaRoute.ConnectionRef), Models: fc.LunaRoute.Models, APIKey: strings.TrimSpace(apiKey)}
	for _, f := range fc.LunaRoute.Families {
		f = strings.TrimSpace(f)
		if !familyRE.MatchString(f) || modelidentity.Family(f) == modelidentity.FamilyUnresolved {
			return LunaRoute{}, fmt.Errorf("runner config: lunaroute.families entry %q is not a model family", f)
		}
		lr.Families = append(lr.Families, modelidentity.Family(f))
	}
	if lr.APIKey != "" && !provider.ValidLunaRouteKey(lr.APIKey) {
		return LunaRoute{}, errors.New("runner config: LUNAROUTE_API_KEY in runner.env is not a LunaRoute key (lr_...)")
	}
	configured := lr.ConnectionRef != "" || len(lr.Families) > 0
	if configured && (lr.ConnectionRef == "" || len(lr.Families) == 0) {
		return LunaRoute{}, errors.New("runner config: lunaroute needs both connection_ref and families")
	}
	if configured && lr.APIKey == "" {
		return LunaRoute{}, errors.New("runner config: lunaroute is configured but LUNAROUTE_API_KEY is missing from runner.env")
	}
	return lr, nil
}

// readEnvFile parses KEY=VALUE lines. The file holds the runner key, so it
// must not be readable by group or others.
func readEnvFile(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("runner config: %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("runner config: %s has mode %04o; it holds the runner key and must be 0600 (chmod 600 %s)", path, perm, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("runner config: %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("runner config: %s line %d is not KEY=VALUE", path, n)
		}
		out[strings.TrimSpace(key)] = unquote(strings.TrimSpace(val))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("runner config: %s: %w", path, err)
	}
	return out, nil
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

func validateServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("runner config: ARBITRON_SERVER_URL is missing from runner.env")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("runner config: ARBITRON_SERVER_URL must be an absolute URL without credentials, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", errors.New("runner config: ARBITRON_SERVER_URL must use https (plain http is only allowed for a loopback server)")
		}
	default:
		return "", errors.New("runner config: ARBITRON_SERVER_URL must use https")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ParseExtraHeaders parses ARBITRON_EXTRA_HEADERS ("Name: value;Name2: value2").
// Headers that would override authentication or the WebSocket handshake are refused.
func ParseExtraHeaders(s string) (http.Header, error) {
	h := http.Header{}
	for i, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" || value == "" || !headerNameRE.MatchString(name) || strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("ARBITRON_EXTRA_HEADERS entry %d must be \"Name: value\"", i+1)
		}
		canon := http.CanonicalHeaderKey(name)
		if forbiddenHdr[canon] {
			return nil, fmt.Errorf("ARBITRON_EXTRA_HEADERS may not set %s", canon)
		}
		h.Add(canon, value)
	}
	return h, nil
}
