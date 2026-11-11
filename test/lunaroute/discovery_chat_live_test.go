package lunaroute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

const (
	discoveryChatTokens     = 1024
	discoveryNegativeTokens = 8
)

var errDiscoveryEvidence = errors.New("discovery/chat evidence incomplete; review contract before release")

// Retain only string code/type fields, consistently with the production bare
// and wrapped decoder. Malformed, conflicting or unsafe input is not evidence.
func errorCodes(body []byte) map[string]string {
	out := map[string]string{}
	if len(body) == 0 || len(body) > maxGatewayResponseBytes || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(body))) {
		return out
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return out
	}
	object := envelope
	if raw, present := envelope["error"]; present {
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return out
		}
	}
	var code, typ string
	if raw, present := object["code"]; present {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &code) != nil {
			return out
		}
	}
	if raw, present := object["type"]; present {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &typ) != nil {
			return out
		}
	}
	if code != provider.LunaRouteErrorCode(provider.LunaRouteChat, string(body)) {
		return out
	}
	for _, value := range []string{code, typ} {
		if value != "" && (!safeCaptureModel(value) || strings.ContainsAny(value, "/:.")) {
			return out
		}
	}
	if code != "" {
		out["error.code"] = code
	}
	if typ != "" {
		out["error.type"] = typ
	}
	return out
}

func classifiedAs(err error) string {
	labels := []string{"credentials_rejected", "model_unavailable", "busy", "service_failure", "cause_unknown", "request_refused"}
	for i, sentinel := range provider.LunaRouteGatewaySentinels() {
		if errors.Is(err, sentinel) {
			return labels[i]
		}
	}
	if err == nil {
		return "success"
	}
	return "unclassified"
}

type rawErrorObservation struct {
	ContentType         string            `json:"content_type"`
	Model               string            `json:"model,omitempty"`
	AuthHeaderNames     []string          `json:"auth_header_names"`
	AuthMatchesSelected bool              `json:"auth_matches_selected"`
	Case                string            `json:"case"`
	Path                string            `json:"path"`
	Status              int               `json:"status"`
	BodyShape           map[string]string `json:"body_shape"`
	Codes               map[string]string `json:"codes"`
	Classified          string            `json:"classified_as"`
}
type discoveryChatResult struct {
	Model         string `json:"model"`
	ResponseModel string `json:"response_model"`
	Completed     bool   `json:"completed"`
	TokensIn      int    `json:"tokens_in"`
	TokensOut     int    `json:"tokens_out"`
	LatencyMS     int64  `json:"latency_ms"`
	Scope         string `json:"scope"`
}
type discoveryChatObservation struct {
	RecordedAt      string                `json:"recorded_at"`
	Root            string                `json:"root"`
	ListingModelIDs []string              `json:"listing_model_ids"`
	ListingShape    map[string]string     `json:"listing_shape"`
	Chat            discoveryChatResult   `json:"chat"`
	Errors          []rawErrorObservation `json:"error_bodies"`
	Inference       []capturedInference   `json:"inference"`
	UnavailableCase string                `json:"unavailable_case"`
}

// Schema keys are untrusted too. Retain identifier-like field names only;
// values are discarded by shape, and unsafe names invalidate the observation.
func validateDiscoverySchema(raw []byte) error {
	schema := shape(raw)
	if schema == nil {
		return errDiscoveryEvidence
	}
	for name := range schema {
		if !safeCaptureModel(name) || strings.ContainsAny(name, "/:.-") {
			return errDiscoveryEvidence
		}
	}
	return nil
}

// The production adapters read the exact response also inspected here. Only
// schema/model/terminal fields survive; bodies and auth values stay in memory.
// A read, cap or schema failure prevents publication of the entire experiment.
type discoveryTransport struct {
	upstream     http.RoundTripper
	key, runID   string
	listingIDs   []string
	listingShape map[string]string
	lastError    rawErrorObservation
	records      []capturedInference
	count        int
}

