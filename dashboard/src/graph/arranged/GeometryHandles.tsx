import { Handle, Position } from '@xyflow/react'
import type { Attachment, Rect } from './types'

export interface GeometryHandle { id: string; type: 'source' | 'target'; attachment: Attachment }
const positions = { top: Position.Top, bottom: Position.Bottom, left: Position.Left, right: Position.Right }
/** Handles identify persisted endpoints; they never compute a route. */
export function GeometryHandles({ box, handles }: { box: Rect; handles: GeometryHandle[] }) {
  return <>{handles.map(h => <Handle key={h.id} id={h.id} type={h.type} position={positions[h.attachment.side]} isConnectable={false}
    style={{ left: h.attachment.point.x - box.x, top: h.attachment.point.y - box.y, right: 'auto', bottom: 'auto', opacity: 0, pointerEvents: 'none', width: 1, height: 1, minWidth: 0, minHeight: 0, border: 0 }} />)}</>
}

/** DOM IDs are graph-local, reversible UTF-8 identities, never lossy sanitizations. */
export function renderedID(graph: string, logical: string): string {
  return `${graph}-${btoa(String.fromCodePoint(...new TextEncoder().encode(logical))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')}`
}
