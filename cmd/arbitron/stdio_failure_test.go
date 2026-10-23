package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

// Run the real Cobra command in process so a caller cancellation can be tested
// independently of EOF. The input writer stays open until test cleanup.
type stdioFailureCommand struct {
	scanErr  chan error
	input    *os.File
	lines    chan string
	done     chan error
	cancel   context.CancelFunc
	cobraErr bytes.Buffer
}

// commandLikeProduction makes a real command boot the way production does:
// readiness never waits on the first catalog discovery (the root-test seam
// defaultAwaitInitialDiscovery would otherwise hold Start on the network).
func commandLikeProduction(t *testing.T) {
	t.Helper()
	prior := defaultAwaitInitialDiscovery
	defaultAwaitInitialDiscovery = false
	t.Cleanup(func() { defaultAwaitInitialDiscovery = prior })
}

func startStdioFailureCommand(t *testing.T, e *layoutE2EEnv, args ...string) *stdioFailureCommand {
	t.Helper()
	commandLikeProduction(t)
	input, writer, err := os.Pipe()
	require.NoError(t, err)
	output, sink, err := os.Pipe()
	require.NoError(t, err)
	priorIn, priorOut, priorLog := os.Stdin, os.Stdout, slog.Default()
	os.Stdin, os.Stdout = input, sink
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	p := &stdioFailureCommand{input: writer, lines: make(chan string, 64), scanErr: make(chan error, 1), done: make(chan error, 1), cancel: cancel}
	cmd := newRootCmd()
	cmd.SetArgs(append([]string{"--config", e.configPath}, args...))
	cmd.SetErr(&p.cobraErr)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		defer close(p.lines)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			p.lines <- scanner.Text()
		}
		p.scanErr <- scanner.Err()
	}()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		p.done <- cmd.ExecuteContext(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		writer.Close()
		select {
		case <-finished:
		case <-time.After(15 * time.Second):
			t.Error("stdio command did not join")
		}
		sink.Close()
		<-scanned
		require.NoError(t, <-p.scanErr)
		input.Close()
		output.Close()
		os.Stdin, os.Stdout = priorIn, priorOut
		slog.SetDefault(priorLog)
	})
	return p
}

func (p *stdioFailureCommand) send(t *testing.T, id string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/list"})
	require.NoError(t, err)
	_, err = p.input.Write(append(b, '\n'))
	require.NoError(t, err)
}

func (p *stdioFailureCommand) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-p.done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("command did not return while stdin remained open")
		return nil
	}
}

func (p *stdioFailureCommand) noLateFrames(t *testing.T) {
	t.Helper()
	p.send(t, "after-shutdown") // open input must not cause a new response
	select {
	case line := <-p.lines:
		t.Fatalf("response after shutdown: %q", line)
	case <-time.After(100 * time.Millisecond):
	}
}

// Renaming the persisted local user after the boot-time lookup forces the
// *later* real SQLite lookup in stdioContext to fail. No identity is faked and
// no production hook is added. Swallowing the identity error breaks this test.
func TestStdioFailure_IdentityLookupAfterBoot(t *testing.T) {
	for _, mode := range []string{"mcp", "serve"} {
		t.Run(mode, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			port := listener.Addr().(*net.TCPAddr).Port
			require.NoError(t, listener.Close())
			cfg, err := os.ReadFile(e.configPath)
			require.NoError(t, err)
			cfg = bytes.Replace(cfg, []byte("port: 0"), []byte("port: "+strconv.Itoa(port)), 1)
			require.NoError(t, os.WriteFile(e.configPath, cfg, 0600))
			old := layoutRenderReviewerFactory
			t.Cleanup(func() { layoutRenderReviewerFactory = old })
			layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
				db, err := adaptsqlite.Open(cfg.Database.Path)
				if err != nil {
					return nil, nil, nil, err
				}
				defer db.Close()
				_, err = db.Exec("UPDATE users SET username='temporarily-unavailable' WHERE username='local'")
				if err != nil {
					return nil, nil, nil, err
				}
				return old(cfg, logger)
			}
			args := []string{mode}
			if mode == "serve" {
				args = append(args, "--mcp-stdio")
			}
			p := startStdioFailureCommand(t, e, args...)
			if mode == "mcp" {
				err := p.wait(t)
				require.ErrorContains(t, err, "resolving local stdio identity")
				require.ErrorIs(t, err, sql.ErrNoRows)
				require.Contains(t, p.cobraErr.String(), "resolving local stdio identity")
			} else {
				require.Eventually(t, func() bool {
					b, _ := os.ReadFile(e.logPath)
					return bytes.Contains(b, []byte("MCP stdio identity unavailable")) && bytes.Contains(b, []byte("resolving local stdio identity"))
				}, 15*time.Second, 10*time.Millisecond)
				// Repair the data only after observing the stdio failure; HTTP must
				// remain alive and resolve its own identity through the real middleware.
				db, err := adaptsqlite.Open(e.dbPath)
				require.NoError(t, err)
				_, err = db.Exec("UPDATE users SET username='local' WHERE username='temporarily-unavailable'")
				require.NoError(t, err)
				require.NoError(t, db.Close())
				client := &http.Client{Timeout: time.Second}
				require.Eventually(t, func() bool {
					resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/api/v1/workflows")
					if err != nil {
						return false
					}
					defer resp.Body.Close()
					io.Copy(io.Discard, resp.Body)
					return resp.StatusCode == http.StatusOK
				}, 15*time.Second, 10*time.Millisecond)
				select {
				case err := <-p.done:
					t.Fatalf("stdio identity failure stopped HTTP: %v", err)
				default:
				}
				p.noLateFrames(t) // failed stdio must stay stopped while HTTP is live
				p.cancel()
				require.NoError(t, p.wait(t))
			}
			p.noLateFrames(t)
			log, err := os.ReadFile(e.logPath)
			require.NoError(t, err)
			require.Contains(t, string(log), "flat-store migration complete")
			require.NotContains(t, string(log), "database is closed")
		})
	}
}

// Cancellation must stop both launch sites without EOF. Returning without
// joining stdio would permit requests after command resources have closed.
func TestStdioFailure_CancellationWithInputOpen(t *testing.T) {
	for _, mode := range []string{"mcp", "serve"} {
		t.Run(mode, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{})
			args := []string{mode}
			if mode == "serve" {
				args = append(args, "--mcp-stdio")
			}
			p := startStdioFailureCommand(t, e, args...)
			p.send(t, "before-cancellation")
			select {
			case line := <-p.lines:
				frame := decodeLayoutStdioFrame(t, line)
				require.JSONEq(t, `"before-cancellation"`, string(frame["id"]))
				require.NotContains(t, frame, "error")
			case <-time.After(15 * time.Second):
				t.Fatal("stdio did not start")
			}
			p.cancel()
			err := p.wait(t)
			if mode == "mcp" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			before, err := os.ReadFile(e.logPath)
			require.NoError(t, err)
			p.noLateFrames(t)
			after, err := os.ReadFile(e.logPath)
			require.NoError(t, err)
			require.Equal(t, before, after, "shutdown must join log writers")
			require.NotContains(t, strings.ToLower(string(after)), "database is closed")
		})
	}
}
