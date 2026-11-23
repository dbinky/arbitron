import type { Edge, Node } from '@xyflow/react'
import { definitionNodeIdOf } from './groupLayout'
import type { HomeMove, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from './types'

export const SERVER_SIDE_COLOR = '#6b7280'
export const RUNNER_PALETTE: readonly string[] = ['#60a5fa', '#34d399', '#fbbf24', '#f87171', '#a78bfa', '#22d3ee', '#fb923c', '#f472b6']

export function fnv1a(s: string): number {
  let hash = 2166136261
  for (let i = 0; i < s.length; i++) hash = Math.imul(hash ^ s.charCodeAt(i), 16777619)
  return hash >>> 0
}

export function runnerColor(runnerId: string): string {
  return RUNNER_PALETTE[fnv1a(runnerId) % RUNNER_PALETTE.length]
}

export const SERVER_SIDE_NODE_TYPES: ReadonlySet<string> = new Set([
  'conditional', 'transform', 'timer_delay', 'webhook_wait', 'spawn_workflow', 'parallel_group',
])
export interface RunnerMark { runnerId: string | null; color: string; label: string; moved?: HomeMove }
export interface RunnerMarks {
  instanceId: string
  nodes: Readonly<Record<string, RunnerMark>>
  moveInto: Readonly<Record<string, readonly HomeMove[]>>
}
export type RunnerMarksByOwner = Readonly<Record<string, RunnerMarks>>

export function moveBadgeText(m: HomeMove): string {
  return `workspace moved ${m.from} → ${m.to}`
}

export function buildRunnerMarks(instanceId: string, def: WorkflowDefinition | null, trace: NodeExecution[], workspaceMoves: HomeMove[] | undefined): RunnerMarks {
  const nodes: Record<string, RunnerMark> = {}
  const moveInto: Record<string, HomeMove[]> = {}
  if (!def) return { instanceId, nodes, moveInto }
  const owned = trace.filter(e => e.instanceId === instanceId)
  for (const node of def.nodes) {
    if (SERVER_SIDE_NODE_TYPES.has(node.type)) {
      nodes[node.id] = { runnerId: null, color: SERVER_SIDE_COLOR, label: 'server' }
      continue
    }
    const latest = owned.filter(e => e.nodeId === node.id).sort((a, b) => b.attempt - a.attempt)[0]
    if (latest?.runnerId) nodes[node.id] = { runnerId: latest.runnerId, color: runnerColor(latest.runnerId), label: latest.runnerId }
  }
  // Time is not an execution identity: drain moves have no target job at all.
  const ordered = [...workspaceMoves ?? []].sort((a, b) => a.generation - b.generation)
  for (const move of ordered) {
    if (!move.targetJobId) continue
    const execution = owned.find(e => e.placementHistory?.some(p => p.jobId === move.targetJobId))
    if (!execution || (move.nodeId && move.nodeId !== execution.nodeId) || !nodes[execution.nodeId]?.runnerId) continue
    const history = moveInto[execution.nodeId] ??= []
    history.push(move)
    nodes[execution.nodeId] = { ...nodes[execution.nodeId], moved: move }
  }
  return { instanceId, nodes, moveInto }
}

export function applyRunnerMarks(nodes: Node[], edges: Edge[], marks: RunnerMarks | undefined): { nodes: Node[]; edges: Edge[] } {
  return marks ? applyRunnerMarksByOwner(nodes, edges, { [marks.instanceId]: marks }, marks.instanceId) : { nodes, edges }
}

export function applyRunnerMarksByOwner(nodes: Node[], edges: Edge[], marks: RunnerMarksByOwner, rootInstanceId: string): { nodes: Node[]; edges: Edge[] } {
  const ownerMarks = (node: Node) => {
    if (node.data.instanceId === null) return undefined
    // A branch may execute inside its parent's layout tile.
    const owner = node.data.instanceId ?? node.data.layoutInstanceId ?? (node.id === definitionNodeIdOf(node) ? rootInstanceId : undefined)
    return typeof owner === 'string' ? marks[owner] : undefined
  }
  const isStep = (node: Node) => node.type === 'arbitronNode' || node.type === 'arrangedNode'
  const byId = new Map(nodes.map(node => [node.id, node]))
  return {
    nodes: nodes.map(node => {
      const mark = isStep(node) ? ownerMarks(node)?.nodes[definitionNodeIdOf(node)] : undefined
      if (mark) return { ...node, data: { ...node.data, runnerMark: mark } }
      if (!isStep(node) || !('runnerMark' in node.data)) return node
      const data = { ...node.data }
      delete data.runnerMark
      return { ...node, data }
    }),
    edges: edges.map(edge => {
      const route = edge.data?.route as { kind?: string } | undefined
      if (edge.type === 'loopBackEdge' || (route?.kind && route.kind !== 'forward')) return edge
      const target = byId.get(edge.target)
      const moves = target && isStep(target) ? ownerMarks(target)?.moveInto[definitionNodeIdOf(target)] : undefined
      if (!moves?.length) return edge
      const badge = moves.map(moveBadgeText).join(' · ')
      return { ...edge,
        ...(edge.type === 'routedEdge' ? {} : { label: edge.label ? `${edge.label} · ${badge}` : badge }),
        data: { ...edge.data, moves, moveBadge: badge },
      }
    }),
  }
}
