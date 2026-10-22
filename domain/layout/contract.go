package layout

// ContractVersion identifies the renderer behaviour used to arrange and review geometry.
const ContractVersion = "arranged-renderer/2"

// RendererContract pins the typography, spacing and stroke policy shared with
// dashboard/src/graph/arranged/contract.json.
type RendererContract struct {
	Version              string  `json:"version"`
	FontFamily           string  `json:"fontFamily"`
	FontPackage          string  `json:"fontPackage"`
	NodeLabelFontPx      float64 `json:"nodeLabelFontPx"`
	NodeLabelWeight      int     `json:"nodeLabelWeight"`
	NodeTypeFontPx       float64 `json:"nodeTypeFontPx"`
	NodeTypeWeight       int     `json:"nodeTypeWeight"`
	EdgeLabelFontPx      float64 `json:"edgeLabelFontPx"`
	EdgeLabelWeight      int     `json:"edgeLabelWeight"`
	LineHeight           float64 `json:"lineHeight"`
	NodePaddingX         float64 `json:"nodePaddingX"`
	NodePaddingY         float64 `json:"nodePaddingY"`
	NodeBorderPx         float64 `json:"nodeBorderPx"`
	NodeTypeBarPx        float64 `json:"nodeTypeBarPx"`
	NodeTextGapPx        float64 `json:"nodeTextGapPx"`
	NodeMinWidth         float64 `json:"nodeMinWidth"`
	NodeTextMaxWidth     float64 `json:"nodeTextMaxWidth"`
	EdgeLabelPaddingX    float64 `json:"edgeLabelPaddingX"`
	EdgeLabelPaddingY    float64 `json:"edgeLabelPaddingY"`
	EdgeLabelMaxWidth    float64 `json:"edgeLabelMaxWidth"`
	StrokePx             float64 `json:"strokePx"`
	ArrowPx              float64 `json:"arrowPx"`
	CrossingTreatment    string  `json:"crossingTreatment"`
	CrossingGapPx        float64 `json:"crossingGapPx"`
	ForwardColor         string  `json:"forwardColor"`
	LoopReturnColor      string  `json:"loopReturnColor"`
	LoopReturnDash       string  `json:"loopReturnDash"`
	PracticalZoom        float64 `json:"practicalZoom"`
	MinReadableTextPx    float64 `json:"minReadableTextPx"`
	MinContrastRatio     float64 `json:"minContrastRatio"`
	MinClearancePx       float64 `json:"minClearancePx"`
	GroupPaddingPx       float64 `json:"groupPaddingPx"`
	GroupTaskGapPx       float64 `json:"groupTaskGapPx"`
	GroupRowGapPx        float64 `json:"groupRowGapPx"`
	GroupMinWidth        float64 `json:"groupMinWidth"`
	GroupHeaderMinHeight float64 `json:"groupHeaderMinHeight"`
	GroupFooterMinHeight float64 `json:"groupFooterMinHeight"`
	GroupTaskMinHeight   float64 `json:"groupTaskMinHeight"`
	GroupConnectorDash   string  `json:"groupConnectorDash"`
}

// DefaultContract returns the renderer contract of this build.
func DefaultContract() RendererContract {
	return RendererContract{
		Version:           ContractVersion,
		FontFamily:        "Inter, Noto Sans SC, Noto Emoji",
		FontPackage:       "@fontsource/inter@5;@fontsource/noto-sans-sc@5.3.1;@fontsource/noto-emoji@5.3.2",
		NodeLabelFontPx:   13,
		NodeLabelWeight:   600,
		NodeTypeFontPx:    10,
		NodeTypeWeight:    700,
		EdgeLabelFontPx:   11,
		EdgeLabelWeight:   500,
		LineHeight:        1.35,
		NodePaddingX:      12,
		NodePaddingY:      8,
		NodeBorderPx:      2,
		NodeTypeBarPx:     4,
		NodeTextGapPx:     2,
		NodeMinWidth:      180,
		NodeTextMaxWidth:  240,
		EdgeLabelPaddingX: 6,
		EdgeLabelPaddingY: 3,
		EdgeLabelMaxWidth: 160,
		StrokePx:          2,
		ArrowPx:           10,
		CrossingTreatment: "gap",
		CrossingGapPx:     10,
		ForwardColor:      "#8a8ab0",
		LoopReturnColor:   "#7c8cf5",
		LoopReturnDash:    "6 4",
		PracticalZoom:     0.9,
		MinReadableTextPx: 9,
		MinContrastRatio:  4.5,
		MinClearancePx:    4,
		GroupPaddingPx:    32, GroupTaskGapPx: 24, GroupRowGapPx: 48, GroupMinWidth: 200,
		GroupHeaderMinHeight: 80, GroupFooterMinHeight: 80, GroupTaskMinHeight: 96, GroupConnectorDash: "3 3",
	}
}
