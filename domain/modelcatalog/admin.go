package modelcatalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
)

// AdminService is the admin-only face of the catalog: the redacted list and the
// manual refresh. Authorization lives here, not in the transport. The refresh
// runs through the same single-flight coordinator as the startup pass and the
// four-hour tick; that cycle uses the coordinator's own context, so the
// internal refresh never impersonates the calling admin.
type AdminService struct {
	Store   CatalogStore
	Refresh func(context.Context) (RefreshReport, error)
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

// List returns the stored catalog through ProjectCatalog: no source URLs, proof
// paths or execution bindings cross this boundary.
func (a *AdminService) List(ctx context.Context) (PublicCatalogView, error) {
	if err := requireAdmin(ctx); err != nil {
		return PublicCatalogView{}, err
	}
	if a == nil || a.Store == nil {
		return PublicCatalogView{}, errNilCollaborator
	}
	view, err := a.Store.Snapshot(ctx)
	if err != nil {
		return PublicCatalogView{}, fmt.Errorf("reading model catalog: %w", err)
	}
	return ProjectCatalog(view), nil
}

// RefreshNow runs one cycle and returns its redacted report, or ErrRefreshBusy
// when a cycle is already in flight. A durable report whose publication is still
// pending is returned as such, not as a failure: the next cycle or recovery
// publishes it.
func (a *AdminService) RefreshNow(ctx context.Context) (PublicRefreshReport, error) {
	if err := requireAdmin(ctx); err != nil {
		return PublicRefreshReport{}, err
	}
	if a == nil || a.Refresh == nil {
		return PublicRefreshReport{}, errNilCollaborator
	}
	report, err := a.Refresh(ctx)
	if errors.Is(err, provider.ErrPublicationPending) {
		report.PublicationPending = true
		return ProjectReport(report), nil
	}
	if err != nil {
		return PublicRefreshReport{}, err
	}
	return ProjectReport(report), nil
}
