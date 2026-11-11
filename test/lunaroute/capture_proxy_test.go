package lunaroute_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const captureFixtureKey = "lr_capture_private_key"

func fixtureCapture(t *testing.T, handler http.HandlerFunc, limits captureLimits) *inferenceCapture {
	t.Helper()
	upstream := httptest.NewTLSServer(handler)
	t.Cleanup(upstream.Close)
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	// Verify the fixture certificate's real IP SAN while routing the canonical
	// hosted request to the private upstream; never disable TLS verification.
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	address := upstream.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	c, err := newInferenceCapture("run-one", "codex", "connection-one", captureFixtureKey, transport, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}
func captureClient(t *testing.T, c *inferenceCapture, trust bool) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	if trust {
		raw, err := os.ReadFile(c.caPath)
		if err != nil || !roots.AppendCertsFromPEM(raw) {
			t.Fatal("cannot load capture CA")
		}
	}
	proxy, err := url.Parse(c.proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func sendCapture(t *testing.T, client *http.Client, host, key, extra string) error {
	t.Helper()
	req, err := http.NewRequest("POST", "https://"+host+"/v1/responses", strings.NewReader(`{"model":"glm-5.3","input":"private-nonce","stream":false}`))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if extra != "" {
		req.Header.Set(extra, "foreign-key")
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, err = io.Copy(io.Discard, res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		return errCaptureInvalid
	}
	return nil
}
func captureUpstream(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model, Input string }
		err := json.NewDecoder(r.Body).Decode(&body)
		if r.Host != "gw.lunaroute.com" || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey || err != nil || body.Model != "glm-5.3" || body.Input != "private-nonce" {
			t.Error("upstream did not receive exact credential-bearing request")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"glm-5.3","status":"completed","output":[{"content":"private-response"}]}`)
	}
}
func TestCaptureHostedRequestResponse(t *testing.T) {
	c := fixtureCapture(t, captureUpstream(t), captureLimits{})
	if err := sendCapture(t, captureClient(t, c, true), "gw.lunaroute.com", captureFixtureKey, ""); err != nil {
		t.Fatal("request failed")
	}
	records := c.Records()
	if len(records) != 1 || validateCapturedInference(records, "glm-5.3", []string{"main"}) != nil {
		t.Fatal("actual request/response not captured")
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), captureFixtureKey) || strings.Contains(string(raw), "private-nonce") || strings.Contains(string(raw), "private-response") {
		t.Fatal("raw content retained")
	}
	root := t.TempDir()
	rel, err := saveObservation(root, captureFixtureKey, "capture.json", records)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil || string(persisted) == "" {
		t.Fatal("observation not persisted")
	}
	var sink []capturedInference
	if json.Unmarshal(persisted, &sink) != nil || len(sink) != 1 || sink[0].RequestedModel != "glm-5.3" || !sink[0].Completed {
		t.Fatal("persisted sink lost routing evidence")
	}
}
func TestCaptureRejectsWrongRouteOrAuth(t *testing.T) {
	for _, tc := range []struct{ name, host, key, extra string }{{"host", "foreign.example", captureFixtureKey, ""}, {"key", "gw.lunaroute.com", "lr_wrong", ""}, {"extra", "gw.lunaroute.com", captureFixtureKey, "x-api-key"}, {"other-auth", "gw.lunaroute.com", captureFixtureKey, "LUNAROUTE-API-KEY"}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }, captureLimits{})
			if sendCapture(t, captureClient(t, c, true), tc.host, tc.key, tc.extra) == nil {
				t.Fatal("invalid request succeeded")
			}
			if calls.Load() != 0 || validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) == nil {
				t.Fatal("invalid request forwarded or accepted")
			}
		})
	}
}
func TestCaptureReadTLSAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		limits     captureLimits
		status     int
		delay      time.Duration
	}{{"malformed-sse", "data: {broken}\n\n", captureLimits{}, 200, 0}, {"truncated", "data: {\"type\":\"response.created\",\"response\":{\"model\":\"glm-5.3\"}}\n\n", captureLimits{}, 200, 0}, {"cap", strings.Repeat("x", 100), captureLimits{BodyBytes: 32}, 200, 0}, {"redirect", "", captureLimits{}, 307, 0}, {"timeout", "", captureLimits{Timeout: 30 * time.Millisecond}, 200, 150 * time.Millisecond}} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(tc.delay)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Location", "https://foreign.example")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}, tc.limits)
			_ = sendCapture(t, captureClient(t, c, true), "gw.lunaroute.com", captureFixtureKey, "")
			if validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) == nil {
				t.Fatal("failed response verified")
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(c.dir); !os.IsNotExist(err) {
				t.Fatal("capture private directory not removed")
			}
		})
	}
	c := fixtureCapture(t, captureUpstream(t), captureLimits{})
	if sendCapture(t, captureClient(t, c, false), "gw.lunaroute.com", captureFixtureKey, "") == nil {
		t.Fatal("untrusted CA accepted")
	}
	if validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) == nil {
		t.Fatal("TLS outage verified")
	}
	c2 := fixtureCapture(t, captureUpstream(t), captureLimits{Records: 1})
	client := captureClient(t, c2, true)
	if sendCapture(t, client, "gw.lunaroute.com", captureFixtureKey, "") != nil {
		t.Fatal("first request failed")
	}
	_ = sendCapture(t, client, "gw.lunaroute.com", captureFixtureKey, "")
	if validateCapturedInference(c2.Records(), "glm-5.3", []string{"main"}) == nil {
		t.Fatal("record cap silently dropped traffic")
	}
}
func TestCaptureWrapperPreservesRun(t *testing.T) {
	c := fixtureCapture(t, captureUpstream(t), captureLimits{})
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := c.WrapBinary(t, binary)
	for _, p := range []string{c.dir, wrapper, c.caPath} {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm()&0077 != 0 {
			t.Fatal("private capture files not owner-only")
		}
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, wrapper, "-test.run=^TestCaptureWrappedProcess$", "--", "nonce-argv")
	command.Dir = workspace
	command.Stdin = strings.NewReader("nonce-stdin")
	command.Env = append(os.Environ(), "ARBITRON_CAPTURE_CHILD=1", "CAPTURE_EXPECTED_CWD="+workspace, "LUNAROUTE_API_KEY="+captureFixtureKey, "NO_PROXY=*", "no_proxy=*", "HTTPS_PROXY=http://127.0.0.1:1", "NODE_EXTRA_CA_CERTS=/missing-conflicting-ca")
	if err := command.Run(); err != nil {
		t.Fatalf("wrapper failed to preserve launch/trust/request: %v", err)
	}
	if validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) != nil {
		t.Fatal("wrapper bypassed observation")
	}
	ca := c.caPath
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ca); !os.IsNotExist(err) {
		t.Fatal("CA survived close")
	}
	// A launcher failure cannot fabricate traffic.
	empty := fixtureCapture(t, captureUpstream(t), captureLimits{})
	failed := empty.WrapBinary(t, "/nonexistent/arbitron-capture-binary")
	if exec.Command(failed).Run() == nil || validateCapturedInference(empty.Records(), "glm-5.3", []string{"main"}) == nil {
		t.Fatal("failed launcher verified")
	}
}
func TestCaptureClosePreservesDrainedKeepAlive(t *testing.T) {
	c := fixtureCapture(t, captureUpstream(t), captureLimits{})
	client := captureClient(t, c, true)
	client.Transport.(*http.Transport).DisableKeepAlives = false
	if sendCapture(t, client, captureHost, captureFixtureKey, "") != nil {
		t.Fatal("keep-alive request failed")
	}
	if validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) != nil {
		t.Fatal("keep-alive exchange not captured")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) != nil {
		t.Fatal("intentional cleanup invalidated drained evidence")
	}
}

func TestCaptureRecordCapReservesBeforeForwarding(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"glm-5.3","status":"completed"}`)
	}, captureLimits{Records: 1})
	client := captureClient(t, c, true)
	firstDone := make(chan struct{})
	go func() { defer close(firstDone); _ = sendCapture(t, client, captureHost, captureFixtureKey, "") }()
	<-entered
	secondDone := make(chan struct{})
	go func() { defer close(secondDone); _ = sendCapture(t, client, captureHost, captureFixtureKey, "") }()
	select {
	case <-entered:
		t.Error("record cap failed to reserve before concurrent forwarding")
	case <-secondDone:
	case <-time.After(time.Second):
		t.Error("over-cap request was not refused promptly")
	}
	close(release)
	<-firstDone
	<-secondDone
	if validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) == nil {
		t.Fatal("dropped over-cap request verified")
	}
}

