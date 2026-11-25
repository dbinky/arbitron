package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	// DefaultTransferRequestTimeout replaces the server's short read deadline
	// for one transfer request.
	DefaultTransferRequestTimeout = 30 * time.Minute
	// MaxTransferParts bounds the part index of one push.
	MaxTransferParts = 10000
	// TransferTokenHeader carries the transfer token.
	TransferTokenHeader = "X-Arbitron-Transfer"
	// PartSHA256Header carries the hex SHA-256 of one push part.
	PartSHA256Header = "X-Part-SHA256"
)

// TransferKeyAuthenticator resolves a presented runner key (runner.KeyService).
// It is the Authenticate-only slice of RunnerKeyAuthenticator (P2 T5).
type TransferKeyAuthenticator interface {
	Authenticate(ctx context.Context, plaintext string) (runner.RunnerKey, error)
}

// TransferSessions validates transfer tokens and records push state
// (runner.TransferTokenService).
type TransferSessions interface {
	Session(ctx context.Context, token string, r runner.RunnerID) (runner.TransferSession, error)
	RecordPart(ctx context.Context, token string, index int, sha256Hex string) error
	RecordCommit(ctx context.Context, token, ref string) error
	Sweep(ctx context.Context) (int64, error)
}

// WorkspaceTransfers publishes pushes and progress (runner.WorkspaceSync).
type WorkspaceTransfers interface {
	AcceptPush(ctx context.Context, f runner.Fence, runID uuid.UUID, staged runner.StagedCheckpoint) (runner.CheckpointRef, error)
	ReportProgress(ctx context.Context, f runner.Fence, runID uuid.UUID, done, total int64)
}

// BundleReader opens a script bundle by content hash (runner.BundleStore).
type BundleReader interface {
	Open(ctx context.Context, hash string) (io.ReadCloser, error)
}

// TransferHandlerDeps configures RunnerTransferHandler.
type TransferHandlerDeps struct {
	Keys             TransferKeyAuthenticator
	Tokens           TransferSessions
	Checkpoints      runner.CheckpointStore
	Sync             WorkspaceTransfers
	Bundles          BundleReader
	StagingDir       string
	PartBytes        int64
	StagingRetention time.Duration
	RequestTimeout   time.Duration
	Clock            func() time.Time
}

// RunnerTransferHandler serves /api/v1/runners/transfers/*.
type RunnerTransferHandler struct {
	d         TransferHandlerDeps
	mu        sync.Mutex
	served    map[string]int64 // bytes moved per token hash (progress)
	lastSweep time.Time
}

// NewRunnerTransferHandler panics when a dependency that guards the transfer
// boundary is missing.
func NewRunnerTransferHandler(d TransferHandlerDeps) *RunnerTransferHandler {
	if d.Keys == nil || d.Tokens == nil || d.Checkpoints == nil || d.Sync == nil || d.StagingDir == "" || d.PartBytes <= 0 {
		panic("http: NewRunnerTransferHandler requires Keys, Tokens, Checkpoints, Sync, StagingDir and PartBytes")
	}
	if d.RequestTimeout <= 0 {
		d.RequestTimeout = DefaultTransferRequestTimeout
	}
	if d.StagingRetention <= 0 {
		d.StagingRetention = time.Hour
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &RunnerTransferHandler{d: d, served: map[string]int64{}}
}

// Routes registers the transfer routes on r (mounted at
// /api/v1/runners/transfers, outside the user auth middleware).
func (h *RunnerTransferHandler) Routes(r chi.Router) {
	r.Get("/manifest", h.manifest)
	r.Get("/files", h.file)
	r.Put("/push/{part}", h.putPart)
	r.Post("/push/commit", h.commit)
	r.Get("/bundles/{hash}", h.bundle)
}

func runnerKeyFromRequest(r *http.Request) (string, bool) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Runner") {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
}

func transferDenied(err error) bool {
	return errors.Is(err, runner.ErrTransferTokenInvalid) || errors.Is(err, runner.ErrTransferTokenExpired) ||
		errors.Is(err, runner.ErrTransferScope) || errors.Is(err, runner.ErrFenced) || errors.Is(err, runner.ErrJobNotFound) ||
		errors.Is(err, runner.ErrResourceFenced)
}

