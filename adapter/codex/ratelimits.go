package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
	pool "github.com/arbitron/arbitron/domain/runner"
)

// ErrRateLimitsUnavailable means the account rate limits could not be read.
// Callers treat it as capacity UNKNOWN — never as available, never as logged out.
var ErrRateLimitsUnavailable = errors.New("codex: account rate limits unavailable")

// RateLimitsSnapshot is the token-free account view from `codex app-server`.
type RateLimitsSnapshot struct {
	// OrdinaryUsageAllowed: true confirms recovery; false keeps exhaustion; nil
	// is unavailable and clients must not infer recovery from percentages.
	OrdinaryUsageAllowed *bool
	Windows              []pool.LimitWindow
}

// appServerProc is one running `codex app-server`. Kill must unblock a pending
// Stdout read; Wait reaps the process.
type appServerProc interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Kill() error
	Wait() error
}

type appServerStarter func(ctx context.Context, bin string, env []string) (appServerProc, error)

type execAppServerProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
}

func (p *execAppServerProc) Stdin() io.WriteCloser { return p.stdin }
func (p *execAppServerProc) Stdout() io.Reader     { return p.stdout }
func (p *execAppServerProc) Kill() error           { return p.cmd.Process.Kill() }
func (p *execAppServerProc) Wait() error           { return p.cmd.Wait() }

// execAppServer starts `<bin> app-server` with pipes for stdin/stdout; stderr is
// discarded. Killing the process closes stdout, unblocking a pending read.
func execAppServer(_ context.Context, bin string, env []string) (appServerProc, error) {
	cmd := exec.Command(bin, "app-server")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execAppServerProc{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// appServerRequest is header-less JSON-RPC (codex app-server omits "jsonrpc").
const appServerRequest = `{"id":0,"method":"initialize","params":{"clientInfo":{"name":"arbitron","title":"Arbitron runner health","version":"1"}}}` + "\n" +
	`{"method":"initialized"}` + "\n" +
	`{"id":1,"method":"account/rateLimits/read"}` + "\n"

type rpcWindow struct {
	UsedPercent        *float64 `json:"usedPercent"` // any JSON number; 0.171.0 sends integers
	WindowDurationMins int      `json:"windowDurationMins"`
	ResetsAt           *float64 `json:"resetsAt"`
}

type rpcSnapshot struct {
	LimitID   *string    `json:"limitId"`
	LimitName *string    `json:"limitName"`
	Primary   *rpcWindow `json:"primary"`
	Secondary *rpcWindow `json:"secondary"`
}

type rpcRateLimitsResult struct {
	OrdinaryUsageAllowed *bool                  `json:"ordinaryUsageAllowed"`
	RateLimits           *rpcSnapshot           `json:"rateLimits"`
	RateLimitsByLimitID  map[string]rpcSnapshot `json:"rateLimitsByLimitId"`
}

var errNoRateLimitsAnswer = errors.New("codex app-server: no account/rateLimits/read answer")

// RateLimitsRead runs `codex app-server`, writes the initialize handshake and
// account/rateLimits/read, and KEEPS STDIN OPEN until the id-1 answer arrives:
// 0.171.0 exits on stdin EOF without answering (codex-app-server.md). It then
// closes stdin, kills and reaps the process. Bounded by probeTimeout and
// probeMaxResponseBytes. Spends no model tokens; runs under the no-key env.
func RateLimitsRead(ctx context.Context, cfg Config) (RateLimitsSnapshot, error) {
	start := cfg.appServer
	if start == nil {
		start = execAppServer
	}
	callCtx, cancel := procenv.WithTimeout(ctx, probeTimeout)
	defer cancel()
	proc, err := start(callCtx, resolveBinary(cfg.BinaryPath), codexEnv())
	if err != nil {
		return RateLimitsSnapshot{}, fmt.Errorf("%w: %w", ErrRateLimitsUnavailable, claudecli.SanitizeProcessError(err))
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-callCtx.Done():
			_ = proc.Kill() // deadline: unblocks the pending read
		case <-stopped:
		}
	}()
	defer func() {
		close(stopped)
		_ = proc.Stdin().Close()
		_ = proc.Kill()
		_ = proc.Wait() // stopped on purpose; exit status is irrelevant
	}()
	if _, err := io.WriteString(proc.Stdin(), appServerRequest); err != nil {
		return RateLimitsSnapshot{}, ErrRateLimitsUnavailable
	}
	result, err := readRateLimitsAnswer(io.LimitReader(proc.Stdout(), probeMaxResponseBytes))
	if err != nil {
		return RateLimitsSnapshot{}, ErrRateLimitsUnavailable
	}
	windows := map[windowKey]pool.LimitWindow{}
	add := func(s rpcSnapshot) {
		id, scope := "codex", ""
		if s.LimitID != nil && *s.LimitID != "" {
			id = *s.LimitID
		}
		if s.LimitName != nil {
			scope = *s.LimitName
		}
		for _, w := range []*rpcWindow{s.Primary, s.Secondary} {
			if w == nil {
				continue
			}
			putWindow(windows, pool.LimitWindow{LimitID: id, ModelScope: scope, DurationMins: w.WindowDurationMins, UsedPercent: w.UsedPercent, ResetAt: normalizeResetEpoch(w.ResetsAt)})
		}
	}
	if len(result.RateLimitsByLimitID) > 0 {
		for _, s := range result.RateLimitsByLimitID {
			add(s)
		}
	} else if result.RateLimits != nil {
		add(*result.RateLimits)
	}
	return RateLimitsSnapshot{OrdinaryUsageAllowed: result.OrdinaryUsageAllowed, Windows: sortedWindows(windows)}, nil
}

// readRateLimitsAnswer scans stdout lines, skipping the initialize reply,
// notifications (no id) and noise, and returns the id-1 result. Any id-1 error
// (whatever its code), unreadable result, oversized line or EOF first fails.
func readRateLimitsAnswer(r io.Reader) (*rpcRateLimitsResult, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), int(probeMaxResponseBytes))
	for sc.Scan() {
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil || *msg.ID != 1 {
			continue
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return nil, errNoRateLimitsAnswer // never classified from the error's prose
		}
		var res rpcRateLimitsResult
		if json.Unmarshal(msg.Result, &res) != nil {
			return nil, errNoRateLimitsAnswer
		}
		return &res, nil
	}
	return nil, errNoRateLimitsAnswer // EOF before id 1, cap reached, or scanner error
}
