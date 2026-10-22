package renderreview

import (
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"render.html":         {Data: []byte("<!DOCTYPE html><title>surface</title>")},
		"index.html":          {Data: []byte("<!DOCTYPE html><title>spa</title>")},
		"assets/render-1.js":  {Data: []byte("console.log('render')")},
		"assets/render-1.css": {Data: []byte("body{}")},
	}
}
func startAssetServer(t *testing.T) *assetServer {
	t.Helper()
	s, err := newAssetServer(testAssets())
	require.NoError(t, err)
	t.Cleanup(s.close)
	return s
}
func get(t *testing.T, s *assetServer, path string) (int, string, http.Header) {
	t.Helper()
	res, err := http.Get("http://" + s.host + path)
	require.NoError(t, err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, string(body), res.Header
}
func TestAssetServer_BindsLoopbackOnly(t *testing.T) {
	s := startAssetServer(t)
	host, port, err := net.SplitHostPort(s.host)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
	assert.True(t, net.ParseIP(host).IsLoopback())
	assert.NotEqual(t, "0", port)
	assert.Equal(t, "http://"+s.host+"/render.html?job=abc", s.pageURL("abc"))
}
func TestAssetServer_ServesEmbeddedFiles(t *testing.T) {
	s := startAssetServer(t)
	code, body, hdr := get(t, s, "/render.html")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "<title>surface</title>")
	assert.Equal(t, "no-store", hdr.Get("Cache-Control"))
	code, body, _ = get(t, s, "/assets/render-1.js")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "console.log('render')", body)
	for _, path := range []string{"/assets/missing.js", "/assets/", "/", "/assets"} {
		code, _, _ = get(t, s, path)
		assert.Equal(t, http.StatusNotFound, code, path)
	}
}
func TestAssetServer_JobIsOneShot(t *testing.T) {
	s := startAssetServer(t)
	token, release := s.register([]byte(`{"mode":"measure"}`))
	defer release()
	assert.Equal(t, 1, s.pending())
	code, body, hdr := get(t, s, "/__render/jobs/"+token+".json")
	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"mode":"measure"}`, body)
	assert.Equal(t, "application/json", hdr.Get("Content-Type"))
	assert.Equal(t, "no-store", hdr.Get("Cache-Control"))
	assert.Equal(t, 0, s.pending())
	code, _, _ = get(t, s, "/__render/jobs/"+token+".json")
	assert.Equal(t, http.StatusNotFound, code)
	release()
	assert.Equal(t, 0, s.pending())
}
func TestAssetServer_ReleasedJobIsGone(t *testing.T) {
	s := startAssetServer(t)
	token, release := s.register([]byte(`{}`))
	release()
	assert.Equal(t, 0, s.pending())
	code, _, _ := get(t, s, "/__render/jobs/"+token+".json")
	assert.Equal(t, http.StatusNotFound, code)
}
func TestAssetServer_UnknownOrMalformedTokenIs404(t *testing.T) {
	s := startAssetServer(t)
	_, release := s.register([]byte(`{}`))
	defer release()
	for _, path := range []string{
		"/__render/jobs/0123456789abcdef0123456789abcdef.json",
		"/__render/jobs/../render.html", "/__render/jobs/ABCDEF.json",
		"/__render/jobs/0123456789abcdef0123456789abcdef",
	} {
		code, _, _ := get(t, s, path)
		assert.Equal(t, http.StatusNotFound, code, path)
	}
	assert.Equal(t, 1, s.pending())
}
func TestAssetServer_TokensAreRandomHex(t *testing.T) {
	s := startAssetServer(t)
	re := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		token, release := s.register([]byte(`{}`))
		assert.Regexp(t, re, token)
		assert.False(t, seen[token])
		seen[token] = true
		release()
	}
}
func TestAssetServer_RejectsForeignHostAndWrites(t *testing.T) {
	s := startAssetServer(t)
	req, err := http.NewRequest(http.MethodGet, "http://"+s.host+"/render.html", nil)
	require.NoError(t, err)
	req.Host = "evil.example:80"
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusForbidden, res.StatusCode)
	res, err = http.Post("http://"+s.host+"/render.html", "text/plain", strings.NewReader("x"))
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)
}
func TestAssetServer_HeadDoesNotConsumeJob(t *testing.T) {
	s := startAssetServer(t)
	token, release := s.register([]byte(`{"mode":"measure"}`))
	defer release()
	res, err := http.Head("http://" + s.host + "/__render/jobs/" + token + ".json")
	require.NoError(t, err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	assert.Empty(t, body)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, 1, s.pending())
}
func TestAssetServer_CloseStopsListening(t *testing.T) {
	s, err := newAssetServer(testAssets())
	require.NoError(t, err)
	s.close()
	s.close()
	_, err = http.Get("http://" + s.host + "/render.html")
	assert.Error(t, err)
}
func TestAssetServer_NeedsAssets(t *testing.T) {
	_, err := newAssetServer(nil)
	assert.ErrorContains(t, err, "asset filesystem")
}