// authorize checks the machine-bound runner key, then the transfer token for
// that runner and the route's direction.
func (h *RunnerTransferHandler) authorize(w http.ResponseWriter, r *http.Request, direction string) (runner.TransferSession, string, bool) {
	plain, ok := runnerKeyFromRequest(r)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "runner key required")
		return runner.TransferSession{}, "", false
	}
	key, err := h.d.Keys.Authenticate(r.Context(), plain)
	if err != nil || key.MachinePublicKey == nil {
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "runner key rejected")
		return runner.TransferSession{}, "", false
	}
	token := strings.TrimSpace(r.Header.Get(TransferTokenHeader))
	if token == "" {
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "transfer token required")
		return runner.TransferSession{}, "", false
	}
	sess, err := h.d.Tokens.Session(r.Context(), token, key.RunnerID)
	if err != nil {
		if transferDenied(err) {
			WriteError(w, http.StatusForbidden, "FORBIDDEN", "transfer token is not valid for this runner, job generation or operation")
			return runner.TransferSession{}, "", false
		}
		slog.Error("transfer token check failed", "error", err)
		WriteError(w, http.StatusInternalServerError, "INTERNAL", "transfer token check failed")
		return runner.TransferSession{}, "", false
	}
	if sess.Direction != direction {
		WriteError(w, http.StatusForbidden, "FORBIDDEN", "transfer token does not allow this operation")
		return runner.TransferSession{}, "", false
	}
	return sess, token, true
}

// extendDeadlines lifts the server-wide read deadline for this request only.
func (h *RunnerTransferHandler) extendDeadlines(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	until := time.Now().Add(h.d.RequestTimeout)
	_ = rc.SetReadDeadline(until)
	_ = rc.SetWriteDeadline(until)
}

func fenceOf(s runner.TransferSession) runner.Fence {
	return runner.Fence{JobID: s.JobID, RunnerID: s.RunnerID, Generation: s.Generation}
}

func (h *RunnerTransferHandler) progress(ctx context.Context, s runner.TransferSession, n, total int64) {
	h.mu.Lock()
	h.served[s.TokenHash] += n
	done := h.served[s.TokenHash]
	h.mu.Unlock()
	h.d.Sync.ReportProgress(ctx, fenceOf(s), s.RunID, done, total)
}

// pinned resolves the session's PINNED base: pull manifests and files are
// served only from the exact committed ref the token was issued for, never
// from Latest, so a candidate committed after issue cannot leak into a pull.
func (h *RunnerTransferHandler) pinned(w http.ResponseWriter, r *http.Request, s runner.TransferSession) (runner.CheckpointRef, runner.Manifest, bool) {
	ref, err := runner.ParseCheckpointRef(s.BaseRef)
	if err != nil || ref.IsZero() || ref.RunID != s.RunID {
		WriteError(w, http.StatusForbidden, "FORBIDDEN", "transfer token has no pinned checkpoint for this run")
		return runner.CheckpointRef{}, runner.Manifest{}, false
	}
	m, err := h.d.Checkpoints.Manifest(r.Context(), ref)
	if err != nil {
		if errors.Is(err, runner.ErrCheckpointNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "the pinned checkpoint is not available")
			return runner.CheckpointRef{}, runner.Manifest{}, false
		}
		slog.Error("transfer: reading manifest", "ref", ref.String(), "error", err)
		WriteError(w, http.StatusInternalServerError, "INTERNAL", "reading manifest failed")
		return runner.CheckpointRef{}, runner.Manifest{}, false
	}
	return ref, m, true
}

func (h *RunnerTransferHandler) manifest(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := h.authorize(w, r, runner.TransferPull)
	if !ok {
		return
	}
	if _, m, ok := h.pinned(w, r, sess); ok {
		WriteJSON(w, http.StatusOK, m)
	}
}

func (h *RunnerTransferHandler) file(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := h.authorize(w, r, runner.TransferPull)
	if !ok {
		return
	}
	h.extendDeadlines(w)
	p, err := runner.CleanRelPath(r.URL.Query().Get("path"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "path must be a relative path inside the workspace")
		return
	}
	ref, m, ok := h.pinned(w, r, sess)
	if !ok {
		return
	}
	rc, err := h.d.Checkpoints.ReadFile(r.Context(), ref, p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, runner.ErrCheckpointNotFound) || errors.Is(err, runner.ErrUnsupportedFileType) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "no such regular file in the pinned checkpoint")
			return
		}
		slog.Error("transfer: reading checkpoint file", "ref", ref.String(), "error", err)
		WriteError(w, http.StatusInternalServerError, "INTERNAL", "reading file failed")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	n, _ := io.Copy(w, rc)
	h.progress(r.Context(), sess, n, m.TotalBytes())
}

func (h *RunnerTransferHandler) sessionDir(s runner.TransferSession) string {
	return filepath.Join(h.d.StagingDir, s.TokenHash)
}

func partPath(dir string, index int) string {
	return filepath.Join(dir, fmt.Sprintf("part-%06d", index))
}

func validHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// maybeSweep removes expired sessions and staged part directories older than
// the retention, at most once a minute. Best effort.
func (h *RunnerTransferHandler) maybeSweep(ctx context.Context) {
	now := h.d.Clock()
	h.mu.Lock()
	if now.Sub(h.lastSweep) < time.Minute {
		h.mu.Unlock()
		return
	}
	h.lastSweep = now
	h.mu.Unlock()
	if _, err := h.d.Tokens.Sweep(ctx); err != nil {
		slog.Warn("transfer: sweeping expired sessions", "error", err)
	}
	entries, err := os.ReadDir(h.d.StagingDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < h.d.StagingRetention {
			continue
		}
		_ = os.RemoveAll(filepath.Join(h.d.StagingDir, e.Name()))
	}
}

func (h *RunnerTransferHandler) putPart(w http.ResponseWriter, r *http.Request) {
	sess, token, ok := h.authorize(w, r, runner.TransferPush)
	if !ok {
		return
	}
	h.extendDeadlines(w)
	idx, err := strconv.Atoi(chi.URLParam(r, "part"))
	if err != nil || idx < 0 || idx >= MaxTransferParts {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("part index must be 0..%d", MaxTransferParts-1))
		return
	}
	want := strings.ToLower(strings.TrimSpace(r.Header.Get(PartSHA256Header)))
	if !validHexSHA256(want) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", PartSHA256Header+" must be a hex SHA-256")
		return
	}
	h.maybeSweep(r.Context())
	dir := h.sessionDir(sess)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Error("transfer: creating staging dir", "error", err)
		WriteError(w, http.StatusInternalServerError, "INTERNAL", "staging failed")
		return
	}
	tmp, err := os.CreateTemp(dir, "upload-*.tmp")
	if err != nil {
		slog.Error("transfer: creating part file", "error", err)
		WriteError(w, http.StatusInternalServerError, "INTERNAL", "staging failed")
		return
	}
	hasher := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, hasher), http.MaxBytesReader(w, r.Body, h.d.PartBytes))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp.Name())
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(copyErr, &tooBig):
			WriteError(w, http.StatusRequestEntityTooLarge, "PART_TOO_LARGE", fmt.Sprintf("a part may be at most %d bytes", h.d.PartBytes))
		case errors.Is(copyErr, syscall.ENOSPC) || errors.Is(closeErr, syscall.ENOSPC):
			WriteError(w, http.StatusInsufficientStorage, "DISK_FULL", "server disk full while staging the part")
		default:
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "the part upload was interrupted; resend it")
		}
		return
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want {
		_ = os.Remove(tmp.Name())
		WriteError(w, http.StatusUnprocessableEntity, "PART_HASH_MISMATCH", "part hash mismatch; resend the part")
		return
	}
	if err := os.Rename(tmp.Name(), partPath(dir, idx)); err != nil {
		_ = os.Remove(tmp.Name())
		slog.Error("transfer: storing part", "error", err)
		WriteError(w, http.StatusInternalServerError, "INTERNAL", "staging failed")
		return
	}
	if err := h.d.Tokens.RecordPart(r.Context(), token, idx, got); err != nil {
		slog.Error("transfer: recording part", "error", err)
		WriteError(w, http.StatusInternalServerError, "INTERNAL", "recording the part failed")
		return
	}
	h.progress(r.Context(), sess, n, 0)
	received := []int{idx}
	for i := range sess.Parts {
		if i != idx {
			received = append(received, i)
		}
	}
	sort.Ints(received)
	WriteJSON(w, http.StatusOK, map[string]any{"part": idx, "received": received})
}

// concatParts writes parts 0..n-1 to w in order, re-verifying each against the
// hash recorded when it was received.
func concatParts(dir string, parts map[int]string, n int, w io.Writer) error {
	for i := 0; i < n; i++ {
		f, err := os.Open(partPath(dir, i))
		if err != nil {
			return fmt.Errorf("part %d: %w", i, err)
		}
		hasher := sha256.New()
		_, err = io.Copy(io.MultiWriter(w, hasher), f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("part %d: %w", i, err)
		}
		if hex.EncodeToString(hasher.Sum(nil)) != parts[i] {
			return fmt.Errorf("%w: staged part %d changed after upload", runner.ErrManifestMismatch, i)
		}
	}
	return nil
}

type pushCommitBody struct {
	Parts  int    `json:"parts"`
	SHA256 string `json:"sha256"`
}

