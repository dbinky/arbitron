// Mirrors the JSON tags of domain/layout and domain/workflow/presentation.go.
import type { GroupGraph } from '../../api/types'
export interface Point {
  x: number
  y: number
}
export interface Rect {
  x: number
  y: number
  w: number
  h: number
}
export type EdgeKind = 'forward' | 'loop_return' | 'self_loop'
export type Side = 'top' | 'bottom' | 'left' | 'right'
export interface Attachment {
  nodeId: string
  elementId?: string
  side: Side
  point: Point
}
export interface LabelBox {
  text: string
  box: Rect
  lines: number
}
export interface NodeBox {
  id: string
  box: Rect
  text: string
  typeCaption: string
  textLines: number
  rank: number
}
export interface EdgeRoute {
  ordinal: number
  source: string
  target: string
  output?: string
  label?: string
  kind: EdgeKind
  side?: Side
  lane?: number
  points: Point[]
  sourceAttach: Attachment
  targetAttach: Attachment
  text?: LabelBox
}
export interface Crossing {
  point: Point
  over: number
  under: number
}
export interface RendererContract {
  version: string
  fontFamily: string
  fontPackage: string
  nodeLabelFontPx: number
  nodeLabelWeight: number
  nodeTypeFontPx: number
  nodeTypeWeight: number
  edgeLabelFontPx: number
  edgeLabelWeight: number
  lineHeight: number
  nodePaddingX: number
  nodePaddingY: number
  nodeBorderPx: number
  nodeTypeBarPx: number
  nodeTextGapPx: number
  nodeMinWidth: number
  nodeTextMaxWidth: number
  edgeLabelPaddingX: number
  edgeLabelPaddingY: number
  edgeLabelMaxWidth: number
  strokePx: number
  arrowPx: number
  crossingTreatment: string
  crossingGapPx: number
  /** Radius (px) of the arc drawn at each interior bend: 0 (sharp) to crossingGapPx / 2 + strokePx. */
  cornerRadiusPx: number
  forwardColor: string
  loopReturnColor: string
  loopReturnDash: string
  practicalZoom: number
  minReadableTextPx: number
  minContrastRatio: number
  minClearancePx: number
  groupPaddingPx: number
  groupTaskGapPx: number
  groupRowGapPx: number
  groupMinWidth: number
  groupHeaderMinHeight: number
  groupFooterMinHeight: number
  groupTaskMinHeight: number
  groupConnectorDash: string
}
export interface GroupText {
  key: string
  text: string
  style: 'node_label' | 'node_type' | 'edge_label'
  box: Rect
  lines: number
}
export interface GroupElement {
  id: string
  groupNodeId: string
  taskKey?: string
  role: string
  box: Rect
  texts: GroupText[]
  overlayBox?: Rect
}
export interface GroupLane {
  groupNodeId: string
  taskKey: string
  box: Rect
  nodeIds: string[]
}
export interface GroupConnector {
  id: string
  groupNodeId: string
  taskKey: string
  kind: string
  sourceElementId: string
  targetElementId: string
  points: Point[]
  sourceAttach: Attachment
  targetAttach: Attachment
}
export interface GroupCrossing {
  point: Point
  over: string
  under: string
}
export interface Geometry {
  format: string
  contract: RendererContract
  options: Record<string, number>
  nodes: NodeBox[]
  edges: EdgeRoute[]
  crossings: Crossing[]
  groupElements?: GroupElement[]
  groupLanes?: GroupLane[]
  groupConnectors?: GroupConnector[]
  groupCrossings?: GroupCrossing[]
  canvas: Rect
}
export type DisplayStatus =
  | 'pending'
  | 'arranging'
  | 'rendering'
  | 'reviewing'
  | 'complete'
  | 'needs_attention'
  | 'failed'
  | 'legacy'
  | 'unavailable'
export type Provenance = 'arranged' | 'retained_geometry' | 'retained_positions' | 'legacy'
export interface LayoutFinding {
  criterion: string
  severity: 'blocking' | 'advisory'
  source: 'preflight' | 'review'
  nodeIds?: string[]
  edgeOrdinals?: number[]
  evidence: string
}
export interface RetainedLayout {
  source: 'layout' | 'manifest' | 'none'
  fromRevision?: string
  fromGeneration?: number
  positions?: Record<string, Point>
}
export interface LayoutEvidence {
  geometryDigest: string
  rendererVersion: string
  contract: RendererContract
  fontsReady: boolean
  zoom: number
  tiles: number
  artifacts: Array<{
    kind: string
    path: string
    sha256: string
    region: Rect
    scale: number
  }>
  reviewedAt: string
}
export interface LayoutView {
  savedRevision: string
  jobGeneration: number
  resultGeneration: number
  state?: string
  displayStatus: DisplayStatus
  stage?: string
  attemptsUsed: number
  attemptLimit: number
  findings: LayoutFinding[] | null
  failureStage?: string
  failureReason?: string
  evidence?: LayoutEvidence
  contract?: RendererContract
  geometry?: Geometry
  groups?: GroupGraph
  retained?: RetainedLayout
  provenance: Provenance
  unreviewed: boolean
  retryEligible: boolean
  available: boolean
  availabilityError?: string
}
export interface RetryAck {
  savedRevision: string
  jobGeneration: number
  state: string
  displayStatus: DisplayStatus
}
export interface Presentation {
  revision: string
  savedAt: string
  reason: string
  scope: string
  ownerId?: string
  name: string
  retained: RetainedLayout
}

/** Poll while the server is still working on this revision. */
export const ACTIVE_STATUSES: ReadonlySet<DisplayStatus> = new Set<DisplayStatus>([
  'pending',
  'arranging',
  'rendering',
  'reviewing',
])
