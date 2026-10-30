package layout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// FormatV1 identifies the serialised geometry shared by storage and renderers.
const FormatV1 = "arbitron.layout/v1"

// FormatV2 is the current stored geometry, including rendered element identity.
const FormatV2 = "arbitron.layout/v2"

// ErrInvalidGeometry wraps every structural validation failure.
var ErrInvalidGeometry = errors.New("layout: invalid geometry")

const geomEps = 0.01

// Point is a canvas coordinate in CSS px at zoom 1.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Rect is an axis-aligned box with a top-left origin.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Right is the x coordinate of the right edge.
func (r Rect) Right() float64 { return r.X + r.W }

// Bottom is the y coordinate of the bottom edge.
func (r Rect) Bottom() float64 { return r.Y + r.H }

// CenterX is the horizontal centre.
func (r Rect) CenterX() float64 { return r.X + r.W/2 }

// CenterY is the vertical centre.
func (r Rect) CenterY() float64 { return r.Y + r.H/2 }

// Inflate grows the rectangle on every side; negative d shrinks it.
func (r Rect) Inflate(d float64) Rect {
	return Rect{X: r.X - d, Y: r.Y - d, W: r.W + 2*d, H: r.H + 2*d}
}

// Overlaps tests strict interior overlap; touching edges do not overlap.
func (r Rect) Overlaps(o Rect) bool {
	return r.X < o.Right() && o.X < r.Right() && r.Y < o.Bottom() && o.Y < r.Bottom()
}

// Contains allows the geometry's 1/100 px coordinate tolerance. A zero-size
// rectangle can be used to test point containment.
func (r Rect) Contains(o Rect) bool {
	return finiteRect(r) && finiteRect(o) &&
		o.X >= r.X-geomEps && o.Y >= r.Y-geomEps &&
		o.Right() <= r.Right()+geomEps && o.Bottom() <= r.Bottom()+geomEps
}

// EdgeKind classifies how an edge occurrence is routed and drawn.
type EdgeKind string

const (
	EdgeForward    EdgeKind = "forward"
	EdgeLoopReturn EdgeKind = "loop_return"
	EdgeSelfLoop   EdgeKind = "self_loop"
)

// Side names a node boundary or gutter side.
type Side string

const (
	SideTop    Side = "top"
	SideBottom Side = "bottom"
	SideLeft   Side = "left"
	SideRight  Side = "right"
)

// Attachment is where an edge meets a node boundary.
type Attachment struct {
	NodeID    string `json:"nodeId"`
	ElementID string `json:"elementId,omitempty"`
	Side      Side   `json:"side"`
	Point     Point  `json:"point"`
}

// UnmarshalJSON distinguishes an explicitly empty synthetic endpoint owner
// from a missing ownership field. Both would otherwise decode to the same Go
// zero value, while the TypeScript wire boundary rejects the missing field.
func (a *Attachment) UnmarshalJSON(raw []byte) error {
	type wire Attachment
	var value wire
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var required struct {
		NodeID *string `json:"nodeId"`
	}
	if err := json.Unmarshal(raw, &required); err != nil {
		return err
	}
	if required.NodeID == nil {
		return fmt.Errorf("%w: attachment.nodeId is required", ErrInvalidGeometry)
	}
	*a = Attachment(value)
	return nil
}

// LabelBox includes the branch text and contract padding.
type LabelBox struct {
	Text  string `json:"text"`
	Box   Rect   `json:"box"`
	Lines int    `json:"lines"`
}

// NodeBox is one node's reviewed box and complete displayed text.
type NodeBox struct {
	ID          string `json:"id"`
	Box         Rect   `json:"box"`
	Text        string `json:"text"`
	TypeCaption string `json:"typeCaption"`
	TextLines   int    `json:"textLines"`
	Rank        int    `json:"rank"`
}

// EdgeRoute is one edge occurrence, identified by its manifest slice ordinal.
type EdgeRoute struct {
	Ordinal      int        `json:"ordinal"`
	Source       string     `json:"source"`
	Target       string     `json:"target"`
	Output       string     `json:"output,omitempty"`
	Label        string     `json:"label,omitempty"`
	Kind         EdgeKind   `json:"kind"`
	Side         Side       `json:"side,omitempty"`
	Lane         int        `json:"lane,omitempty"`
	Points       []Point    `json:"points"`
	SourceAttach Attachment `json:"sourceAttach"`
	TargetAttach Attachment `json:"targetAttach"`
	Text         *LabelBox  `json:"text,omitempty"`
}

