package layout

import (
	"context"
	"slices"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
)

// RuntimeStates are the execution/selection styles exercised by rendered review.
// Styling must not resize nodes or obscure labels and edge endpoints.
var RuntimeStates = []string{"pending", "running", "completed", "failed", "waiting", "skipped", "selected"}

// RenderRequest asks the renderer to draw a candidate and probe runtime styles.
type RenderRequest struct {
	Revision          string               `json:"revision"`
	Attempt           int                  `json:"attempt"`
	Geometry          Geometry             `json:"geometry"`
	WorkflowNodeTypes map[string]string    `json:"workflowNodeTypes"`
	WorkflowGroups    *workflow.GroupGraph `json:"workflowGroups,omitempty"`
	States            []string             `json:"states"`
}

// NewRenderRequest copies raw manifest node types and the runtime state list.
func NewRenderRequest(revision string, attempt int, geo Geometry, nodeTypes map[string]string) RenderRequest {
	copied := make(map[string]string, len(nodeTypes))
	for id, typ := range nodeTypes {
		copied[id] = typ
	}
	return RenderRequest{Revision: revision, Attempt: attempt, Geometry: geo, WorkflowNodeTypes: copied, States: append([]string(nil), RuntimeStates...)}
}

// RenderedSegment is an actual SVG path segment, preserving crossing gaps.
type RenderedSegment struct {
	Start Point `json:"start"`
	End   Point `json:"end"`
}

// RenderedText describes actual DOM text and Range bounds, not expected text.
// Coordinates are canvas CSS px at zoom 1.
type RenderedText struct {
	Content  string  `json:"content"`
	Glyphs   []Rect  `json:"glyphs"`
	Visible  bool    `json:"visible"`
	Box      Rect    `json:"box"`
	Clipped  bool    `json:"clipped"`
	FontPx   float64 `json:"fontPx"`
	Contrast float64 `json:"contrast"`
	Occluded bool    `json:"occluded"`
}

// RenderedNode is the rendered node box and its two text elements.
type RenderedNode struct {
	ID    string       `json:"id"`
	Box   Rect         `json:"box"`
	Label RenderedText `json:"label"`
	Type  RenderedText `json:"type"`
}

// RenderedEdge describes the actual path, computed styles and marker outline
// of one edge occurrence.
type RenderedEdge struct {
	Segments    []RenderedSegment `json:"segments"`
	StrokeColor string            `json:"strokeColor"`
	DashPattern string            `json:"dashPattern"`
	Visible     bool              `json:"visible"`
	ArrowPoints []Point           `json:"arrowPoints"`
	Ordinal     int               `json:"ordinal"`
	Start       Point             `json:"start"`
	End         Point             `json:"end"`
	HasArrow    bool              `json:"hasArrow"`
	Dashed      bool              `json:"dashed"`
	StrokePx    float64           `json:"strokePx"`
	Label       *RenderedText     `json:"label,omitempty"`
}

// RenderedCrossing reports whether the under edge is visibly interrupted
// while the over edge remains continuous.
type RenderedCrossing struct {
	Point           Point `json:"point"`
	Over            int   `json:"over"`
	Under           int   `json:"under"`
	UnderGapVisible bool  `json:"underGapVisible"`
	OverContinuous  bool  `json:"overContinuous"`
}

// RenderedGroupText binds an actual text observation to its logical identity.
type RenderedGroupText struct {
	Key  string       `json:"key"`
	Text RenderedText `json:"text"`
}

// RenderedGroupElement records actual DOM bounds, text and computed paint.
type RenderedGroupElement struct {
	ID              string              `json:"id"`
	Box             Rect                `json:"box"`
	Texts           []RenderedGroupText `json:"texts"`
	Visible         bool                `json:"visible"`
	BorderColor     string              `json:"borderColor"`
	BorderPx        float64             `json:"borderPx"`
	BackgroundColor string              `json:"backgroundColor"`
}

// RenderedGroupConnector has no manifest edge ordinal.
type RenderedGroupConnector struct {
	ID          string            `json:"id"`
	Segments    []RenderedSegment `json:"segments"`
	StrokeColor string            `json:"strokeColor"`
	DashPattern string            `json:"dashPattern"`
	Visible     bool              `json:"visible"`
	ArrowPoints []Point           `json:"arrowPoints"`
	Start       Point             `json:"start"`
	End         Point             `json:"end"`
	HasArrow    bool              `json:"hasArrow"`
	StrokePx    float64           `json:"strokePx"`
}

