package renderreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
)

// ErrEvidenceUnavailable means the rendering's screenshots cannot be examined.
// The coordinator records it as a failed (retryable) review, never a pass.
var ErrEvidenceUnavailable = errors.New("renderreview: rendered evidence is unavailable")

// ReadabilityReviewer is the production layout.Reviewer. It judges what the
// browser actually drew (the render surface's DOM report and the captured
// overview and readable-scale tiles), not the geometry's promises.
type ReadabilityReviewer struct {
	logger *slog.Logger
	now    func() time.Time
}

var _ layout.Reviewer = (*ReadabilityReviewer)(nil)

// NewReadabilityReviewer builds the reviewer. A nil logger uses slog.Default().
func NewReadabilityReviewer(logger *slog.Logger) *ReadabilityReviewer {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReadabilityReviewer{logger: logger, now: time.Now}
}

// Review examines one candidate's own rendering. A rendering that is not
// provably of this candidate, or is incomplete, is reported with
// Rendered=false. Unreadable screenshot files are an error.
func (r *ReadabilityReviewer) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error) {
	if err := ctx.Err(); err != nil {
		return layout.Review{}, fmt.Errorf("renderreview: review interrupted: %w", err)
	}
	geo, rend := req.Geometry, req.Rendering
	rep := rend.Report
	ev := layout.Evidence{
		GeometryDigest:  rend.GeometryDigest,
		RendererVersion: rep.SurfaceVersion,
		Contract:        rep.Contract,
		FontsReady:      rep.FontsReady && rep.FontFamilyResolved,
		Zoom:            rep.Zoom,
		Tiles:           countKind(rend.Artifacts, "tile"),
		Artifacts:       append([]layout.Artifact(nil), rend.Artifacts...),
		ReviewedAt:      r.now().UTC(),
	}
	f := newFindingSet()

	checkIdentity(req, f)
	states := slices.Clone(layout.RuntimeStates)
	if f.blocking() == 0 {
		states = append(states, layout.GroupProbeNames(req.WorkflowGroups)...)
	}
	checkCoverage(geo, rep, states, f)
	checkGroupCoverage(geo, rep, f)
	if f.blocking() > 0 {
		r.logger.Warn("layout review: rendering is not evidence for this candidate",
			"revision", req.Revision, "attempt", req.Attempt, "findings", len(f.items))
		return layout.Review{Rendered: false, Findings: f.items, Evidence: ev}, nil
	}

	overview, tiles, err := loadArtifacts(ctx, rend.Artifacts)
	if err != nil {
		return layout.Review{}, err
	}
	checkCapture(ctx, geo, rep, overview, tiles, f)
	checkGroupCapture(ctx, geo, rep, tiles, f)
	if err := ctx.Err(); err != nil {
		return layout.Review{}, fmt.Errorf("renderreview: review interrupted: %w", err)
	}
	captureComplete := f.blocking() == 0

	checkFonts(rep, f)
	checkNodes(geo, rep, f)
	checkEdges(geo, rep, f)
	checkGroups(geo, rep, f)
	checkCrossings(geo, rep, f)
	checkStates(geo, rep, f)
	if err := ctx.Err(); err != nil {
		return layout.Review{}, fmt.Errorf("renderreview: review interrupted: %w", err)
	}
	return layout.Review{Rendered: captureComplete, Findings: f.items, Evidence: ev}, nil
}

// findingSet accumulates review findings in check order.
type findingSet struct{ items []layout.Finding }

func newFindingSet() *findingSet { return &findingSet{items: []layout.Finding{}} }

func (s *findingSet) add(c layout.Criterion, sev layout.Severity, nodes []string, edges []int, format string, args ...any) {
	s.items = append(s.items, layout.Finding{
		Criterion: c, Severity: sev, Source: layout.SourceReview,
		NodeIDs: nodes, EdgeOrdinals: edges, Evidence: fmt.Sprintf(format, args...),
	})
}

func (s *findingSet) block(c layout.Criterion, nodes []string, edges []int, format string, args ...any) {
	s.add(c, layout.SeverityBlocking, nodes, edges, format, args...)
}

func (s *findingSet) blocking() int { return layout.BlockingCount(s.items) }

func countKind(arts []layout.Artifact, kind string) int {
	n := 0
	for _, a := range arts {
		if a.Kind == kind {
			n++
		}
	}
	return n
}