func (c *discoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.count++
	started := time.Now().UTC()
	if r.URL.Scheme != "https" || r.URL.Host != captureHost || r.URL.RawQuery != "" || r.URL.User != nil || r.URL.RawPath != "" || !slices.Equal(captureAuthNames(r.Header), []string{"Authorization"}) {
		return nil, errDiscoveryEvidence
	}
	auth := r.Header.Get("Authorization")
	selectedAuth := len(r.Header.Values("Authorization")) == 1 && auth == "Bearer "+c.key
	invalidAuth := len(r.Header.Values("Authorization")) == 1 && auth == "Bearer "+invalidGatewayKey
	if !selectedAuth && !invalidAuth {
		return nil, errDiscoveryEvidence
	}
	listing := r.Method == "GET" && r.URL.Path == "/v1/models"
	chat := r.Method == "POST" && r.URL.Path == "/v1/chat/completions"
	if !listing && !chat || invalidAuth && !listing {
		return nil, errDiscoveryEvidence
	}
	var requested string
	if chat {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayResponseBytes+1))
		closeErr := r.Body.Close()
		var payload struct {
			Model string `json:"model"`
		}
		if err != nil || closeErr != nil || len(raw) > maxGatewayResponseBytes || json.Unmarshal(raw, &payload) != nil || !safeCaptureModel(payload.Model) {
			return nil, errDiscoveryEvidence
		}
		requested = payload.Model
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	res, err := c.upstream.RoundTrip(r)
	if err != nil {
		return nil, errDiscoveryEvidence
	}
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, maxGatewayResponseBytes+1))
	closeErr := res.Body.Close()
	if readErr != nil || closeErr != nil || len(raw) > maxGatewayResponseBytes || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(raw))) {
		return nil, errDiscoveryEvidence
	}
	if validateDiscoverySchema(raw) != nil {
		return nil, errDiscoveryEvidence
	}
	schema := shape(raw)
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if listing {
			ids, err := listingIDs(raw)
			if err != nil {
				return nil, errDiscoveryEvidence
			}
			c.listingIDs = make([]string, 0, len(ids))
			for id := range ids {
				if !safeCaptureModel(id) {
					return nil, errDiscoveryEvidence
				}
				c.listingIDs = append(c.listingIDs, id)
			}
			slices.Sort(c.listingIDs)
			c.listingShape = schema
		} else {
			reported, err := extractCapturedResponse(r.URL.Path, raw, false)
			if err != nil {
				return nil, errDiscoveryEvidence
			}
			c.records = append(c.records, capturedInference{
				RunID: c.runID, Backend: "chat", Connection: "selected-connection", Category: "main",
				RequestID: "request-" + strconv.Itoa(c.count), Method: r.Method, Host: r.URL.Host, Path: r.URL.Path,
				RequestedModel: requested, ReportedModels: reported,
				AuthMatchesSelected: selectedAuth, AuthHeaderNames: captureAuthNames(r.Header),
				Completed: true, StartedAt: started, FinishedAt: time.Now().UTC(),
			})
		}
	} else {
		contentType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
		if contentType != "application/json" {
			contentType = "unknown"
		}
		c.lastError = rawErrorObservation{Path: r.URL.Path, Status: res.StatusCode, BodyShape: schema, Codes: errorCodes(raw), Model: requested, ContentType: contentType, AuthHeaderNames: captureAuthNames(r.Header), AuthMatchesSelected: selectedAuth}
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	return res, nil
}

