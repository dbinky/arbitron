package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// This is the shipped Cobra binary, not a test entrypoint or an in-process MCP
// server. No factory, logger or principal is replaced in the child.
func buildLayoutStdioBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "arbitron")
	cmd := exec.Command("go", "build", "-race", "-o", path, ".")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return path
}

type layoutStdioProcess struct {
	cmd      *exec.Cmd
	input    io.WriteCloser
	lines    chan string
	scanErr  chan error
	scanDone chan struct{}
	stderr   bytes.Buffer
	waited   bool
}

func startLayoutStdio(t *testing.T, binary string, e *layoutE2EEnv) *layoutStdioProcess {
	t.Helper()
	return startLayoutStdioArgs(t, binary, e, "mcp")
}

func startLayoutStdioArgs(t *testing.T, binary string, e *layoutE2EEnv, args ...string) *layoutStdioProcess {
	t.Helper()
	return startLayoutStdioWithin(t, binary, e, 30*time.Second, args...)
}

func startLayoutStdioWithin(t *testing.T, binary string, e *layoutE2EEnv, deadline time.Duration, args ...string) *layoutStdioProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	p := &layoutStdioProcess{lines: make(chan string, 64), scanErr: make(chan error, 1), scanDone: make(chan struct{})}
	p.cmd = exec.CommandContext(ctx, binary, append([]string{"--config", e.configPath}, args...)...)
	p.cmd.Stderr = &p.stderr
	var err error
	p.input, err = p.cmd.StdinPipe()
	require.NoError(t, err)
	output, err := p.cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, p.cmd.Start())
	go func() {
		defer close(p.scanDone)
		defer close(p.lines)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			select {
			case p.lines <- scanner.Text(): // EVERY line, including blank/text log lines
			case <-ctx.Done():
				return
			}
		}
		p.scanErr <- scanner.Err()
	}()
	t.Cleanup(func() {
		cancel()
		p.input.Close()
		if !p.waited {
			p.cmd.Wait()
		}
		<-p.scanDone
		if t.Failed() {
			t.Logf("subprocess stderr: %s", p.stderr.String())
		}
	})
	return p
}

func decodeLayoutStdioFrame(t *testing.T, line string) map[string]json.RawMessage {
	t.Helper()
	var frame map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(line), &frame), "non-protocol stdout: %q", line)
	require.JSONEq(t, `"2.0"`, string(frame["jsonrpc"]), "stdout must contain JSON-RPC only: %s", line)
	if method, notification := frame["method"]; notification {
		var name string
		require.NoError(t, json.Unmarshal(method, &name))
		require.NotEmpty(t, name)
		require.NotContains(t, frame, "id", "unsolicited server request")
	} else {
		require.Contains(t, frame, "id")
		_, result := frame["result"]
		rpcError, failure := frame["error"]
		require.NotEqual(t, result, failure, "response needs exactly one result/error: %s", line)
		if failure {
			var detail struct {
				Code    *int   `json:"code"`
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(rpcError, &detail))
			require.NotNil(t, detail.Code)
			require.NotEmpty(t, detail.Message)
		}
	}
	return frame
}

func (p *layoutStdioProcess) send(t *testing.T, request any) {
	t.Helper()
	b, err := json.Marshal(request)
	require.NoError(t, err)
	_, err = p.input.Write(append(b, '\n'))
	require.NoError(t, err)
}

func (p *layoutStdioProcess) response(t *testing.T, id any) json.RawMessage {
	t.Helper()
	want, err := json.Marshal(id)
	require.NoError(t, err)
	for {
		select {
		case line, ok := <-p.lines:
			require.True(t, ok, "stdout ended before response %s", want)
			frame := decodeLayoutStdioFrame(t, line)
			if _, notification := frame["method"]; notification {
				continue
			}
			require.JSONEq(t, string(want), string(frame["id"]))
			require.NotContains(t, frame, "error", "%s", line)
			return frame["result"]
		case <-time.After(10 * time.Second):
			t.Fatal("stdio response deadline")
		}
	}
}

func (p *layoutStdioProcess) call(t *testing.T, id any, tool string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	var result gomcp.CallToolResult
	require.NoError(t, json.Unmarshal(p.response(t, id), &result))
	return &result
}

func (p *layoutStdioProcess) initialize(t *testing.T) {
	t.Helper()
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": "initialize", "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "layout-protocol-test", "version": "1"}}})
	var result map[string]any
	require.NoError(t, json.Unmarshal(p.response(t, "initialize"), &result))
	require.NotEmpty(t, result["protocolVersion"])
	require.NotEmpty(t, result["serverInfo"])
	p.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
}

