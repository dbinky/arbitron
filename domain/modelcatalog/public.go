package modelcatalog

import (
	"slices"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
)

type PublicPath struct {
	PathKey        string    `json:"path_key"`
	ConfigRevision string    `json:"config_revision"`
	State          string    `json:"state"`
	CheckedAt      time.Time `json:"checked_at"`
}
type PublicCatalogEntry struct {
	CanonicalID string `json:"canonical_id"`
	Provider    string `json:"provider"`
	NativeID    string `json:"native_id"`
	Tier        string `json:"tier"`
	// PolicyRevision belongs to the stored classification, which may lag the
	// current identity policy until a successful refresh replaces this slice.
	PolicyRevision int64        `json:"policy_revision"`
	SpecificModel  string       `json:"specific_model"`
	Usability      string       `json:"usability"`
	Paths          []PublicPath `json:"paths"`
}
type PublicRefreshReport struct {
	ID                 string                 `json:"id"`
	At                 time.Time              `json:"at"`
	SliceStatus        map[string]string      `json:"slice_status"`
	PathStatus         map[string]string      `json:"path_status"`
	Changes            []provider.Advancement `json:"changes"`
	PublicationPending bool                   `json:"publication_pending"`
	AnnouncementError  string                 `json:"announcement_error,omitempty"`
}
type PublicCatalogView struct {
	Entries          []PublicCatalogEntry    `json:"entries"`
	UnmatchedTargets []provider.FollowTarget `json:"unmatched_targets"`
	Reports          []PublicRefreshReport   `json:"reports"`
}

// ProjectCatalog deliberately builds a separate DTO: never serialize internal
// slices, source URLs, raw proof references or execution bindings.
func ProjectCatalog(view CatalogView) PublicCatalogView {
	out := PublicCatalogView{UnmatchedTargets: slices.Clone(view.UnmatchedTargets)}
	for _, slice := range view.Catalog.Slices {
		for _, entry := range slice.Entries {
			public := PublicCatalogEntry{CanonicalID: entry.CanonicalID, Provider: entry.Provider, NativeID: entry.NativeID, Tier: entry.Tier, PolicyRevision: entry.PolicyRevision, SpecificModel: entry.SpecificModel, Usability: UsabilityUnknown}
			for _, ev := range view.Evidence {
				if ev.CanonicalID != entry.CanonicalID || ev.PathKey == "" || ev.ConfigRevision == "" {
					continue
				}
				state := publicStatus(ev.State)
				if state == UsabilityConfirmed && !ev.Confirmed() {
					state = UsabilityUnknown
				}
				public.Paths = append(public.Paths, PublicPath{PathKey: ev.PathKey, ConfigRevision: ev.ConfigRevision, State: state, CheckedAt: ev.CheckedAt})
				if ev.Confirmed() {
					public.Usability = UsabilityConfirmed
				}
			}
			out.Entries = append(out.Entries, public)
		}
	}
	for _, report := range view.Reports {
		out.Reports = append(out.Reports, ProjectReport(report))
	}
	return out
}

// ProjectReport is the redacted form of one refresh report.
func ProjectReport(report RefreshReport) PublicRefreshReport {
	public := PublicRefreshReport{ID: report.ID, At: report.At, SliceStatus: publicStatuses(report.SliceStatus), PathStatus: publicStatuses(report.PathStatus), Changes: slices.Clone(report.Changes), PublicationPending: report.PublicationPending}
	if report.AnnouncementError != "" {
		public.AnnouncementError = "announcement_error"
	}
	return public
}

func publicStatuses(statuses map[string]string) map[string]string {
	if statuses == nil {
		return nil
	}
	out := make(map[string]string, len(statuses))
	for key, value := range statuses {
		out[key] = publicStatus(value)
	}
	return out
}

// Diagnostics cross the public boundary only as static classified codes.
func publicStatus(status string) string {
	switch status {
	case SliceStatusOK, SliceStatusInvalid, UsabilityConfirmed, UsabilityUnavailable, UsabilityUnknown,
		"fetch_failed", "parse_error", "budget_exhausted", "advance_conflict", "invalid_slice", "publication_pending",
		"unknown_current", "ambiguous_identity", "unavailable_paths", "candidate_invalid":
		return status
	default:
		return UsabilityUnknown
	}
}