// No destination parameter: both real factories enforce the hosted endpoint.
// The injected transport is solely the deterministic offline gateway seam.
func collectDiscoveryChat(ctx context.Context, upstream http.RoundTripper, key, model, unavailable string, rules provider.CapabilityRules) (discoveryChatObservation, error) {
	empty := discoveryChatObservation{}
	if upstream == nil || !provider.ValidLunaRouteKey(key) {
		return empty, errDiscoveryEvidence
	}
	ids := []string{model}
	if unavailable != "" {
		ids = append(ids, unavailable)
	}
	for _, id := range ids {
		resolved, err := modelidentity.NewVersionResolver().SpecificVersion(ctx, modelidentity.VersionRequest{RouteProvider: "lunaroute", Model: id})
		if err != nil || resolved != id {
			return empty, errDiscoveryEvidence
		}
	}
	runID := "discovery-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	capture := &discoveryTransport{upstream: upstream, key: key, runID: runID}
	client := liveClient()
	client.Transport = capture
	binding := func(credential, selected string) provider.ResolvedBinding {
		return provider.GuardLunaRouteReveal(provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "lunaroute", BaseURL: provider.LunaRouteHostedRoot, Model: selected, ConnectionRef: "selected-connection", RevealKey: func(context.Context) (string, error) { return credential, nil }})
	}
	probes := adaptprobe.NewProbeFactory(adaptprobe.Deps{HTTPClient: client, Timeout: time.Minute, MaxResponseBytes: maxGatewayResponseBytes})
	probe, err := probes.ProbeFor(binding(key, ""))
	if err != nil {
		return empty, errDiscoveryEvidence
	}
	listing, err := probe.Probe(ctx, binding(key, ""))
	if err != nil || !listing.Reachable || !slices.Contains(listing.Models, model) || !slices.Contains(capture.listingIDs, model) {
		return empty, errDiscoveryEvidence
	}
	obs := discoveryChatObservation{RecordedAt: time.Now().UTC().Format(time.RFC3339), Root: hostedRoot, ListingModelIDs: slices.Clone(capture.listingIDs), ListingShape: capture.listingShape, UnavailableCase: "not supplied; deterministic root fixtures cover unavailable models"}
	factory := providersadapter.NewFactory(providersadapter.Deps{HTTPClient: client, HTTPTimeout: captureTimeout, MaxResponseBytes: maxGatewayResponseBytes, CapabilityRules: rules})
	chat, err := factory.ChatProviderFor(binding(key, model))
	if err != nil {
		return empty, errDiscoveryEvidence
	}
	started := time.Now()
	response, err := chat.Chat(ctx, engine.ChatRequest{Prompt: gatewayPrompt, MaxTokens: discoveryChatTokens})
	if err != nil {
		return empty, errDiscoveryEvidence
	}
	output, ok := response.Output.(string)
	if !ok || !strings.Contains(strings.ToLower(output), "pong") || validateCapturedInference(capture.records, model, []string{"main"}) != nil {
		return empty, errDiscoveryEvidence
	}
	obs.Chat = discoveryChatResult{Model: model, ResponseModel: response.Model, Completed: true, TokensIn: response.TokensIn, TokensOut: response.TokensOut, LatencyMS: time.Since(started).Milliseconds(), Scope: "one completed chat task only; proves no static limits or agent backend"}
	obs.Inference = slices.Clone(capture.records)
	_, badErr := probe.Probe(ctx, binding(invalidGatewayKey, ""))
	if !errors.Is(badErr, provider.ErrLunaRouteCredentialsRejected) || capture.lastError.Status != 401 || capture.lastError.Codes["error.code"] != "INVALID_API_KEY" {
		return empty, errDiscoveryEvidence
	}
	bad := capture.lastError
	bad.Case, bad.Classified = "invalid key", classifiedAs(badErr)
	obs.Errors = append(obs.Errors, bad)
	if unavailable != "" {
		// Availability must be fresh and successful, not a prior local grammar error.
		fresh, err := probe.Probe(ctx, binding(key, ""))
		if err != nil || !fresh.Reachable || slices.Contains(fresh.Models, unavailable) || slices.Contains(capture.listingIDs, unavailable) {
			return empty, errDiscoveryEvidence
		}
		negative, err := factory.ChatProviderFor(binding(key, unavailable))
		if err != nil {
			return empty, errDiscoveryEvidence
		}
		capture.lastError = rawErrorObservation{}
		_, inferenceErr := negative.Chat(ctx, engine.ChatRequest{Prompt: gatewayPrompt, MaxTokens: discoveryNegativeTokens})
		observed := capture.lastError
		if inferenceErr == nil || observed.Status < 300 || observed.Path != "/v1/chat/completions" || classifiedAs(inferenceErr) == "unclassified" {
			return empty, errDiscoveryEvidence
		}
		observed.Case, observed.Classified = "unavailable model", classifiedAs(inferenceErr)
		obs.Errors = append(obs.Errors, observed)
		obs.UnavailableCase = "reviewed id absent from fresh listing; actual non-success inference observed"
	}
	return obs, nil
}

func TestLiveDiscoveryAndChat(t *testing.T) {
	key := liveKey(t)
	model := os.Getenv("ARBITRON_LUNAROUTE_E2E_CHAT_MODEL")
	if model == "" {
		if os.Getenv("ARBITRON_LUNAROUTE_RELEASE_GATE") == "1" {
			t.Fatal("explicit ARBITRON_LUNAROUTE_E2E_CHAT_MODEL required; release acceptance remains pending")
		}
		t.Skip("choose an explicit reviewed ARBITRON_LUNAROUTE_E2E_CHAT_MODEL; release acceptance remains pending")
	}
	rules, err := adapterconfig.LoadCapabilityRules(os.Getenv("ARBITRON_LUNAROUTE_E2E_CAPABILITIES"))
	if err != nil {
		t.Fatal("cannot load live capability table; release acceptance remains pending")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// Normal TLS verification, no ambient proxy and no redirects. Production
	// adapters, not raw duplicate probes, produce every observed exchange.
	transport := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 30 * time.Second, ResponseHeaderTimeout: captureTimeout, MaxResponseHeaderBytes: captureHeaderLimit}
	defer transport.CloseIdleConnections()
	obs, err := collectDiscoveryChat(ctx, transport, key, model, os.Getenv("ARBITRON_LUNAROUTE_E2E_UNAVAILABLE_MODEL"), rules)
	if err != nil {
		t.Fatal("live discovery/chat failed; no verified observation or support fact written; review LR-01/LR-04 and apply the decision's on-failure rule")
	}
	path := writeObservation(t, key, "lr04-discovery-chat.json", obs)
	t.Log("credential-free observation: " + path + "; source review and scoped production population required before verification")
}