// checkIdentity proves the rendering is of this exact candidate, drawn by the
// same renderer contract at the practical zoom.
func checkIdentity(req layout.ReviewRequest, f *findingSet) {
	geo, rend, rep := req.Geometry, req.Rendering, req.Rendering.Report
	var groupIDs []string
	for _, n := range geo.Nodes {
		if n.TypeCaption == "parallel group" {
			groupIDs = append(groupIDs, n.ID)
		}
	}
	groupsMatch := req.WorkflowGroups == nil && len(groupIDs) == 0
	if req.WorkflowGroups != nil && len(groupIDs) > 0 {
		groupsMatch = slices.Equal(groupIDs, req.WorkflowGroups.Order) && len(req.WorkflowGroups.Groups) == len(groupIDs)
		for _, id := range groupIDs {
			scope := req.WorkflowGroups.Groups[id]
			groupsMatch = groupsMatch && scope != nil && scope.NodeID == id
		}
	}
	if !groupsMatch {
		f.block(layout.CriterionRenderMismatch, groupIDs, nil, "missing or mismatched group ownership")
	} else if len(groupIDs) > 0 && geo.Format == layout.FormatV1 {
		f.block(layout.CriterionRenderMismatch, groupIDs, nil, "reviewed group geometry is unavailable in v1")
	}
	seen := map[string]bool{}
	for _, n := range geo.Nodes {
		if n.ID == "" || seen[n.ID] {
			f.block(layout.CriterionRenderMismatch, []string{n.ID}, nil, "candidate node identity is empty or duplicated")
		}
		seen[n.ID] = true
	}
	if err := req.ValidateGeometry(); err != nil || geo.Contract != layout.DefaultContract() {
		f.block(layout.CriterionRenderMismatch, nil, nil, "candidate geometry or renderer contract is invalid: %v", err)
	}
	if req.Revision == "" || req.Attempt < 1 {
		f.block(layout.CriterionRenderMismatch, nil, nil, "review requires a saved revision and positive attempt")
	}
	if rend.Revision != req.Revision || rend.Attempt != req.Attempt {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"rendering belongs to revision %q attempt %d; review requested revision %q attempt %d",
			rend.Revision, rend.Attempt, req.Revision, req.Attempt)
	}
	if want := layout.GeometryDigest(geo); rend.GeometryDigest != want {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"rendering is of geometry %q; the candidate under review is %q", rend.GeometryDigest, want)
	}
	if rep.SurfaceVersion != layout.ContractVersion {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"render surface version %q does not match renderer contract %q", rep.SurfaceVersion, layout.ContractVersion)
	}
	if rep.Contract != geo.Contract {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"render surface used a different renderer contract than the candidate (%q)", geo.Contract.Version)
	}
	if rep.Zoom != geo.Contract.PracticalZoom {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"rendered at zoom %.3f; readable review requires the practical zoom %.2f", rep.Zoom, geo.Contract.PracticalZoom)
	}
	if rep.Canvas != geo.Canvas {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"rendered canvas %s differs from the candidate canvas %s", fmtRect(rep.Canvas), fmtRect(geo.Canvas))
	}
}

