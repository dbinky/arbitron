package renderreview

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	surfacePage   = "render.html"
	jobPathPrefix = "/__render/jobs/"
)

var jobTokenRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// assetServer serves static assets and one-shot jobs to local browser tabs.
// The exact Host check prevents DNS rebinding to the loopback listener.
type assetServer struct {
	assets fs.FS
	static http.Handler
	srv    *http.Server
	host   string
	mu     sync.Mutex
	jobs   map[string][]byte
}

func newAssetServer(assets fs.FS) (*assetServer, error) {
	if assets == nil {
		return nil, errors.New("renderreview: asset server needs the dashboard asset filesystem")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("renderreview: listen on loopback: %w", err)
	}
	s := &assetServer{assets: assets, static: http.FileServerFS(assets), host: ln.Addr().String(), jobs: make(map[string][]byte)}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *assetServer) register(doc []byte) (token string, release func()) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf) // Go 1.24+ makes entropy failure fatal.
	token = hex.EncodeToString(buf)
	s.mu.Lock()
	s.jobs[token] = bytes.Clone(doc)
	s.mu.Unlock()
	return token, func() { s.mu.Lock(); delete(s.jobs, token); s.mu.Unlock() }
}
func (s *assetServer) pending() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.jobs) }
func (s *assetServer) pageURL(token string) string {
	return "http://" + s.host + "/" + surfacePage + "?job=" + token
}

func (s *assetServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.host {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, jobPathPrefix) {
		s.serveJob(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	info, err := fs.Stat(s.assets, name)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.static.ServeHTTP(w, r)
}
func (s *assetServer) serveJob(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, jobPathPrefix), ".json")
	if !ok || !jobTokenRE.MatchString(token) {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	doc, found := s.jobs[token]
	if found && r.Method == http.MethodGet {
		delete(s.jobs, token)
	}
	s.mu.Unlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		_, _ = w.Write(doc)
	}
}
func (s *assetServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		_ = s.srv.Close()
	}
}