// RenderedGroupCrossing uses real or synthetic route keys.
type RenderedGroupCrossing struct {
	Point           Point  `json:"point"`
	Over            string `json:"over"`
	Under           string `json:"under"`
	UnderGapVisible bool   `json:"underGapVisible"`
	OverContinuous  bool   `json:"overContinuous"`
}

// StateProbe reports the result of re-rendering in one runtime state.
type StateProbe struct {
	State           string   `json:"state"`
	NodeBoxesStable bool     `json:"nodeBoxesStable"`
	LabelsReadable  bool     `json:"labelsReadable"`
	Issues          []string `json:"issues,omitempty"`
}

// RenderReport is the DOM measurement report emitted by the render surface.
type RenderReport struct {
	SurfaceVersion     string                   `json:"surfaceVersion"`
	Contract           RendererContract         `json:"contract"`
	FontsReady         bool                     `json:"fontsReady"`
	FontFamilyResolved bool                     `json:"fontFamilyResolved"`
	Zoom               float64                  `json:"zoom"`
	Canvas             Rect                     `json:"canvas"`
	Nodes              []RenderedNode           `json:"nodes"`
	Edges              []RenderedEdge           `json:"edges"`
	Crossings          []RenderedCrossing       `json:"crossings"`
	States             []StateProbe             `json:"states"`
	GroupElements      []RenderedGroupElement   `json:"groupElements,omitempty"`
	GroupConnectors    []RenderedGroupConnector `json:"groupConnectors,omitempty"`
	GroupCrossings     []RenderedGroupCrossing  `json:"groupCrossings,omitempty"`
}

// Artifact identifies an overview screenshot or readable-scale tile.
type Artifact struct {
	Kind   string  `json:"kind"`
	Path   string  `json:"path"`
	SHA256 string  `json:"sha256"`
	Region Rect    `json:"region"`
	Scale  float64 `json:"scale"`
}

// Rendering is the renderer's output for one candidate.
type Rendering struct {
	Revision       string       `json:"revision"`
	Attempt        int          `json:"attempt"`
	GeometryDigest string       `json:"geometryDigest"`
	Report         RenderReport `json:"report"`
	Artifacts      []Artifact   `json:"artifacts"`
	RenderedAt     time.Time    `json:"renderedAt"`
}

// ReviewRequest hands the reviewer one candidate and its own rendering.
type ReviewRequest struct {
	Revision       string               `json:"revision"`
	Attempt        int                  `json:"attempt"`
	Geometry       Geometry             `json:"geometry"`
	Rendering      Rendering            `json:"rendering"`
	WorkflowGroups *workflow.GroupGraph `json:"workflowGroups,omitempty"`
}

// Evidence binds a review to the exact candidate and renderer examined.
type Evidence struct {
	GeometryDigest  string           `json:"geometryDigest"`
	RendererVersion string           `json:"rendererVersion"`
	Contract        RendererContract `json:"contract"`
	FontsReady      bool             `json:"fontsReady"`
	Zoom            float64          `json:"zoom"`
	Tiles           int              `json:"tiles"`
	Artifacts       []Artifact       `json:"artifacts"`
	ReviewedAt      time.Time        `json:"reviewedAt"`
}

// ForViewer is the copy served to API readers. Artifact paths are server
// filesystem locations that validation reads from the stored result; no viewer
// needs them, so they are cleared while kind, digest and region remain.
func (e Evidence) ForViewer() Evidence {
	e.Artifacts = slices.Clone(e.Artifacts)
	for i := range e.Artifacts {
		e.Artifacts[i].Path = ""
	}
	return e
}

// Review is the readability verdict. Rendered is false when a complete
// rendering could not be examined; missing evidence never establishes a pass.
type Review struct {
	Rendered bool      `json:"rendered"`
	Findings []Finding `json:"findings"`
	Evidence Evidence  `json:"evidence"`
}

// Renderer measures text and draws candidates with the dashboard's real
// components, CSS and fonts. Production implementation: BrowserRenderer.
type Renderer interface {
	Measure(ctx context.Context, req MeasureRequest) (Measurements, error)
	Render(ctx context.Context, req RenderRequest) (Rendering, error)
}

// Reviewer examines rendered evidence and returns readability findings.
// Production implementation: ReadabilityReviewer.
type Reviewer interface {
	Review(ctx context.Context, req ReviewRequest) (Review, error)
}