func (p *layoutStdioProcess) finish(t *testing.T, success bool) string {
	t.Helper()
	require.NoError(t, p.input.Close())
	// Drain before Wait: StdoutPipe must reach EOF before Wait closes its pipe.
	for line := range p.lines {
		frame := decodeLayoutStdioFrame(t, line)
		require.Contains(t, frame, "method", "unexpected extra response: %s", line)
	}
	require.NoError(t, <-p.scanErr)
	err := p.cmd.Wait()
	p.waited = true
	if success {
		require.NoError(t, err, "%s", p.stderr.String())
	} else {
		require.Error(t, err)
	}
	require.NotContains(t, p.stderr.String(), "DATA RACE")
	require.NotContains(t, p.stderr.String(), "database is closed")
	return p.stderr.String()
}

func stdioTerminal(t *testing.T, db *sql.DB, revision string, generation int64) layout.Record {
	t.Helper()
	store := adaptsqlite.NewLayoutRepo(db)
	var row layout.Record
	require.Eventually(t, func() bool {
		var err error
		row, err = store.Get(context.Background(), revision)
		return err == nil && row.State.Terminal() && row.JobGeneration == generation
	}, 10*time.Second, 10*time.Millisecond)
	layoutE2EAssertOutcome(t, row)
	return row
}

func TestLayoutStdioProtocol_LocalAuthoringAndRetry(t *testing.T) {
	binary := buildLayoutStdioBinary(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "0"})
	p := startLayoutStdio(t, binary, e)
	p.initialize(t)
	const (
		name        = "stdio-unicode"
		firstLabel  = "并行 🚀\nFirst line"
		secondLabel = "Changed 🚀\nSecond line"
	)
	manifest := func(label string) string {
		b, err := json.MarshalIndent(layoutE2EWorkflow(name, label), "", "  ")
		require.NoError(t, err)
		return string(b)
	}
	created := p.call(t, 1, "workflow_create", map[string]any{"manifest": manifest(firstLabel)})
	require.False(t, created.IsError, mcpE2EText(t, created))
	first := layoutE2EManifest(t, e.privatePath(name))
	require.Equal(t, layoutE2EWorkflow(name, firstLabel).Nodes, first.Nodes)
	require.Equal(t, layoutE2EWorkflow(name, firstLabel).Edges, first.Edges)
	db, err := adaptsqlite.Open(e.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	row1 := stdioTerminal(t, db, first.SavedRevision(), 1)
	require.Equal(t, 1, row1.AttemptLimit)
	require.Equal(t, workflow.Private(localDefaultUser), row1.Destination.Scope)
	require.Equal(t, first.ID, row1.Destination.WorkflowID)
	updated := p.call(t, "update-并行", "workflow_update", map[string]any{"name": name, "manifest": manifest(secondLabel)})
	require.False(t, updated.IsError, mcpE2EText(t, updated))
	second := layoutE2EManifest(t, e.privatePath(name))
	require.Equal(t, layoutE2EWorkflow(name, secondLabel).Nodes, second.Nodes)
	require.Equal(t, layoutE2EWorkflow(name, secondLabel).Edges, second.Edges)
	require.Equal(t, first.ID, second.ID)
	require.NotEqual(t, first.SavedRevision(), second.SavedRevision())
	row2 := stdioTerminal(t, db, second.SavedRevision(), 1)
	for _, pair := range []struct {
		wf  *workflow.Workflow
		row layout.Record
	}{{first, row1}, {second, row2}} {
		b, err := json.Marshal(pair.wf)
		require.NoError(t, err)
		require.JSONEq(t, string(b), string(pair.row.Manifest))
		require.Equal(t, pair.wf.ID, pair.row.Destination.WorkflowID)
		require.Equal(t, pair.wf.Presentation.SavedAt, pair.row.RequestedAt)
	}
	got := p.call(t, 3, "workflow_get", map[string]any{"name": name})
	require.False(t, got.IsError, mcpE2EText(t, got))
	var wire struct {
		Presentation workflow.Presentation `json:"presentation"`
		Arrangement  layout.View           `json:"arrangement"`
	}
	require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, got)), &wire))
	require.Equal(t, second.SavedRevision(), wire.Presentation.Revision)
	require.Equal(t, second.SavedRevision(), wire.Arrangement.SavedRevision)
	require.Equal(t, layout.DisplayFailed, wire.Arrangement.DisplayStatus)
	require.Equal(t, layout.ProvenanceRetainedPositions, wire.Arrangement.Provenance)
	var fetched workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, got)), &fetched))
	require.Equal(t, *second, fetched, "workflow_get must return the actual saved Unicode graph")
	// Repeat the same stale revision: every response must conflict and neither
	// historical nor current rows may acquire a phantom retry generation.
	for _, id := range []any{4, "stale-again"} {
		res := p.call(t, id, "workflow_layout_retry", map[string]any{"name": name, "expectedRevision": first.SavedRevision()})
		require.True(t, res.IsError)
		require.Contains(t, mcpE2EText(t, res), "revision conflict")
	}
	res := p.call(t, 6, "workflow_layout_retry", map[string]any{"name": name, "expectedRevision": second.SavedRevision()})
	require.False(t, res.IsError, mcpE2EText(t, res))
	var ack layout.RetryAck
	require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &ack))
	require.Equal(t, second.SavedRevision(), ack.SavedRevision)
	require.EqualValues(t, 2, ack.JobGeneration)
	retried := stdioTerminal(t, db, second.SavedRevision(), ack.JobGeneration)
	require.Equal(t, row2.Manifest, retried.Manifest)
	require.Equal(t, row1, stdioTerminal(t, db, first.SavedRevision(), 1))
	require.Equal(t, 2, layoutE2ECountRows(t, db, name))
	require.Equal(t, *second, *layoutE2EManifest(t, e.privatePath(name)), "retry must not author another save")
	stderr := p.finish(t, true)
	require.Contains(t, stderr, "loading configuration")
	require.Contains(t, stderr, "layout.maxattempts 0 is below 1; using 1")
	require.Contains(t, stderr, "no Chrome/Chromium browser found for layout review")
	before, err := os.ReadFile(e.logPath)
	require.NoError(t, err)
	time.Sleep(30 * time.Millisecond)
	after, err := os.ReadFile(e.logPath)
	require.NoError(t, err)
	require.Equal(t, before, after, "EOF must join background writers")
}