func TestCaptureBoundsHeadersAndRequestBodies(t *testing.T) {
	for _, part := range []string{"headers", "body"} {
		t.Run(part, func(t *testing.T) {
			var calls atomic.Int32
			c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }, captureLimits{BodyBytes: 128})
			body := `{"model":"glm-5.3"}`
			if part == "body" {
				body = `{"model":"glm-5.3","input":"` + strings.Repeat("x", 256) + `"}`
			}
			req, err := http.NewRequest("POST", hostedRoot+"/v1/responses", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+captureFixtureKey)
			if part == "headers" {
				req.Header.Set("X-Padding", strings.Repeat("x", (64<<10)+1))
			}
			res, _ := captureClient(t, c, true).Do(req)
			if res != nil {
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
			if calls.Load() != 0 {
				t.Fatal("unbounded input forwarded upstream")
			}
			if validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) == nil {
				t.Fatal("oversized input accepted")
			}
		})
	}
}

func TestCaptureStreamsBeforeTerminal(t *testing.T) {
	firstRead := make(chan struct{})
	c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model  string
			Stream bool
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != "glm-5.3" || !payload.Stream || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey {
			t.Error("stream request lost model/credential/settings")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"model\":\"glm-5.3\",\"status\":\"in_progress\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-firstRead:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"glm-5.3\",\"status\":\"completed\"}}\n\n")
	}, captureLimits{Timeout: time.Second})
	client := captureClient(t, c, true)
	req, err := http.NewRequest("POST", hostedRoot+"/v1/responses", strings.NewReader(`{"model":"glm-5.3","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+captureFixtureKey)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal("stream did not begin")
	}
	one := make([]byte, 1)
	_, err = io.ReadFull(res.Body, one)
	close(firstRead)
	if err != nil {
		res.Body.Close()
		t.Fatal("response buffered until terminal")
	}
	_, err = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if err != nil || validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) != nil {
		t.Fatal("stream did not complete with observed model")
	}
}

func TestCaptureUpstreamReadAndTLSFailure(t *testing.T) {
	for _, fault := range []string{"read", "tls", "wrong-model"} {
		t.Run(fault, func(t *testing.T) {
			c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if fault == "read" {
					w.Header().Set("Content-Length", "1000")
				}
				io.WriteString(w, `{"model":"gpt-6.1-sol","status":"completed"}`)
			}, captureLimits{})
			if fault == "tls" {
				c.transport.TLSClientConfig.RootCAs = x509.NewCertPool()
			}
			_ = sendCapture(t, captureClient(t, c, true), captureHost, captureFixtureKey, "")
			if validateCapturedInference(c.Records(), "glm-5.3", []string{"main"}) == nil {
				t.Fatal("failed or wrong-model exchange verified")
			}
		})
	}
}

func TestCaptureCorrelatesOperationAndIsolatesRuns(t *testing.T) {
	one := fixtureCapture(t, captureUpstream(t), captureLimits{})
	two := fixtureCapture(t, captureUpstream(t), captureLimits{})
	two.runID = "run-two"
	var wg sync.WaitGroup
	for _, c := range []*inferenceCapture{one, two} {
		client := captureClient(t, c, true)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2 {
				if sendCapture(t, client, captureHost, captureFixtureKey, "") != nil {
					t.Error("interleaved request failed")
				}
			}
		}()
	}
	wg.Wait()
	records := one.Records()
	other := two.Records()
	if len(records) != 2 || len(other) != 2 {
		t.Fatal("interleaved records lost")
	}
	op := capturedOperation{RunID: one.runID, Backend: one.backend, ID: "forced-one", Session: "session-one", Category: "compaction", Trigger: "forced compact lifecycle", Source: "fixture lifecycle parser", Version: "fixture-1", StartedAt: records[1].StartedAt.Add(-time.Second), FinishedAt: records[1].FinishedAt.Add(time.Second), RequestIDs: []string{records[1].RequestID}}
	bad := op
	bad.RunID = "stale"
	if one.CorrelateOperation(bad) == nil {
		t.Fatal("stale operation accepted")
	}
	bad = op
	bad.RequestIDs = []string{"unobserved"}
	if one.CorrelateOperation(bad) == nil {
		t.Fatal("unobserved operation accepted")
	}
	if err := one.CorrelateOperation(op); err != nil {
		t.Fatal(err)
	}
	correlated := one.Records()
	if validateCapturedInference(correlated, "glm-5.3", []string{"main", "compaction"}) != nil {
		t.Fatal("observed helper not correlated")
	}
	if validateCapturedInference(append(correlated, other...), "glm-5.3", []string{"main", "compaction"}) == nil {
		t.Fatal("interleaved stale run accepted")
	}
	rel, err := saveObservation(t.TempDir(), captureFixtureKey, "helper.json", correlated)
	if err != nil || rel == "" {
		t.Fatal("correlated helper did not reach observation sink")
	}
	correlated[1].Operation.RequestIDs[0] = "tampered"
	if validateCapturedInference(one.Records(), "glm-5.3", []string{"main", "compaction"}) != nil {
		t.Fatal("Records exposes mutable capture state")
	}
	// Remove the proxy/trust injection from the exact same child launch. The
	// child refuses missing trust before sending any credential to the network.
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestCaptureWrappedProcess$", "--", "nonce-argv")
	cmd.Dir = workspace
	cmd.Stdin = strings.NewReader("nonce-stdin")
	cmd.Env = append(os.Environ(), "ARBITRON_CAPTURE_CHILD=1", "CAPTURE_EXPECTED_CWD="+workspace, "LUNAROUTE_API_KEY="+captureFixtureKey, "NO_PROXY=", "no_proxy=", "SSL_CERT_FILE=", "NODE_EXTRA_CA_CERTS=")
	empty := fixtureCapture(t, captureUpstream(t), captureLimits{})
	if cmd.Run() == nil || validateCapturedInference(empty.Records(), "glm-5.3", []string{"main"}) == nil {
		t.Fatal("missing wrapper feed accepted")
	}
}

func TestCaptureWrappedProcess(t *testing.T) {
	if os.Getenv("ARBITRON_CAPTURE_CHILD") != "1" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil || cwd != os.Getenv("CAPTURE_EXPECTED_CWD") {
		os.Exit(10)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil || string(input) != "nonce-stdin" || os.Args[len(os.Args)-1] != "nonce-argv" || os.Getenv("LUNAROUTE_API_KEY") != captureFixtureKey || os.Getenv("NO_PROXY") != "" || os.Getenv("no_proxy") != "" || os.Getenv("NODE_EXTRA_CA_CERTS") != os.Getenv("SSL_CERT_FILE") {
		os.Exit(11)
	}
	// This fixture runtime explicitly honors SSL_CERT_FILE (Go's macOS system
	// verifier does not). Each installed backend must independently prove its
	// own proxy/trust support in its contract experiment.
	roots := x509.NewCertPool()
	ca, readErr := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
	if readErr != nil || !roots.AppendCertsFromPEM(ca) {
		os.Exit(13)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{RootCAs: roots},
	}}
	if sendCapture(t, client, "gw.lunaroute.com", os.Getenv("LUNAROUTE_API_KEY"), "") != nil {
		os.Exit(12)
	}
}