// Crossing is a proper intersection; Under is drawn with a visible gap.
type Crossing struct {
	Point Point `json:"point"`
	Over  int   `json:"over"`
	Under int   `json:"under"`
}

// Geometry contains everything needed to reproduce one reviewed candidate.
// Runtime state may restyle it but never moves it.
type Geometry struct {
	Format          string           `json:"format"`
	Contract        RendererContract `json:"contract"`
	Options         Options          `json:"options"`
	Nodes           []NodeBox        `json:"nodes"`
	Edges           []EdgeRoute      `json:"edges"`
	Crossings       []Crossing       `json:"crossings"`
	GroupElements   []GroupElement   `json:"groupElements,omitempty"`
	GroupLanes      []GroupLane      `json:"groupLanes,omitempty"`
	GroupConnectors []GroupConnector `json:"groupConnectors,omitempty"`
	GroupCrossings  []GroupCrossing  `json:"groupCrossings,omitempty"`
	Canvas          Rect             `json:"canvas"`
}

// Validate checks structural coverage, identity, text, routes, attachments and
// canvas bounds. Glyph readability and semantic routing belong to preflight
// and rendered review, not to this stored-value boundary.
func (g Geometry) Validate(gr Graph) error {
	if err := gr.validateGroups(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidGeometry, err)
	}
	if g.Format != FormatV2 {
		return fmt.Errorf("%w: format %q", ErrInvalidGeometry, g.Format)
	}
	if len(gr.Nodes) == 0 {
		return fmt.Errorf("%w: empty graph", ErrInvalidGeometry)
	}
	if !positiveRect(g.Canvas) || g.Canvas.X != 0 || g.Canvas.Y != 0 {
		return fmt.Errorf("%w: canvas must be positive, finite and at origin", ErrInvalidGeometry)
	}
	if err := validateGeometryContract(g.Contract); err != nil {
		return err
	}
	if err := validateGeometryOptions(g.Options); err != nil {
		return err
	}
	if len(g.Nodes) != len(gr.Nodes) || len(g.Edges) != len(gr.Edges) {
		return fmt.Errorf("%w: node/edge coverage", ErrInvalidGeometry)
	}
	for i, n := range g.Nodes {
		want := gr.Nodes[i]
		if n.ID != want.ID || n.Text != want.Text || n.TypeCaption != want.TypeCaption {
			return fmt.Errorf("%w: node %d identity/text", ErrInvalidGeometry, i)
		}
		if !positiveRect(n.Box) || !g.Canvas.Contains(n.Box) || n.TextLines < 1 || n.Rank < 0 {
			return fmt.Errorf("%w: node %q box/lines/rank", ErrInvalidGeometry, n.ID)
		}
	}
	if err := validateGroupGeometry(gr, g); err != nil {
		return err
	}
	elementBoxes := groupElementBoxes(g)
	for i, e := range g.Edges {
		want := gr.Edges[i]
		if e.Ordinal != i || e.Ordinal != want.Ordinal || e.Source != want.Source || e.Target != want.Target || e.Output != want.Output || e.Label != want.Label {
			return fmt.Errorf("%w: edge %d identity", ErrInvalidGeometry, i)
		}
		sourceID, targetID := GroupElementID(e.Source, "header", ""), GroupElementID(e.Target, "header", "")
		if gr.Groups != nil && gr.Groups.Groups[e.Source] != nil && e.Output != "task" {
			sourceID = GroupElementID(e.Source, "footer", "")
		}
		if e.SourceAttach.ElementID != sourceID || e.TargetAttach.ElementID != targetID {
			return fmt.Errorf("%w: edge %d element attachments", ErrInvalidGeometry, i)
		}
		routeBoxes := map[string]Rect{e.Source: elementBoxes[sourceID], e.Target: elementBoxes[targetID]}
		if err := validateRoute(e, routeBoxes, g.Canvas); err != nil {
			return fmt.Errorf("%w: edge %d: %s", ErrInvalidGeometry, i, err)
		}
		if want.Text == "" {
			if e.Text != nil {
				return fmt.Errorf("%w: edge %d unexpected label", ErrInvalidGeometry, i)
			}
		} else if e.Text == nil || e.Text.Text != want.Text || e.Text.Lines < 1 || !positiveRect(e.Text.Box) || !g.Canvas.Contains(e.Text.Box) {
			return fmt.Errorf("%w: edge %d label text/box/lines", ErrInvalidGeometry, i)
		}
	}
	for i, c := range g.Crossings {
		if c.Over < 0 || c.Over >= len(g.Edges) || c.Under < 0 || c.Under >= len(g.Edges) || c.Over == c.Under || !containsPoint(g.Canvas, c.Point) {
			return fmt.Errorf("%w: crossing %d ordinals/point", ErrInvalidGeometry, i)
		}
		for _, prev := range g.Crossings[:i] {
			if c.Over == prev.Over && c.Under == prev.Under && samePoint(c.Point, prev.Point) {
				return fmt.Errorf("%w: crossing %d duplicate", ErrInvalidGeometry, i)
			}
		}
		if !interiorOnRoute(c.Point, g.Edges[c.Over].Points, true) || !interiorOnRoute(c.Point, g.Edges[c.Under].Points, false) {
			return fmt.Errorf("%w: crossing %d is not a proper horizontal/vertical intersection", ErrInvalidGeometry, i)
		}
	}
	return nil
}

