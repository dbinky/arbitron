package lunaroute_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

const (
	captureBodyLimit   = 8 << 20
	captureHeaderLimit = 64 << 10
	captureRecordLimit = 1024
	captureTimeout     = 2 * time.Minute
	captureHost        = "gw.lunaroute.com"
)

var errCaptureInvalid = errors.New("inference capture incomplete or invalid")

// These are the only fields persisted from the credential-bearing exchange.
// Operation is supplied by the backend-specific lifecycle parser, never inferred
// from a user label, argv, configured model or a successful process exit.
type capturedInference struct {
	RunID               string             `json:"run_id"`
	Backend             string             `json:"backend"`
	Connection          string             `json:"connection"`
	Category            string             `json:"category"`
	RequestID           string             `json:"request_id"`
	Method              string             `json:"method"`
	Host                string             `json:"host"`
	Path                string             `json:"path"`
	RequestedModel      string             `json:"requested_model"`
	MaxTokens           int                `json:"max_tokens,omitempty"`
	ClaudeProjectMemory bool               `json:"claude_project_memory,omitempty"`
	ReportedModels      []string           `json:"reported_models"`
	AuthMatchesSelected bool               `json:"auth_matches_selected"`
	AuthHeaderNames     []string           `json:"auth_header_names"`
	Completed           bool               `json:"completed"`
	StartedAt           time.Time          `json:"started_at"`
	FinishedAt          time.Time          `json:"finished_at"`
	Failure             string             `json:"failure,omitempty"`
	Operation           *capturedOperation `json:"operation,omitempty"`
	SessionID           string             `json:"session_id,omitempty"`
	ParentSessionID     string             `json:"parent_session_id,omitempty"`
}

