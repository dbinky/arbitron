// Package transfer is the runner side of /api/v1/runners/transfers/*.
package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/domain/runner"
)

const transfersPath = "/api/v1/runners/transfers"

// PushCommit identifies the parts and hash of the entire delta stream.
type PushCommit struct {
	Parts  int    `json:"parts"`
	SHA256 string `json:"sha256"`
}

// Transfer is the transport required by workspace sync and the bundle cache.
type Transfer interface {
	Manifest(context.Context, string) (runner.Manifest, error)
	FetchFile(context.Context, string, string) (io.ReadCloser, error)
	PutPart(context.Context, string, int, []byte) error
	CommitPush(context.Context, string, PushCommit) (runner.CheckpointRef, error)
	FetchBundle(context.Context, string, string) (io.ReadCloser, error)
}

// Client sends runner credentials in headers, never in transfer URLs.
type Client struct {
	base  string
	key   string
	hc    *http.Client
	extra http.Header
}

var _ Transfer = (*Client)(nil)

func NewClient(serverURL, runnerKey string, hc *http.Client, extraHeaders http.Header) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimRight(serverURL, "/") + transfersPath, key: runnerKey, hc: hc, extra: extraHeaders.Clone()}
}

func (c *Client) do(ctx context.Context, method, path, token string, body []byte, hdr map[string]string) (*http.Response, error) {
	route := strings.SplitN(path, "?", 2)[0]
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	for k, values := range c.extra {
		for _, value := range values {
			req.Header.Add(k, value)
		}
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Runner "+c.key)
	req.Header.Set("X-Arbitron-Transfer", token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("transfer %s %s: %w", method, route, err)
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("transfer %s %s: status %d: %s", method, route, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

func (c *Client) Manifest(ctx context.Context, token string) (runner.Manifest, error) {
	resp, err := c.do(ctx, http.MethodGet, "/manifest", token, nil, nil)
	if err != nil {
		return runner.Manifest{}, err
	}
	defer resp.Body.Close()
	var m runner.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return runner.Manifest{}, fmt.Errorf("transfer manifest: %w", err)
	}
	return m, nil
}

func (c *Client) FetchFile(ctx context.Context, token, path string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/files?path="+url.QueryEscape(path), token, nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) PutPart(ctx context.Context, token string, index int, data []byte) error {
	sum := sha256.Sum256(data)
	resp, err := c.do(ctx, http.MethodPut, "/push/"+strconv.Itoa(index), token, data, map[string]string{"X-Part-SHA256": hex.EncodeToString(sum[:]), "Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *Client) CommitPush(ctx context.Context, token string, pc PushCommit) (runner.CheckpointRef, error) {
	body, err := json.Marshal(pc)
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/push/commit", token, body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	defer resp.Body.Close()
	var ref runner.CheckpointRef
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		return runner.CheckpointRef{}, fmt.Errorf("transfer commit: %w", err)
	}
	return ref, nil
}

func (c *Client) FetchBundle(ctx context.Context, token, hash string) (io.ReadCloser, error) {
	if !runner.ValidBundleHash(hash) {
		return nil, errors.New("transfer bundle: malformed hash")
	}
	resp, err := c.do(ctx, http.MethodGet, "/bundles/"+hash, token, nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
