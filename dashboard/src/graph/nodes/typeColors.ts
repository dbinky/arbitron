import type { NodeType } from '../../api/types'

/**
 * Accent colours keyed by workflow node type, shared by both node renderers.
 * `satisfies` rejects a key that is not a known NodeType (a typo); a type
 * without an entry falls back to DEFAULT_TYPE_COLOR.
 */
export const typeColors: Record<string, string> = {
  llm_call: '#7c8cf5',
  script: '#e67e22',
  tool_call: '#1abc9c',
  conditional: '#f39c12',
  transform: '#9b59b6',
  timer_delay: '#00bcd4',
  webhook_wait: '#e91e63',
  spawn_workflow: '#2ecc71',
  parallel_group: '#3498db',
} satisfies Partial<Record<NodeType, string>>

export const DEFAULT_TYPE_COLOR = '#888888'

export function typeAccent(nodeType: string): string {
  return typeColors[nodeType] ?? DEFAULT_TYPE_COLOR
}