// GeometryDigest binds evidence to the canonical JSON of the exact candidate.
// Non-finite values cannot be marshalled and have no digest.
func GeometryDigest(g Geometry) string {
	raw, err := json.Marshal(g)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Compatible requires structural validity and the entire current contract.
func Compatible(g Geometry, gr Graph) bool {
	return g.Validate(gr) == nil && g.Contract == DefaultContract()
}

func nearly(a, b float64) bool  { return math.Abs(a-b) <= geomEps }
func samePoint(a, b Point) bool { return nearly(a.X, b.X) && nearly(a.Y, b.Y) }
func finite(v float64) bool     { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func finitePoint(p Point) bool  { return finite(p.X) && finite(p.Y) }

func finiteRect(r Rect) bool {
	return finite(r.X) && finite(r.Y) && finite(r.W) && finite(r.H) && finite(r.Right()) && finite(r.Bottom())
}

func positiveRect(r Rect) bool { return finiteRect(r) && r.W > 0 && r.H > 0 }

func containsPoint(r Rect, p Point) bool {
	return finitePoint(p) && r.Contains(Rect{X: p.X, Y: p.Y})
}

// onSide excludes corners, where an attachment's side would be ambiguous.
func onSide(p Point, side Side, r Rect) bool {
	if !finitePoint(p) || !positiveRect(r) {
		return false
	}
	switch side {
	case SideTop:
		return nearly(p.Y, r.Y) && p.X > r.X && p.X < r.Right()
	case SideBottom:
		return nearly(p.Y, r.Bottom()) && p.X > r.X && p.X < r.Right()
	case SideLeft:
		return nearly(p.X, r.X) && p.Y > r.Y && p.Y < r.Bottom()
	case SideRight:
		return nearly(p.X, r.Right()) && p.Y > r.Y && p.Y < r.Bottom()
	default:
		return false
	}
}

func validateRoute(e EdgeRoute, boxes map[string]Rect, canvas Rect) error {
	if (e.Source == e.Target) != (e.Kind == EdgeSelfLoop) {
		return errors.New("self-loop kind/endpoints disagree")
	}
	sourceSide, targetSide := SideBottom, SideTop
	switch e.Kind {
	case EdgeForward:
		if e.Side != "" || e.Lane != 0 {
			return errors.New("forward side/lane")
		}
	case EdgeLoopReturn:
		if (e.Side != SideLeft && e.Side != SideRight) || e.Lane < 1 {
			return errors.New("loop-return side/lane")
		}
	case EdgeSelfLoop:
		if e.Side != SideRight || e.Lane < 0 {
			return errors.New("self-loop side/lane")
		}
		sourceSide, targetSide = SideRight, SideRight
	default:
		return errors.New("unknown kind")
	}
	if len(e.Points) < 2 {
		return errors.New("route needs at least two points")
	}
	for i, p := range e.Points {
		if !containsPoint(canvas, p) {
			return fmt.Errorf("point %d not finite/contained", i)
		}
		if i > 0 {
			a, b := quantizedPoint(e.Points[i-1]), quantizedPoint(p)
			if (a.X == b.X) == (a.Y == b.Y) {
				return fmt.Errorf("segment %d is zero-length or nonorthogonal", i-1)
			}
		}
	}
	for _, end := range []struct {
		attach Attachment
		id     string
		side   Side
		point  Point
	}{
		{e.SourceAttach, e.Source, sourceSide, e.Points[0]},
		{e.TargetAttach, e.Target, targetSide, e.Points[len(e.Points)-1]},
	} {
		box, ok := boxes[end.id]
		if !ok || end.attach.NodeID != end.id || end.attach.Side != end.side || !samePoint(end.attach.Point, end.point) || !onSide(end.attach.Point, end.side, box) {
			return fmt.Errorf("attachment to %q inconsistent/off-boundary", end.id)
		}
	}
	return nil
}

// quantizedPoint compares routes at the serialized 1/100 px precision without
// overflowing when a finite coordinate is too large to have fractional pixels.
func quantizedPoint(p Point) Point {
	round := func(v float64) float64 {
		if math.Abs(v) > math.MaxFloat64*geomEps {
			return v
		}
		return math.Round(v/geomEps) * geomEps
	}
	return Point{X: round(p.X), Y: round(p.Y)}
}

// interiorOnRoute requires an interior point of a segment of the given
// orientation, and excludes every route vertex (including bends/endpoints).
func interiorOnRoute(p Point, points []Point, horizontal bool) bool {
	for _, v := range points {
		if samePoint(v, p) {
			return false
		}
	}
	for i := 1; i < len(points); i++ {
		a, b := quantizedPoint(points[i-1]), quantizedPoint(points[i])
		if horizontal && a.Y == b.Y && nearly(p.Y, a.Y) && p.X > math.Min(a.X, b.X) && p.X < math.Max(a.X, b.X) {
			return true
		}
		if !horizontal && a.X == b.X && nearly(p.X, a.X) && p.Y > math.Min(a.Y, b.Y) && p.Y < math.Max(a.Y, b.Y) {
			return true
		}
	}
	return false
}

// arrowBounds includes the full destination triangle, not only its tip.
func arrowBounds(points []Point, size float64) Rect {
	tip, prev := points[len(points)-1], points[len(points)-2]
	marker := Rect{X: tip.X - size/2, Y: tip.Y - size, W: size, H: size}
	if tip.Y == prev.Y {
		marker = Rect{X: tip.X, Y: tip.Y - size/2, W: size, H: size}
		if tip.X > prev.X {
			marker.X = tip.X - size
		}
	} else if tip.Y < prev.Y {
		marker.Y = tip.Y
	}
	return marker
}

func validateGeometryContract(c RendererContract) error {
	for _, f := range []struct{ name, value string }{
		{"version", c.Version}, {"fontFamily", c.FontFamily}, {"fontPackage", c.FontPackage},
		{"crossingTreatment", c.CrossingTreatment}, {"forwardColor", c.ForwardColor},
		{"loopReturnColor", c.LoopReturnColor}, {"loopReturnDash", c.LoopReturnDash}, {"groupConnectorDash", c.GroupConnectorDash},
	} {
		if f.value == "" {
			return fmt.Errorf("%w: contract.%s is empty", ErrInvalidGeometry, f.name)
		}
	}
	for _, f := range []struct {
		name  string
		value float64
	}{
		{"groupPaddingPx", c.GroupPaddingPx}, {"groupTaskGapPx", c.GroupTaskGapPx}, {"groupRowGapPx", c.GroupRowGapPx}, {"groupMinWidth", c.GroupMinWidth}, {"groupHeaderMinHeight", c.GroupHeaderMinHeight}, {"groupFooterMinHeight", c.GroupFooterMinHeight}, {"groupTaskMinHeight", c.GroupTaskMinHeight},
		{"nodeLabelFontPx", c.NodeLabelFontPx}, {"nodeTypeFontPx", c.NodeTypeFontPx}, {"edgeLabelFontPx", c.EdgeLabelFontPx},
		{"lineHeight", c.LineHeight}, {"nodePaddingX", c.NodePaddingX}, {"nodePaddingY", c.NodePaddingY},
		{"nodeBorderPx", c.NodeBorderPx}, {"nodeTypeBarPx", c.NodeTypeBarPx}, {"nodeTextGapPx", c.NodeTextGapPx},
		{"nodeMinWidth", c.NodeMinWidth}, {"nodeTextMaxWidth", c.NodeTextMaxWidth},
		{"edgeLabelPaddingX", c.EdgeLabelPaddingX}, {"edgeLabelPaddingY", c.EdgeLabelPaddingY}, {"edgeLabelMaxWidth", c.EdgeLabelMaxWidth},
		{"strokePx", c.StrokePx}, {"arrowPx", c.ArrowPx}, {"crossingGapPx", c.CrossingGapPx},
		{"practicalZoom", c.PracticalZoom}, {"minReadableTextPx", c.MinReadableTextPx},
		{"minContrastRatio", c.MinContrastRatio}, {"minClearancePx", c.MinClearancePx},
		{"node label height", c.NodeLabelFontPx * c.LineHeight}, {"node type height", c.NodeTypeFontPx * c.LineHeight},
		{"edge label height", c.EdgeLabelFontPx * c.LineHeight},
		{"node label at zoom", c.NodeLabelFontPx * c.PracticalZoom}, {"node type at zoom", c.NodeTypeFontPx * c.PracticalZoom},
		{"edge label at zoom", c.EdgeLabelFontPx * c.PracticalZoom},
		{"node padded width", c.NodeTextMaxWidth + 2*c.NodePaddingX + 2*c.NodeBorderPx + c.NodeTypeBarPx},
		{"node padded height", (c.NodeLabelFontPx+c.NodeTypeFontPx)*c.LineHeight + c.NodeTextGapPx + 2*c.NodePaddingY + 2*c.NodeBorderPx},
		{"edge padded width", c.EdgeLabelMaxWidth + 2*c.EdgeLabelPaddingX},
		{"edge padded height", c.EdgeLabelFontPx*c.LineHeight + 2*c.EdgeLabelPaddingY},
	} {
		if !finite(f.value) || f.value <= 0 {
			return fmt.Errorf("%w: contract.%s must be finite/positive", ErrInvalidGeometry, f.name)
		}
	}
	for _, f := range []struct {
		name  string
		value int
	}{
		{"nodeLabelWeight", c.NodeLabelWeight}, {"nodeTypeWeight", c.NodeTypeWeight}, {"edgeLabelWeight", c.EdgeLabelWeight},
	} {
		if f.value < 100 || f.value > 900 {
			return fmt.Errorf("%w: contract.%s must be 100..900", ErrInvalidGeometry, f.name)
		}
	}
	if c.PracticalZoom > 1 || c.MinContrastRatio > 21 {
		return fmt.Errorf("%w: contract zoom/contrast out of range", ErrInvalidGeometry)
	}
	// 0 draws sharp corners (geometry stored before the field existed). The
	// bound is the crossing bend clearance preflight keeps; it does not by itself
	// keep a corner off a crossing (radius + gap half + bank exceeds it), the
	// renderer's per-bend clamp does.
	if !finite(c.CornerRadiusPx) || c.CornerRadiusPx < 0 || c.CornerRadiusPx > c.CrossingGapPx/2+c.StrokePx {
		return fmt.Errorf("%w: contract.cornerRadiusPx must be finite, nonnegative and within the crossing bend clearance", ErrInvalidGeometry)
	}
	return nil
}

func validateGeometryOptions(o Options) error {
	for _, f := range []struct {
		name  string
		value float64
	}{
		{"rankSpacing", o.RankSpacing}, {"nodeSpacing", o.NodeSpacing}, {"laneSeparation", o.LaneSeparation},
		{"gutterWidth", o.GutterWidth}, {"labelGap", o.LabelGap}, {"portSpacing", o.PortSpacing}, {"dummyWidth", o.DummyWidth},
	} {
		if !finite(f.value) || f.value <= 0 {
			return fmt.Errorf("%w: options.%s must be finite/positive", ErrInvalidGeometry, f.name)
		}
	}
	if !finite(o.NodeTextSlack) || o.NodeTextSlack < 0 || !finite(o.Margin) || o.Margin < 0 || o.CrossingSweeps < 0 {
		return fmt.Errorf("%w: options slack/margin/sweeps must be finite/nonnegative", ErrInvalidGeometry)
	}
	return nil
}