func TestLayoutStdioProtocol_NonLocalDeniesAuthoring(t *testing.T) {
	binary := buildLayoutStdioBinary(t)
	// Only supply discovery metadata so the real auth adapters can boot. No
	// credential, verified identity or authenticated context is manufactured.
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/openid-configuration", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize",
			"token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	t.Cleanup(issuer.Close)
	for _, mode := range []string{"org", "edge"} {
		t.Run(mode, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{})
			// Save real input in local mode first. Missing names or revisions could
			// mask an accidental default-user fallback with unrelated tool errors.
			ds, closeStack := e.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "Committed private input")))
			require.NoError(t, ds.workflowSvc.WriteFile(ctx, "source", "support.txt", []byte("committed supporting file")))
			require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "published", workflow.VisibilityPublic, ""))
			private := layoutE2EManifest(t, e.privatePath("source"))
			shared := layoutE2EManifest(t, e.sharedPath("published"))
			privateRow := stdioTerminal(t, ds.db, private.SavedRevision(), 1)
			sharedRow := stdioTerminal(t, ds.db, shared.SavedRevision(), 1)
			publication, found, err := ds.publishedRepo.PublicationOf(ctx, "published")
			require.NoError(t, err)
			require.True(t, found)
			files := map[string][]byte{}
			for _, path := range []string{e.privatePath("source"), e.sharedPath("published"),
				filepath.Join(filepath.Dir(e.privatePath("source")), "support.txt"),
				filepath.Join(filepath.Dir(e.sharedPath("published")), "support.txt")} {
				files[path], err = os.ReadFile(path)
				require.NoError(t, err)
			}
			closeStack()
			b, err := os.ReadFile(e.configPath)
			require.NoError(t, err)
			cfg := strings.Replace(string(b), "mode: local", "mode: "+mode, 1)
			if mode == "org" {
				cfg = strings.Replace(cfg, "mode: org", "mode: org\n  oidc:\n    issuerurl: https://issuer.example\n    clientid: test\n    clientsecret: test\n    redirecturl: https://app.example/auth/callback", 1)
			} else {
				cfg = strings.Replace(cfg, "mode: edge", "mode: edge\n  edge:\n    teamdomain: team.example\n    audiences: [test]", 1)
			}
			cfg = strings.ReplaceAll(cfg, "https://issuer.example", issuer.URL)
			cfg = strings.ReplaceAll(cfg, "team.example", issuer.URL)
			require.NoError(t, os.WriteFile(e.configPath, []byte(cfg), 0600))
			p := startLayoutStdio(t, binary, e)
			p.initialize(t)
			manifest, err := json.Marshal(layoutE2EWorkflow("denied", "Real denied input"))
			require.NoError(t, err)
			db, err := adaptsqlite.Open(e.dbPath)
			require.NoError(t, err)
			defer db.Close()
			store := adaptsqlite.NewLayoutRepo(db)
			// All workflow authoring tools, including both publication branches
			// and a retry of an existing terminal generation, must fail closed.
			for _, tc := range []struct {
				name, tool string
				args       map[string]any
			}{
				{"create", "workflow_create", map[string]any{"manifest": string(manifest)}},
				{"update", "workflow_update", map[string]any{"name": "source", "manifest": string(manifest)}},
				{"publish", "workflow_publish", map[string]any{"name": "source", "publishedName": "denied-publish"}},
				{"republish", "workflow_publish", map[string]any{"name": "source", "publishedName": "published"}},
				{"retry", "workflow_layout_retry", map[string]any{"name": "source", "expectedRevision": private.SavedRevision()}},
				{"shared-retry", "workflow_layout_retry", map[string]any{"name": "published", "expectedRevision": shared.SavedRevision()}},
				{"delete", "workflow_delete", map[string]any{"name": "source"}},
				{"write-file", "workflow_write_file", map[string]any{"name": "source", "path": "support.txt", "content": "must not replace"}},
				{"delete-file", "workflow_delete_file", map[string]any{"name": "source", "path": "support.txt"}},
				{"visibility", "workflow_set_visibility", map[string]any{"name": "published", "visibility": "department", "department": "default"}},
				{"copy", "workflow_copy", map[string]any{"name": "published", "newName": "denied-copy"}},
				{"rename", "workflow_rename", map[string]any{"name": "source", "newName": "denied-rename"}},
				{"unpublish", "workflow_unpublish", map[string]any{"name": "published"}},
				{"duplicate", "workflow_duplicate", map[string]any{"name": "source", "newName": "denied-duplicate"}},
				{"archive", "workflow_archive", map[string]any{"name": "source"}},
				{"unarchive", "workflow_unarchive", map[string]any{"name": "source"}},
				{"transfer", "workflow_transfer_ownership", map[string]any{"name": "source", "newOwner": "00000000-0000-0000-0000-000000000002"}},
				{"group-upsert", "workflow_group_upsert", map[string]any{"name": "source", "group": `{"nodeId":"g","successTarget":"t2","failureTarget":"t3"}`}},
				{"group-delete", "workflow_group_delete", map[string]any{"name": "source", "nodeId": "g"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					res := p.call(t, tc.name, tc.tool, tc.args)
					require.True(t, res.IsError, mcpE2EText(t, res))
					want := "unauthenticated"
					switch tc.tool {
					case "workflow_delete", "workflow_rename", "workflow_unpublish", "workflow_archive", "workflow_unarchive", "workflow_transfer_ownership":
						want = "not authorized"
					case "workflow_group_upsert", "workflow_group_delete":
						want = "not authenticated"
					}
					require.Contains(t, mcpE2EText(t, res), want)
					for path, before := range files {
						after, err := os.ReadFile(path)
						require.NoError(t, err)
						require.Equal(t, before, after, path)
					}
					for _, before := range []layout.Record{privateRow, sharedRow} {
						after, err := store.Get(context.Background(), before.Revision)
						require.NoError(t, err)
						require.Equal(t, before, after, "denial must preserve captured input and generation")
					}
					after, found, err := adaptsqlite.NewPublishedWorkflowRepo(db).PublicationOf(context.Background(), "published")
					require.NoError(t, err)
					require.True(t, found)
					require.Equal(t, publication, after)
					var rows, identities, publications int
					require.NoError(t, db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&rows))
					require.NoError(t, db.QueryRow("SELECT count(*) FROM workflow_identity").Scan(&identities))
					require.NoError(t, db.QueryRow("SELECT count(*) FROM published_workflows").Scan(&publications))
					require.Equal(t, 2, rows)
					require.Equal(t, 2, identities)
					require.Equal(t, 1, publications)
					for _, name := range []string{"denied", "denied-publish", "denied-copy", "denied-rename", "denied-duplicate"} {
						require.NoDirExists(t, filepath.Dir(e.privatePath(name)))
						require.NoDirExists(t, filepath.Dir(e.sharedPath(name)))
					}
				})
			}
			stderr := p.finish(t, true)
			require.Contains(t, stderr, "MCP stdio carries no credential outside local auth mode")
		})
	}
}