func (h *RunnerTransferHandler) commit(w http.ResponseWriter, r *http.Request) {
	sess, token, ok := h.authorize(w, r, runner.TransferPush)
	if !ok {
		return
	}
	h.extendDeadlines(w)
	if sess.CheckpointRef != "" {
		ref, err := runner.ParseCheckpointRef(sess.CheckpointRef)
		if err == nil {
			WriteJSON(w, http.StatusOK, ref)
			return
		}
		slog.Error("transfer: stored checkpoint ref unreadable", "error", err)
	}
	var body pushCommitBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || body.Parts < 1 ||
		body.Parts > MaxTransferParts || !validHexSHA256(strings.ToLower(body.SHA256)) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", `body must be {"parts":N,"sha256":"<hex>"}`)
		return
	}
	for i := 0; i < body.Parts; i++ {
		if _, have := sess.Parts[i]; !have {
			WriteError(w, http.StatusConflict, "INCOMPLETE_PUSH", fmt.Sprintf("part %d is missing; resend it before committing", i))
			return
		}
	}
	dir := h.sessionDir(sess)
	whole := sha256.New()
	if err := concatParts(dir, sess.Parts, body.Parts, whole); err != nil {
		h.writeStageError(w, err)
		return
	}
	if hex.EncodeToString(whole.Sum(nil)) != strings.ToLower(body.SHA256) {
		WriteError(w, http.StatusUnprocessableEntity, "PUSH_HASH_MISMATCH", "the assembled push does not match its hash; resend the parts")
		return
	}
	// The delta applies to the session's PINNED base, never Latest.
	base, err := runner.ParseCheckpointRef(sess.BaseRef)
	if err != nil || base.IsZero() || base.RunID != sess.RunID {
		WriteError(w, http.StatusForbidden, "FORBIDDEN", "the push token has no pinned base for this run")
		return
	}

	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pw.CloseWithError(concatParts(dir, sess.Parts, body.Parts, pw))
	}()
	staged, stageErr := h.d.Checkpoints.StageDelta(r.Context(), sess.RunID, base, pr)
	_ = pr.Close()
	<-done
	if stageErr != nil {
		h.writeStageError(w, stageErr)
		return
	}
	staged.Resource = sess.Resource // scope comes from the durable session, never from archive bytes
	ref, err := h.d.Sync.AcceptPush(r.Context(), fenceOf(sess), sess.RunID, staged)
	if err != nil {
		switch {
		case errors.Is(err, runner.ErrFenced) || errors.Is(err, runner.ErrHomeOwned) || errors.Is(err, runner.ErrResourceFenced):
			WriteError(w, http.StatusForbidden, "FORBIDDEN", "this job generation no longer owns the workspace")
		case errors.Is(err, runner.ErrCheckpointConflict):
			WriteError(w, http.StatusConflict, "BASE_MOVED", "the committed checkpoint moved; pull again")
		default:
			h.writeStageError(w, err)
		}
		return
	}
	if err := h.d.Tokens.RecordCommit(r.Context(), token, ref.String()); err != nil {
		slog.Warn("transfer: recording push commit", "ref", ref.String(), "error", err)
	}
	_ = os.RemoveAll(dir)
	WriteJSON(w, http.StatusOK, ref)
}

func (h *RunnerTransferHandler) writeStageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, syscall.ENOSPC):
		WriteError(w, http.StatusInsufficientStorage, "DISK_FULL", "server disk full while applying the push; the previous checkpoint is kept")
	case errors.Is(err, runner.ErrManifestMismatch), errors.Is(err, runner.ErrCorruptCheckpoint), errors.Is(err, runner.ErrUnsupportedFileType):
		WriteError(w, http.StatusUnprocessableEntity, "PUSH_REJECTED", err.Error())
	case errors.Is(err, runner.ErrCheckpointConflict):
		WriteError(w, http.StatusConflict, "BASE_MOVED", "the committed checkpoint moved; pull again")
	case errors.Is(err, fs.ErrNotExist):
		WriteError(w, http.StatusConflict, "INCOMPLETE_PUSH", "a staged part expired; resend the parts")
	default:
		slog.Error("transfer: applying push", "error", err)
		WriteError(w, http.StatusInternalServerError, "INTERNAL", "applying the push failed")
	}
}

func (h *RunnerTransferHandler) bundle(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := h.authorize(w, r, runner.TransferBundle)
	if !ok {
		return
	}
	if h.d.Bundles == nil {
		WriteError(w, http.StatusServiceUnavailable, "BUNDLES_UNAVAILABLE", "script bundles are not configured on this server")
		return
	}
	hash := chi.URLParam(r, "hash")
	if !validHexSHA256(hash) || strings.ToLower(hash) != hash {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "bundle hash must be a lowercase hex SHA-256")
		return
	}
	if hash != sess.BundleHash {
		WriteError(w, http.StatusForbidden, "FORBIDDEN", "this bundle token is for another bundle")
		return
	}
	h.extendDeadlines(w)
	rc, err := h.d.Bundles.Open(r.Context(), hash)
	if err != nil {
		slog.Warn("transfer: bundle unavailable", "hash", hash, "error", err)
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "bundle not found")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = io.Copy(w, rc)
}
