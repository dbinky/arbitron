package notification

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
)

var modelCardEscapes = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\",
	"*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)",
	"`", "\\`", "#", "\\#", "!", "\\!", "|", "\\|",
)

func buildModelAnnouncement(part modelcatalog.AnnouncementPart) ([]byte, error) {
	p := part.Payload
	if part.EventID == "" || part.EventID != p.EventID || part.Index < 1 || part.Count < part.Index ||
		p.CanonicalID == "" || p.Provider == "" || p.FirstConfirmedAt.IsZero() {
		return nil, modelcatalog.ErrAnnouncementPayload
	}
	tier := p.Tier
	if tier == "" {
		tier = "unknown"
	}
	var facts []cardFact
	add := func(title, value string) {
		facts = append(facts, cardFact{Title: modelCardEscapes.Replace(title), Value: modelCardEscapes.Replace(value)})
	}
	add("Event", p.EventID)
	add("Provider", p.Provider)
	add("Model", p.CanonicalID)
	add("Tier", tier)
	add("First confirmed", p.FirstConfirmedAt.UTC().Format(time.RFC3339Nano))
	if part.Count > 1 {
		add("Part", fmt.Sprintf("%d/%d", part.Index, part.Count))
	}
	// Project only inventory's safe opaque identifiers/revisions. In particular,
	// never marshal UsabilityEvidence itself: ProofRef contains private paths.
	for _, path := range p.Paths {
		if path.PathKey == "" || path.ConfigRevision == "" {
			return nil, modelcatalog.ErrAnnouncementPayload
		}
		facts = append(facts, cardFact{Title: "Path", Value: modelCardEscapes.Replace(path.PathKey) + " (" + modelCardEscapes.Replace(path.ConfigRevision) + ")"})
	}
	if len(p.Changes) == 0 {
		message := "No profiles changed"
		if part.Count > 1 {
			message = "No profile changes in this part"
		}
		add("Profiles", message)
	}
	for _, change := range p.Changes {
		if change.Alias == "" || change.FromModel == "" || change.ToModel == "" {
			return nil, modelcatalog.ErrAnnouncementPayload
		}
		add(change.Alias, change.FromModel+" → "+change.ToModel)
	}
	card := adaptiveCard{
		Schema: "http://adaptivecards.io/schemas/adaptive-card.json", Type: "AdaptiveCard", Version: "1.4",
		Body: []cardElement{
			{Type: "TextBlock", Size: "Medium", Weight: "Bolder", Text: "New usable model"},
			{Type: "FactSet", Facts: facts},
		},
	}
	return json.Marshal(teamsMessage{Type: "message", Attachments: []teamsAttachment{{ContentType: "application/vnd.microsoft.card.adaptive", Content: card}}})
}

// PartitionAnnouncement belongs beside the Teams serializer so its byte budget
// includes the actual envelope, escaping and numbered-card metadata. The durable
// repository stores this complete plan before sending or acknowledging any part.
// Index is one-based; ordinary single-card payloads are unchanged.
func PartitionAnnouncement(p modelcatalog.AnnouncementPayload, maxBytes int) ([]modelcatalog.AnnouncementPart, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: nonpositive card limit", modelcatalog.ErrAnnouncementPayload)
	}
	maxBytes = min(maxBytes, modelcatalog.MaxAnnouncementBytes)
	p.Paths = slices.Clone(p.Paths)
	p.Changes = slices.Clone(p.Changes)
	single := modelcatalog.AnnouncementPart{EventID: p.EventID, Index: 1, Count: 1, Payload: p}
	raw, err := buildModelAnnouncement(single)
	if err != nil {
		return nil, err
	}
	if len(raw) <= maxBytes {
		return []modelcatalog.AnnouncementPart{single}, nil
	}

	// At most one part per indivisible item. Reserve that many digits in both
	// numbering fields so final numbering can never push a card over the cap.
	upper := max(2, len(p.Paths)+len(p.Changes))
	base := p
	base.Paths, base.Changes = nil, nil
	current := modelcatalog.AnnouncementPart{EventID: p.EventID, Index: upper, Count: upper, Payload: base}
	var parts []modelcatalog.AnnouncementPart
	fits := func(part modelcatalog.AnnouncementPart) bool {
		raw, err := buildModelAnnouncement(part)
		return err == nil && len(raw) <= maxBytes
	}
	if !fits(current) {
		return nil, fmt.Errorf("%w: model identity exceeds card limit", modelcatalog.ErrAnnouncementPayload)
	}
	flush := func() {
		parts = append(parts, current)
		current.Payload = base
	}
	for _, path := range p.Paths {
		candidate := current
		candidate.Payload.Paths = append(slices.Clone(current.Payload.Paths), path)
		if !fits(candidate) {
			if len(current.Payload.Paths)+len(current.Payload.Changes) == 0 {
				return nil, fmt.Errorf("%w: indivisible path exceeds card limit", modelcatalog.ErrAnnouncementPayload)
			}
			flush()
			candidate = current
			candidate.Payload.Paths = []modelcatalog.UsabilityEvidence{path}
			if !fits(candidate) {
				return nil, fmt.Errorf("%w: indivisible path exceeds card limit", modelcatalog.ErrAnnouncementPayload)
			}
		}
		current = candidate
	}
	for _, change := range p.Changes {
		candidate := current
		candidate.Payload.Changes = append(slices.Clone(current.Payload.Changes), change)
		if !fits(candidate) {
			if len(current.Payload.Paths)+len(current.Payload.Changes) == 0 {
				return nil, fmt.Errorf("%w: indivisible profile change exceeds card limit", modelcatalog.ErrAnnouncementPayload)
			}
			flush()
			candidate = current
			candidate.Payload.Changes = append(candidate.Payload.Changes, change)
			if !fits(candidate) {
				return nil, fmt.Errorf("%w: indivisible profile change exceeds card limit", modelcatalog.ErrAnnouncementPayload)
			}
		}
		current = candidate
	}
	if len(current.Payload.Paths)+len(current.Payload.Changes) != 0 {
		flush()
	}
	for i := range parts {
		parts[i].Index, parts[i].Count = i+1, len(parts)
	}
	return parts, nil
}