func TestLayoutStdioProtocol_BootErrorsNeverWriteStdout(t *testing.T) {
	binary := buildLayoutStdioBinary(t)
	for _, kind := range []string{"missing-default-user", "log-file", "malformed-config", "inaccessible-db"} {
		t.Run(kind, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{})
			want := "opening log file"
			switch kind {
			case "malformed-config":
				require.NoError(t, os.WriteFile(e.configPath, []byte("server: [unterminated\n"), 0600))
				want = "loading config file"
			case "inaccessible-db":
				// A directory cannot be opened as a database, even when tests run
				// as root (unlike a chmod-based unreadable-file fixture).
				require.NoError(t, os.Mkdir(e.dbPath, 0700))
				want = "opening database"
			case "log-file":
				require.NoError(t, os.Mkdir(e.logPath, 0700))
			default:
				db, err := adaptsqlite.Open(e.dbPath)
				require.NoError(t, err)
				require.NoError(t, adaptsqlite.RunMigrations(db))
				_, err = db.Exec("DELETE FROM users WHERE id = ?", localDefaultUser.String())
				require.NoError(t, err)
				require.NoError(t, db.Close())
				want = "resolving default user"
			}
			for _, args := range [][]string{{"mcp"}, {"serve", "--mcp-stdio"}} {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				cmd := exec.CommandContext(ctx, binary, append([]string{"--config", e.configPath}, args...)...)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				cancel()
				require.Error(t, err, "%v", args)
				require.IsType(t, &exec.ExitError{}, err)
				require.Empty(t, stdout.String(), "boot failures must emit no stdout, including notifications")
				require.Contains(t, stderr.String(), want)
				require.Contains(t, stderr.String(), "Error:", "Cobra must report the boot error visibly")
				require.NotContains(t, stderr.String(), "DATA RACE")
			}
		})
	}
}

