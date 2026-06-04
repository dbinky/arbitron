package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// DeviceCodeService implements the OAuth 2.0 device authorization grant. A
// browserless client Starts a flow, an authenticated browser Approves it, and
// the client Polls until approval, at which point the service mints a personal
// API token FOR THE APPROVER (never a raw IdP token) — reusing the TokenService
// so device login produces an ordinary arb_ bearer credential.
type DeviceCodeService struct {
	repo     DeviceCodeRepository
	tokens   *TokenService
	ttl      time.Duration
	interval int
	now      func() time.Time
	// newCodes is the crypto/rand seam producing (deviceCode, userCode). Tests
	// may override it; production reads crypto/rand.
	newCodes func() (deviceCode, userCode string, err error)
}

// NewDeviceCodeService builds the service with the given token TTL and poll
// interval (seconds).
func NewDeviceCodeService(repo DeviceCodeRepository, tokens *TokenService, ttl time.Duration, interval int) *DeviceCodeService {
	return &DeviceCodeService{
		repo:     repo,
		tokens:   tokens,
		ttl:      ttl,
		interval: interval,
		now:      time.Now,
		newCodes: generateDeviceCodes,
	}
}

// StartResponse mirrors the RFC 8628 device-authorization response.
type StartResponse struct {
	DeviceCode      string // the polling secret (plaintext, returned once)
	UserCode        string // shown to the human
	VerificationURI string
	Interval        int
	ExpiresIn       int // seconds
}

// Start generates a fresh device_code + user_code, persists the row (storing
// ONLY the hash of the device_code), and returns the RFC fields. The plaintext
// device_code is returned once and is the polling secret.
func (s *DeviceCodeService) Start(ctx context.Context, verificationURI string) (*StartResponse, error) {
	deviceCode, userCode, err := s.newCodes()
	if err != nil {
		return nil, fmt.Errorf("generating device codes: %w", err)
	}
	now := s.now().UTC()
	dc := &DeviceCode{
		HashedCode: HashToken(deviceCode),
		UserCode:   userCode,
		Status:     DeviceCodePending,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.ttl),
	}
	if err := s.repo.Create(ctx, dc); err != nil {
		return nil, err
	}
	return &StartResponse{
		DeviceCode:      deviceCode,
		UserCode:        userCode,
		VerificationURI: verificationURI,
		Interval:        s.interval,
		ExpiresIn:       int(s.ttl.Seconds()),
	}, nil
}

// Approve resolves the user_code to the logged-in approver (identity.FromContext)
// and marks the row approved. The row must be pending and unexpired.
func (s *DeviceCodeService) Approve(ctx context.Context, userCode string) error {
	id, ok := FromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	dc, err := s.repo.GetByUserCode(ctx, userCode)
	if err != nil {
		return err // ErrNotFound
	}
	if !s.now().UTC().Before(dc.ExpiresAt) {
		return ErrExpired
	}
	return s.repo.Approve(ctx, userCode, id.UserID)
}

// Deny resolves the user_code and marks the row denied (terminal). Requires an
// authenticated approver.
func (s *DeviceCodeService) Deny(ctx context.Context, userCode string) error {
	if _, ok := FromContext(ctx); !ok {
		return ErrUnauthenticated
	}
	return s.repo.Deny(ctx, userCode)
}

// Poll is the token-endpoint core. It returns the minted plaintext token on
// success, or one of the RFC 8628 terminal/pending errors.
//
// SECURITY: consume-before-mint with the repo's atomic status='approved' guard
// guarantees at-most-once minting under concurrent polls — only the poll that
// wins Consume proceeds to Mint, and Mint runs with the approver's identity so
// the token is owned by the right user.
func (s *DeviceCodeService) Poll(ctx context.Context, deviceCode string) (plaintext string, t *Token, err error) {
	if deviceCode == "" {
		return "", nil, ErrInvalidGrant
	}
	dc, err := s.repo.GetByHash(ctx, HashToken(deviceCode))
	if err != nil {
		return "", nil, ErrInvalidGrant
	}
	if !s.now().UTC().Before(dc.ExpiresAt) {
		return "", nil, ErrExpired
	}
	switch dc.Status {
	case DeviceCodePending:
		return "", nil, ErrAuthorizationPending
	case DeviceCodeDenied:
		return "", nil, ErrAccessDenied
	case DeviceCodeConsumed:
		return "", nil, ErrInvalidGrant
	case DeviceCodeApproved:
		if dc.UserID == nil {
			// Defensive: an approved row must carry the approver.
			return "", nil, ErrInvalidGrant
		}
		// Atomically consume; a racing poll that already consumed it -> 0 rows.
		if err := s.repo.Consume(ctx, dc.HashedCode); err != nil {
			return "", nil, ErrInvalidGrant
		}
		mintCtx := WithIdentity(ctx, Identity{UserID: *dc.UserID})
		return s.tokens.Mint(mintCtx, "device-login")
	default:
		return "", nil, ErrInvalidGrant
	}
}

// DeleteExpired purges expired device-code rows (background sweep).
func (s *DeviceCodeService) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	return s.repo.DeleteExpired(ctx, now)
}

// generateDeviceCodes produces a high-entropy device_code (the polling secret)
// and a shorter human user_code, both from crypto/rand.
func generateDeviceCodes() (deviceCode, userCode string, err error) {
	deviceRaw := make([]byte, 32)
	if _, err := rand.Read(deviceRaw); err != nil {
		return "", "", err
	}
	userRaw := make([]byte, 4)
	if _, err := rand.Read(userRaw); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(deviceRaw), formatUserCode(userRaw), nil
}

// formatUserCode renders bytes as an uppercase, dash-grouped, human-typeable
// code (e.g. "A1B2-C3D4").
func formatUserCode(b []byte) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no easily-confused chars
	out := make([]byte, 0, 9)
	for i, x := range b {
		if i == 2 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(x)%len(alphabet)], alphabet[int(x>>3)%len(alphabet)])
	}
	return string(out)
}