// checkCoverage proves every node, edge occurrence, crossing and runtime state
// of the candidate was drawn exactly once.
func checkCoverage(geo layout.Geometry, rep layout.RenderReport, states []string, f *findingSet) {
	want := make(map[string]bool, len(geo.Nodes))
	for _, n := range geo.Nodes {
		want[n.ID] = true
	}
	seen := make(map[string]int, len(rep.Nodes))
	for _, n := range rep.Nodes {
		seen[n.ID]++
		if !want[n.ID] {
			f.block(layout.CriterionCoverage, []string{n.ID}, nil, "render report contains node %q, which is not in the candidate", n.ID)
		}
	}
	for _, n := range geo.Nodes {
		switch c := seen[n.ID]; c {
		case 1:
		case 0:
			f.block(layout.CriterionCoverage, []string{n.ID}, nil, "node %q was not drawn", n.ID)
		default:
			f.block(layout.CriterionCoverage, []string{n.ID}, nil, "node %q was drawn %d times", n.ID, c)
		}
	}
	if len(rep.Edges) != len(geo.Edges) {
		f.block(layout.CriterionCoverage, nil, nil, "render report has %d edges; the candidate has %d edge occurrences", len(rep.Edges), len(geo.Edges))
	}
	for i, e := range rep.Edges {
		if e.Ordinal != i {
			f.block(layout.CriterionCoverage, nil, []int{e.Ordinal}, "edge report %d carries ordinal %d; edges must be reported in manifest order", i, e.Ordinal)
		}
	}
	if len(rep.Crossings) != len(geo.Crossings) {
		f.block(layout.CriterionCoverage, nil, nil, "render report has %d crossings; the candidate records %d", len(rep.Crossings), len(geo.Crossings))
	}
	seenCrossings := map[layout.Crossing]bool{}
	for i, c := range rep.Crossings {
		key := layout.Crossing{Point: c.Point, Over: c.Over, Under: c.Under}
		if seenCrossings[key] {
			f.block(layout.CriterionCoverage, nil, []int{c.Over, c.Under}, "crossing is duplicated")
		}
		seenCrossings[key] = true
		if i < len(geo.Crossings) {
			want := geo.Crossings[i]
			if c.Over != want.Over || c.Under != want.Under || !pointNear(c.Point, want.Point, .01) {
				f.block(layout.CriterionRenderMismatch, nil, []int{want.Over, want.Under}, "crossing %d identity/location differs from candidate", i)
			}
		}
	}
	probed := make(map[string]bool, len(rep.States))
	for _, s := range rep.States {
		known := false
		for _, expected := range states {
			if s.State == expected {
				known = true
				break
			}
		}
		if !known || probed[s.State] {
			f.block(layout.CriterionCoverage, nil, nil, "unknown or duplicated runtime state %q", s.State)
		}
		probed[s.State] = true
	}
	for _, s := range states {
		if !probed[s] {
			f.block(layout.CriterionCoverage, nil, nil, "runtime state %q was not exercised by the rendering", s)
		}
	}
}

// shot is one decoded screenshot artifact.
type shot struct {
	art layout.Artifact
	img image.Image
}

// loadArtifacts reads, verifies and decodes the overview and the tiles.
func loadArtifacts(ctx context.Context, arts []layout.Artifact) (shot, []shot, error) {
	var overview *shot
	var tiles []shot
	for _, a := range arts {
		if err := ctx.Err(); err != nil {
			return shot{}, nil, fmt.Errorf("renderreview: review interrupted: %w", err)
		}
		s, err := loadArtifact(a)
		if err != nil {
			return shot{}, nil, err
		}
		switch a.Kind {
		case "overview":
			if overview != nil {
				return shot{}, nil, fmt.Errorf("%w: rendering has more than one overview", ErrEvidenceUnavailable)
			}
			overview = &s
		case "tile":
			tiles = append(tiles, s)
		default:
			return shot{}, nil, fmt.Errorf("%w: unknown artifact kind %q (%s)", ErrEvidenceUnavailable, a.Kind, a.Path)
		}
	}
	if overview == nil {
		return shot{}, nil, fmt.Errorf("%w: rendering has no overview screenshot", ErrEvidenceUnavailable)
	}
	if len(tiles) == 0 {
		return shot{}, nil, fmt.Errorf("%w: rendering has no readable-scale tiles", ErrEvidenceUnavailable)
	}
	return *overview, tiles, nil
}

func loadArtifact(a layout.Artifact) (shot, error) {
	if !filepath.IsAbs(a.Path) {
		return shot{}, fmt.Errorf("%w: artifact path %q is not absolute", ErrEvidenceUnavailable, a.Path)
	}
	raw, err := os.ReadFile(a.Path)
	if err != nil {
		return shot{}, fmt.Errorf("%w: %v", ErrEvidenceUnavailable, err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != a.SHA256 {
		return shot{}, fmt.Errorf("%w: %s has sha256 %s; the rendering recorded %s", ErrEvidenceUnavailable, a.Path, got, a.SHA256)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return shot{}, fmt.Errorf("%w: %s is not a PNG: %v", ErrEvidenceUnavailable, a.Path, err)
	}
	return shot{art: a, img: img}, nil
}

func fmtRect(r layout.Rect) string {
	return fmt.Sprintf("(%.1f,%.1f %.1f×%.1f)", r.X, r.Y, r.W, r.H)
}
