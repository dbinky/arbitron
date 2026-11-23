// Package wsclient is the runner's WebSocket connection to the server's
// runner endpoint, speaking runnerproto.
package wsclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
)

// ConnectPath is the server's runner WebSocket endpoint.
const ConnectPath = "/api/v1/runners/connect"

// Options configures the client. RunnerKey travels only in the
// Authorization header.
type Options struct {
	ServerURL  string
	RunnerKey  string
	Headers    http.Header
	HTTPClient *http.Client
}

// Client implements runneragent.Connection.
type Client struct {
	endpoint   string
	key        string
	headers    http.Header
	httpClient *http.Client
}

var _ runneragent.Connection = (*Client)(nil)

// New validates o and returns a client.
func New(o Options) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(o.ServerURL), "/")
	if base == "" {
		return nil, errors.New("wsclient: server URL is required")
	}
	if !strings.HasPrefix(o.RunnerKey, "ark_") {
		return nil, errors.New("wsclient: runner key must be an ark_ key")
	}
	for name := range o.Headers {
		if c := http.CanonicalHeaderKey(name); c == "Authorization" || c == "Host" {
			return nil, fmt.Errorf("wsclient: extra headers may not set %s", c)
		}
	}
	return &Client{endpoint: base + ConnectPath, key: o.RunnerKey, headers: o.Headers.Clone(), httpClient: o.HTTPClient}, nil
}

// Dial opens one session.
func (c *Client) Dial(ctx context.Context) (runneragent.Session, error) {
	h := c.headers.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Authorization", "Runner "+c.key)
	conn, resp, err := websocket.Dial(ctx, c.endpoint, &websocket.DialOptions{HTTPHeader: h, HTTPClient: c.httpClient})
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return nil, &runneragent.RefusedError{Code: "unauthorized", Reason: fmt.Sprintf("server refused the runner key (HTTP %d)", resp.StatusCode)}
		}
		return nil, fmt.Errorf("wsclient: dial %s: %w", c.endpoint, err)
	}
	conn.SetReadLimit(runnerproto.MaxFrameBytes)
	return &session{conn: conn}, nil
}

type session struct {
	conn *websocket.Conn
	wmu  sync.Mutex
}

func (s *session) Send(ctx context.Context, m runneragent.Outbound) error {
	env, err := toEnvelope(m)
	if err != nil {
		return err
	}
	b, err := runnerproto.Encode(env)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.conn.Write(ctx, websocket.MessageText, b)
}

func (s *session) Recv(ctx context.Context) (runneragent.Inbound, error) {
	typ, b, err := s.conn.Read(ctx)
	if err != nil {
		return runneragent.Inbound{}, err
	}
	if typ != websocket.MessageText {
		return runneragent.Inbound{}, fmt.Errorf("%w: binary frame", runnerproto.ErrMalformed)
	}
	env, err := runnerproto.Decode(b)
	if err != nil {
		return runneragent.Inbound{}, err
	}
	return fromEnvelope(env)
}

func (s *session) Close() error {
	return s.conn.Close(websocket.StatusNormalClosure, "runner closing")
}
