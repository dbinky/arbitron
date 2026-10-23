package settings

import (
	"context"
	"fmt"
	"net/url"
	"reflect"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
)

// Service separates admin-only public settings from trusted internal reads.
type Service struct{ store SystemSettings }

// NewService requires a real store; a missing dependency is a wiring error.
func NewService(store SystemSettings) *Service {
	if store == nil {
		panic("settings: nil store")
	}
	switch v := reflect.ValueOf(store); v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			panic("settings: nil store")
		}
	}
	return &Service{store: store}
}

func requireAdmin(ctx context.Context) error {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.ErrUnauthenticated
	}
	if !id.IsAdmin() {
		return identity.ErrForbidden
	}
	return nil
}

func publicValues(v Values) PublicValues {
	return PublicValues{
		CatalogURL:      v.CatalogURL,
		TeamsWebhookURL: config.RedactWebhookURL(v.TeamsWebhookURL),
		TeamsConfigured: v.TeamsWebhookURL != "",
	}
}

func (s *Service) Get(ctx context.Context) (PublicValues, error) {
	if err := requireAdmin(ctx); err != nil {
		return PublicValues{}, err
	}
	v, err := s.store.Read(ctx)
	if err != nil {
		return PublicValues{}, err
	}
	return publicValues(v), nil
}

func (s *Service) Update(ctx context.Context, patch Patch) (PublicValues, error) {
	if err := requireAdmin(ctx); err != nil {
		return PublicValues{}, err
	}
	if patch.CatalogURL != nil {
		u, err := url.Parse(*patch.CatalogURL)
		// Catalog content decides which models get paid probes and which can
		// auto-advance following profiles; plaintext would let a network
		// attacker rewrite it.
		if err != nil || !u.IsAbs() || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return PublicValues{}, fmt.Errorf("%w: catalog URL must be an absolute HTTPS URL, without credentials", ErrInvalid)
		}
	}
	if patch.TeamsWebhookURL != nil {
		if *patch.TeamsWebhookURL == config.RedactedSecret {
			return PublicValues{}, fmt.Errorf("%w: webhook URL must not be a redacted placeholder", ErrInvalid)
		}
		if err := config.ValidateTeamsWebhookURL(*patch.TeamsWebhookURL); err != nil {
			return PublicValues{}, fmt.Errorf("%w: webhook URL must be a valid HTTPS URL", ErrInvalid)
		}
	}
	v, err := s.store.Update(ctx, patch)
	if err != nil {
		return PublicValues{}, err
	}
	return publicValues(v), nil
}

// ReadInternal is a trusted port for background services; it never redacts or
// converts storage errors into an empty (disabled) destination.
func (s *Service) ReadInternal(ctx context.Context) (Values, error) {
	return s.store.Read(ctx)
}