// `serve --mcp-stdio` is the second stdio launch site. It must keep stdout
// protocol-only while HTTP logs and boot output go to stderr, author as the
// local default user, and leave the database usable by a second `arbitron mcp`
// process that boots while the server is still running.
func TestLayoutStdioProtocol_ServeStdioAndPeerMCPBootWithoutLock(t *testing.T) {
	binary := buildLayoutStdioBinary(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	serve := startLayoutStdioArgs(t, binary, e, "serve", "--mcp-stdio")
	serve.initialize(t)
	manifest := func(name string) string {
		b, err := json.Marshal(layoutE2EWorkflow(name, "Beside a running server"))
		require.NoError(t, err)
		return string(b)
	}
	created := serve.call(t, "serve-create", "workflow_create", map[string]any{"manifest": manifest("from-serve")})
	require.False(t, created.IsError, mcpE2EText(t, created))
	require.FileExists(t, e.privatePath("from-serve"))

	peer := startLayoutStdio(t, binary, e)
	peer.initialize(t)
	got := peer.call(t, "peer-get", "workflow_get", map[string]any{"name": "from-serve"})
	require.False(t, got.IsError, mcpE2EText(t, got))
	peerCreated := peer.call(t, "peer-create", "workflow_create", map[string]any{"manifest": manifest("from-peer")})
	require.False(t, peerCreated.IsError, mcpE2EText(t, peerCreated))
	peerErr := peer.finish(t, true)
	require.NotContains(t, peerErr, "database is locked")
	require.NotContains(t, peerErr, "SQLITE_BUSY")
	require.FileExists(t, e.privatePath("from-peer"))

	// EOF ends only the stdio transport; the server keeps running until signalled.
	require.NoError(t, serve.input.Close())
	require.NoError(t, serve.cmd.Process.Signal(os.Interrupt))
	for line := range serve.lines {
		frame := decodeLayoutStdioFrame(t, line)
		require.Contains(t, frame, "method", "unexpected extra response: %s", line)
	}
	require.NoError(t, <-serve.scanErr)
	err := serve.cmd.Wait()
	serve.waited = true
	require.NoError(t, err, "%s", serve.stderr.String())
	serveErr := serve.stderr.String()
	require.NotContains(t, serveErr, "database is locked")
	require.NotContains(t, serveErr, "DATA RACE")
}