type capturedOperation struct {
	RunID      string    `json:"run_id"`
	Backend    string    `json:"backend"`
	ID         string    `json:"id"`
	Session    string    `json:"session"`
	Category   string    `json:"category"`
	Trigger    string    `json:"trigger"`
	Source     string    `json:"source"`
	Version    string    `json:"version"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	RequestIDs []string  `json:"request_ids"`
}

type captureLimits struct {
	BodyBytes, Records int
	Timeout            time.Duration
}
type inferenceCapture struct {
	mu                              sync.Mutex
	drained                         *sync.Cond
	active                          int
	records                         []capturedInference
	claudeMemory                    string            // private source-formatted path/nonce, never serialized
	openCodeTitles                  map[string]string // private response text, never serialized
	openCodeCompactions             map[string]string // private summary comparison, never serialized
	failed                          bool
	closed                          bool
	nextID                          int
	requestCount                    int
	runID, backend, connection, key string
	dir, caPath, proxyURL           string
	limits                          captureLimits
	certificate                     tls.Certificate
	server                          *http.Server
	transport                       *http.Transport
	listener                        net.Listener
	ctx                             context.Context
	cancel                          context.CancelFunc
	conns                           map[net.Conn]bool
	wg                              sync.WaitGroup
	closeOnce                       sync.Once
	closeErr                        error
}

func startInferenceCapture(t *testing.T, runID, backend, connection, key string) *inferenceCapture {
	t.Helper()
	// No ambient proxy, custom trust roots, redirects or destination overrides.
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: captureTimeout}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   captureTimeout,
		ResponseHeaderTimeout: captureTimeout,
		ForceAttemptHTTP2:     true,
	}
	c, err := newInferenceCapture(runID, backend, connection, key, transport, captureLimits{})
	if err != nil {
		t.Fatal("cannot start private inference capture")
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

// The injected upstream exists only for deterministic tooling fixtures. Live
// callers use startInferenceCapture, which fixes destination, TLS and transport.
func newInferenceCapture(runID, backend, connection, key string, transport *http.Transport, limits captureLimits) (*inferenceCapture, error) {
	if !provider.ValidLunaRouteKey(key) || !safeCaptureText(runID, key) || !safeCaptureText(backend, key) || !safeCaptureText(connection, key) || transport == nil {
		return nil, errCaptureInvalid
	}
	if limits.BodyBytes == 0 {
		limits.BodyBytes = captureBodyLimit
	}
	if limits.Records == 0 {
		limits.Records = captureRecordLimit
	}
	if limits.Timeout == 0 {
		limits.Timeout = captureTimeout
	}
	if limits.BodyBytes < 1 || limits.BodyBytes > captureBodyLimit || limits.Records < 1 || limits.Records > captureRecordLimit || limits.Timeout <= 0 || limits.Timeout > captureTimeout {
		return nil, errCaptureInvalid
	}
	dir, err := os.MkdirTemp("", "arbitron-inference-capture-")
	if err != nil {
		return nil, errCaptureInvalid
	}
	c := &inferenceCapture{runID: runID, backend: backend, connection: connection, key: key, dir: dir, caPath: filepath.Join(dir, "ca.pem"), limits: limits, transport: transport, conns: make(map[net.Conn]bool)}
	transport.MaxResponseHeaderBytes = captureHeaderLimit
	c.drained = sync.NewCond(&c.mu)
	c.ctx, c.cancel = context.WithCancel(context.Background())
	ok := false
	defer func() {
		if !ok {
			_ = c.Close()
		}
	}()
	if os.Chmod(dir, 0700) != nil {
		return nil, errCaptureInvalid
	}
	certificate, ca, err := captureCertificate()
	if err != nil {
		return nil, errCaptureInvalid
	}
	c.certificate = certificate
	if os.WriteFile(c.caPath, ca, 0600) != nil {
		return nil, errCaptureInvalid
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errCaptureInvalid
	}
	c.listener = listener
	c.proxyURL = "http://" + listener.Addr().String()
	c.server = &http.Server{Handler: http.HandlerFunc(c.connect), ReadHeaderTimeout: limits.Timeout, IdleTimeout: limits.Timeout, MaxHeaderBytes: captureHeaderLimit, ErrorLog: log.New(io.Discard, "", 0)}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		err := c.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.fail()
		}
	}()
	ok = true
	return c, nil
}
func captureCertificate() (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Arbitron ephemeral inference capture"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf := &x509.Certificate{SerialNumber: new(big.Int).Add(serial, big.NewInt(1)), Subject: pkix.Name{CommonName: captureHost}, DNSNames: []string{captureHost}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: leafKey}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}
func (c *inferenceCapture) fail() { c.mu.Lock(); c.failed = true; c.mu.Unlock() }
func (c *inferenceCapture) connect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect || r.Host != captureHost+":443" || r.URL.Host != r.Host || len(r.Header.Values("Proxy-Authorization")) != 0 {
		c.fail()
		http.Error(w, "capture destination refused", http.StatusForbidden)
		return
	}
	raw, buffer, err := w.(http.Hijacker).Hijack()
	if err != nil {
		c.fail()
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		raw.Close()
		return
	}
	c.conns[raw] = true
	c.wg.Add(1)
	c.mu.Unlock()
	defer func() { raw.Close(); c.mu.Lock(); delete(c.conns, raw); c.mu.Unlock(); c.wg.Done() }()
	if buffer.Reader.Buffered() != 0 {
		c.fail()
		return
	}
	if _, err = raw.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		c.fail()
		return
	}
	_ = raw.SetDeadline(time.Now().Add(c.limits.Timeout))
	secured := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{c.certificate}, MinVersion: tls.VersionTLS12})
	if secured.HandshakeContext(c.ctx) != nil {
		c.fail()
		return
	}
	reader := bufio.NewReader(secured)
	for {
		_ = raw.SetDeadline(time.Now().Add(c.limits.Timeout))
		request, nextReader, err := readCaptureRequest(reader)
		reader = nextReader
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			c.mu.Lock()
			closing := c.closed
			c.mu.Unlock()
			if !closing || !errors.Is(err, net.ErrClosed) {
				c.fail()
			}
			return
		}
		if !c.forward(secured, request) {
			return
		}
		if request.Close {
			return
		}
	}
}

// Bound decrypted HTTP headers as well as bodies. Keep the parser's buffered
// reader for the next keep-alive exchange so pipelined bytes are never lost.
func readCaptureRequest(reader *bufio.Reader) (*http.Request, *bufio.Reader, error) {
	var header bytes.Buffer
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			if header.Len() == 0 && len(line) == 0 {
				return nil, reader, err
			}
			return nil, reader, errCaptureInvalid
		}
		if header.Len()+len(line) > captureHeaderLimit {
			return nil, reader, errCaptureInvalid
		}
		header.Write(line)
		if bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n")) {
			break
		}
	}
	combined := bufio.NewReader(io.MultiReader(bytes.NewReader(header.Bytes()), reader))
	request, err := http.ReadRequest(combined)
	return request, combined, err
}

func capturePath(path string) bool {
	return path == "/v1/messages" || path == "/v1/chat/completions" || path == "/v1/responses"
}
func safeCaptureText(s, key string) bool {
	return s != "" && len(s) <= 256 && !unsafeText(s, key) && !strings.ContainsAny(s, "\r\n\t")
}
func safeCaptureModel(model string) bool {
	if !safeCaptureText(model, "") {
		return false
	}
	for _, r := range model {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:/-", r)) {
			return false
		}
	}
	return true
}
func captureAuthNames(header http.Header) []string {
	var names []string
	for name := range header {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "authorization") || strings.Contains(lower, "api-key") || strings.Contains(lower, "apikey") || lower == "cookie" || strings.Contains(lower, "token") {
			names = append(names, http.CanonicalHeaderKey(name))
		}
	}
	slices.Sort(names)
	return names
}
func (c *inferenceCapture) forward(conn net.Conn, r *http.Request) bool {
	c.mu.Lock()
	c.active++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.active--
		c.drained.Broadcast()
		c.mu.Unlock()
	}()
	started := time.Now().UTC()
	c.mu.Lock()
	c.requestCount++
	id := c.requestCount
	over := id > c.limits.Records
	if over {
		c.failed = true
	}
	c.mu.Unlock()
	if over {
		return false
	}
	record := capturedInference{RunID: c.runID, Backend: c.backend, Connection: c.connection, Category: "main", RequestID: "request-" + strconv.Itoa(id), Method: r.Method, Host: r.Host, Path: r.URL.Path, StartedAt: started}
	finish := func(failure string) {
		record.FinishedAt = time.Now().UTC()
		record.Failure = failure
		c.mu.Lock()
		if len(c.records) < c.limits.Records {
			c.records = append(c.records, record)
		} else {
			c.failed = true
		}
		c.mu.Unlock()
	}
	reject := func(failure string) bool {
		c.fail()
		finish(failure)
		_ = (&http.Response{StatusCode: 502, ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("capture exchange refused")), Close: true}).Write(conn)
		return false
	}
	record.AuthHeaderNames = captureAuthNames(r.Header)
	record.AuthMatchesSelected = len(r.Header.Values("Authorization")) == 1 && r.Header.Get("Authorization") == "Bearer "+c.key
	if r.Method != "POST" || r.Host != captureHost || !capturePath(r.URL.Path) || r.URL.RawQuery != "" || r.URL.RawPath != "" || r.URL.User != nil || r.URL.IsAbs() || !record.AuthMatchesSelected || !slices.Equal(record.AuthHeaderNames, []string{"Authorization"}) {
		r.Body.Close()
		return reject("request_refused")
	}
	if c.backend == "opencode" {
		for _, field := range []struct {
			name string
			dest *string
		}{{"X-Opencode-Session-Id", &record.SessionID}, {"X-Opencode-Parent-Session-Id", &record.ParentSessionID}} {
			values := r.Header.Values(field.name)
			if len(values) > 1 || len(values) == 1 && (!openCodeID(values[0], "ses_") || !safeCaptureText(values[0], c.key)) {
				r.Body.Close()
				return reject("session_schema")
			}
			if len(values) == 1 {
				*field.dest = values[0]
			}
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(c.limits.BodyBytes)+1))
	closeErr := r.Body.Close()
	if err != nil || closeErr != nil || len(body) > c.limits.BodyBytes || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(body))) {
		return reject("request_schema")
	}
	var payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(body, &payload) != nil || !safeCaptureModel(payload.Model) || !safeCaptureText(payload.Model, c.key) {
		return reject("request_schema")
	}
	record.RequestedModel = payload.Model
	if r.URL.Path == "/v1/messages" {
		var budget struct {
			MaxTokens int `json:"max_tokens"`
		}
		if json.Unmarshal(body, &budget) != nil {
			return reject("request_schema")
		}
		record.MaxTokens = budget.MaxTokens
		c.mu.Lock()
		if c.backend == "claude-code" && id == 1 {
			record.ClaudeProjectMemory = claudeMemoryInRequest(body, c.claudeMemory)
		}
		c.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.limits.Timeout)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, "POST", hostedRoot+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		return reject("request_schema")
	}
	upstream.Header = r.Header.Clone()
	upstream.Header.Del("Connection")
	upstream.Header.Del("Proxy-Connection")
	upstream.Host = captureHost
	response, err := c.transport.RoundTrip(upstream)
	if err != nil {
		return reject("upstream_transport")
	}
	defer response.Body.Close()
	// Tee while forwarding, rather than waiting for terminal output before the
	// real CLI receives its stream. A read/cap error truncates the exchange and
	// poisons the entire observation, including previously successful requests.
	buffer := &captureBuffer{limit: c.limits.BodyBytes}
	response.Body = &captureReadCloser{Reader: io.TeeReader(response.Body, buffer), Closer: response.Body}
	response.Close = r.Close
	err = response.Write(conn)
	closeErr = response.Body.Close()
	if err != nil || closeErr != nil {
		c.fail()
		finish("response_read")
		return false
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		c.fail()
		finish("upstream_status")
		return false
	}
	stream := strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")
	if payload.Stream != stream {
		c.fail()
		finish("response_schema")
		return false
	}
	models, err := extractCapturedResponse(r.URL.Path, buffer.Bytes(), stream)
	if err != nil {
		c.fail()
		finish("response_schema")
		return false
	}
	if c.backend == "opencode" && r.URL.Path == "/v1/chat/completions" && isOpenCodeTitleRequest(body) {
		title, err := openCodeTitleResponse(buffer.Bytes(), stream)
		if err != nil {
			c.fail()
			finish("title_schema")
			return false
		}
		c.mu.Lock()
		if c.openCodeTitles == nil {
			c.openCodeTitles = make(map[string]string)
		}
		c.openCodeTitles[record.RequestID] = title
		c.mu.Unlock()
	}
	if c.backend == "opencode" && r.URL.Path == "/v1/chat/completions" && isOpenCodeCompactionRequest(body) {
		text, err := openCodeTextResponse(buffer.Bytes(), stream)
		if err != nil {
			c.fail()
			finish("compaction_schema")
			return false
		}
		c.mu.Lock()
		if c.openCodeCompactions == nil {
			c.openCodeCompactions = make(map[string]string)
		}
		c.openCodeCompactions[record.RequestID] = text
		c.mu.Unlock()
	}
	record.ReportedModels = models
	record.Completed = true
	finish("")
	return true
}

type captureReadCloser struct {
	io.Reader
	io.Closer
}
type captureBuffer struct {
	bytes.Buffer
	limit int
}

func (b *captureBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errCaptureInvalid
	}
	return b.Buffer.Write(p)
}

func (c *inferenceCapture) Records() []capturedInference {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.active != 0 {
		c.drained.Wait()
	}
	out := make([]capturedInference, len(c.records))
	for i, r := range c.records {
		out[i] = cloneCaptureRecord(r)
	}
	if c.failed {
		out = append(out, capturedInference{RunID: c.runID, Backend: c.backend, Connection: c.connection, Failure: "capture_failed"})
	}
	return out
}
func cloneCaptureRecord(r capturedInference) capturedInference {
	r.ReportedModels = slices.Clone(r.ReportedModels)
	r.AuthHeaderNames = slices.Clone(r.AuthHeaderNames)
	if r.Operation != nil {
		op := *r.Operation
		op.RequestIDs = slices.Clone(op.RequestIDs)
		r.Operation = &op
	}
	return r
}

// CorrelateOperation is fed by the owning backend experiment's lifecycle parser
// after requests drain. Its source/version, session, trigger and exact request
// IDs must come from that experiment, not a caller-provided category alone.
func (c *inferenceCapture) CorrelateOperation(op capturedOperation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !validCapturedOperation(&op, c.runID, c.backend) || len(op.RequestIDs) == 0 {
		return errCaptureInvalid
	}
	indices := make([]int, 0, len(op.RequestIDs))
	seen := map[string]bool{}
	for _, id := range op.RequestIDs {
		if seen[id] {
			return errCaptureInvalid
		}
		seen[id] = true
		found := -1
		for i, r := range c.records {
			if r.RequestID == id {
				if r.Operation != nil || r.StartedAt.Before(op.StartedAt) || r.FinishedAt.After(op.FinishedAt) {
					return errCaptureInvalid
				}
				found = i
			}
		}
		if found < 0 {
			return errCaptureInvalid
		}
		indices = append(indices, found)
	}
	for _, i := range indices {
		copy := op
		copy.RequestIDs = slices.Clone(op.RequestIDs)
		c.records[i].Category = op.Category
		c.records[i].Operation = &copy
	}
	return nil
}
func (c *inferenceCapture) WrapBinary(t *testing.T, realBinary string) string {
	t.Helper()
	if !filepath.IsAbs(realBinary) {
		t.Fatal("capture requires an absolute binary path")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		t.Fatal("capture is closed")
	}
	// Preserve production argv/stdin/cwd/credentials; inject only private trust
	// and proxy settings. No production env allowlist is changed.
	name := filepath.Join(c.dir, "launcher-"+strconv.Itoa(c.nextID))
	c.nextID++
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nunset NO_PROXY no_proxy ALL_PROXY all_proxy HTTP_PROXY http_proxy\nexport HTTPS_PROXY=" + quote(c.proxyURL) + "\nexport https_proxy=" + quote(c.proxyURL) + "\nexport SSL_CERT_FILE=" + quote(c.caPath) + "\nexport NODE_EXTRA_CA_CERTS=" + quote(c.caPath) + "\nexec " + quote(realBinary) + " \"$@\"\n"
	if os.WriteFile(name, []byte(script), 0700) != nil {
		t.Fatal("cannot write capture launcher")
	}
	return name
}
func (c *inferenceCapture) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		for conn := range c.conns {
			_ = conn.Close()
		}
		c.mu.Unlock()
		if c.cancel != nil {
			c.cancel()
		}
		if c.server != nil {
			_ = c.server.Close()
		}
		if c.listener != nil {
			_ = c.listener.Close()
		}
		if c.transport != nil {
			c.transport.CloseIdleConnections()
		}
		c.wg.Wait()
		c.mu.Lock()
		c.key = ""
		c.claudeMemory = ""
		c.openCodeTitles = nil
		c.openCodeCompactions = nil
		c.mu.Unlock()
		if c.dir != "" && os.RemoveAll(c.dir) != nil {
			c.closeErr = errors.New("cannot remove private capture files")
		}
	})
	return c.closeErr
}

func validCapturedOperation(op *capturedOperation, runID, backend string) bool {
	if op == nil || op.RunID != runID || op.Backend != backend || op.Category == "main" || op.StartedAt.IsZero() || !op.FinishedAt.After(op.StartedAt) || len(op.RequestIDs) == 0 {
		return false
	}
	for _, s := range []string{op.ID, op.Session, op.Category, op.Trigger, op.Source, op.Version} {
		if !safeCaptureText(s, "") {
			return false
		}
	}
	return true
}
func validateCapturedInference(records []capturedInference, selectedModel string, requiredCategories []string) error {
	if len(records) == 0 || len(requiredCategories) == 0 || !modelidentity.LunaRouteExecutedModelAccepted(selectedModel, selectedModel) {
		return errCaptureInvalid
	}
	first := records[0]
	seen := map[string]bool{}
	categories := map[string]bool{}
	for _, r := range records {
		for _, s := range []string{r.RunID, r.Backend, r.Connection, r.Category, r.RequestID} {
			if !safeCaptureText(s, "") {
				return errCaptureInvalid
			}
		}
		if r.RunID != first.RunID || r.Backend != first.Backend || r.Connection != first.Connection || seen[r.RequestID] || r.Failure != "" || !r.Completed || !r.AuthMatchesSelected || !slices.Equal(r.AuthHeaderNames, []string{"Authorization"}) || r.Method != "POST" || r.Host != captureHost || !capturePath(r.Path) || r.RequestedModel != selectedModel || len(r.ReportedModels) == 0 || r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
			return errCaptureInvalid
		}
		seen[r.RequestID] = true
		for _, reported := range r.ReportedModels {
			if !modelidentity.LunaRouteExecutedModelAccepted(selectedModel, reported) {
				return errCaptureInvalid
			}
		}
		if r.Category != "main" {
			op := r.Operation
			if !validCapturedOperation(op, r.RunID, r.Backend) || op.Category != r.Category || !slices.Contains(op.RequestIDs, r.RequestID) || r.StartedAt.Before(op.StartedAt) || r.FinishedAt.After(op.FinishedAt) {
				return errCaptureInvalid
			}
		} else if r.Operation != nil {
			return errCaptureInvalid
		}
		categories[r.Category] = true
	}
	// Every lifecycle reference must resolve to the same current operation,
	// not an unobserved request, stale run or conflicting reused operation id.
	byID := make(map[string]capturedInference, len(records))
	operations := make(map[string]string)
	for _, r := range records {
		byID[r.RequestID] = r
		if r.Operation != nil {
			raw, _ := json.Marshal(r.Operation)
			if prior, ok := operations[r.Operation.ID]; ok && prior != string(raw) {
				return errCaptureInvalid
			}
			operations[r.Operation.ID] = string(raw)
		}
	}
	for _, r := range records {
		if r.Operation == nil {
			continue
		}
		seenIDs := make(map[string]bool)
		for _, id := range r.Operation.RequestIDs {
			linked, ok := byID[id]
			if !ok || seenIDs[id] || linked.Operation == nil || linked.Operation.ID != r.Operation.ID {
				return errCaptureInvalid
			}
			seenIDs[id] = true
		}
	}
	for _, category := range requiredCategories {
		if !categories[category] {
			return errCaptureInvalid
		}
	}
	return nil
}

// extractCapturedResponse retains only model strings after a complete JSON or
// SSE terminal lifecycle. Raw prompts, output/tool text and errors never escape.
func extractCapturedResponse(path string, body []byte, stream bool) ([]string, error) {
	if !capturePath(path) || len(body) == 0 || len(body) > captureBodyLimit {
		return nil, errCaptureInvalid
	}
	models := []string{}
	add := func(model string) bool {
		if !safeCaptureModel(model) {
			return false
		}
		if !slices.Contains(models, model) {
			models = append(models, model)
		}
		return true
	}
	decode := func(raw []byte) (map[string]json.RawMessage, bool) {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(raw))) {
			return nil, false
		}
		if rawError, ok := object["error"]; ok && !bytes.Equal(bytes.TrimSpace(rawError), []byte("null")) {
			return nil, false
		}
		return object, true
	}
	text := func(object map[string]json.RawMessage, key string) string {
		var s string
		_ = json.Unmarshal(object[key], &s)
		return s
	}
	messageStopped := func(reason string) bool {
		return reason == "end_turn" || reason == "tool_use" || reason == "stop_sequence"
	}
	chatStopped := func(reason string) bool { return reason == "stop" || reason == "tool_calls" }
	chatFinished := func(object map[string]json.RawMessage) bool {
		var choices []struct {
			FinishReason string `json:"finish_reason"`
		}
		if json.Unmarshal(object["choices"], &choices) != nil || len(choices) == 0 {
			return false
		}
		for _, choice := range choices {
			if !chatStopped(choice.FinishReason) {
				return false
			}
		}
		return true
	}
	if !stream {
		object, ok := decode(body)
		if !ok || !add(text(object, "model")) {
			return nil, errCaptureInvalid
		}
		terminal := false
		switch path {
		case "/v1/chat/completions":
			terminal = chatFinished(object)
		case "/v1/messages":
			terminal = text(object, "type") == "message" && messageStopped(text(object, "stop_reason"))
		case "/v1/responses":
			terminal = text(object, "status") == "completed"
		}
		if !terminal {
			return nil, errCaptureInvalid
		}
		return models, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), captureBodyLimit)
	var data []string
	event := ""
	terminal, finished, started := false, false, false
	consume := func() bool {
		if len(data) == 0 {
			return true
		}
		raw := strings.Join(data, "\n")
		data = nil
		if terminal {
			return false
		}
		if raw == "[DONE]" {
			if path != "/v1/chat/completions" || !finished {
				return false
			}
			terminal = true
			return true
		}
		object, ok := decode([]byte(raw))
		if !ok {
			return false
		}
		typ := text(object, "type")
		if typ == "error" {
			return false
		}
		if event != "" && event != typ {
			return false
		}
		switch path {
		case "/v1/chat/completions":
			if rawModel, present := object["model"]; present {
				var model string
				if json.Unmarshal(rawModel, &model) != nil || !add(model) {
					return false
				}
			}
			var choices []struct {
				FinishReason *string `json:"finish_reason"`
			}
			if json.Unmarshal(object["choices"], &choices) != nil || choices == nil {
				return false
			}
			for _, choice := range choices {
				if choice.FinishReason != nil && !chatStopped(*choice.FinishReason) {
					return false
				}
			}
			finished = finished || chatFinished(object)
		case "/v1/messages":
			switch typ {
			case "message_start":
				nested, ok := decode(object["message"])
				if !ok || started || !add(text(nested, "model")) {
					return false
				}
				started = true
			case "message_delta":
				nested, ok := decode(object["delta"])
				if !ok || !started || finished {
					return false
				}
				finished = messageStopped(text(nested, "stop_reason"))
			case "message_stop":
				if !started || !finished {
					return false
				}
				terminal = true
			case "content_block_start", "content_block_delta", "content_block_stop":
				if !started || finished {
					return false
				}
			case "ping":
			default:
				return false
			}
		case "/v1/responses":
			if typ == "response.failed" || typ == "response.incomplete" || typ == "error" {
				return false
			}
			if nestedRaw, ok := object["response"]; ok {
				nested, ok := decode(nestedRaw)
				if !ok {
					return false
				}
				if model := text(nested, "model"); model != "" && !add(model) {
					return false
				}
				if typ == "response.completed" {
					if text(nested, "status") != "completed" || text(nested, "model") == "" {
						return false
					}
					terminal = true
				}
			}
		}
		return true
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if !consume() {
				return nil, errCaptureInvalid
			}
			event = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errCaptureInvalid
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			event = value
		case "id", "retry":
		default:
			return nil, errCaptureInvalid
		}
	}
	if scanner.Err() != nil || len(data) != 0 || !terminal || len(models) == 0 {
		return nil, errCaptureInvalid
	}
	return models, nil
}
