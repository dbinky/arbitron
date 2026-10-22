import type { GroupGraph } from '../../api/types'
import type { Geometry } from './types'
import type { TextItem } from '../../render/job'
import { groupElementID } from './groupValidate'

export const GROUP_RUN_STATES = ['not_started', 'launching', 'running', 'decided', 'gathering', 'resolving_conflicts', 'done', 'unknown'] as const
export const GROUP_TASK_STATES = ['not_started', 'queued', 'running', 'waiting', 'paused', 'completed', 'failed', 'cancelled', 'unfinished', 'unknown'] as const
export const groupTextKey = (id: string, field: string) => JSON.stringify(['text', id, field])
const display = (s: string) => s.replace(/_/g, ' ')
/** Mirrors groupTextItems' finite runtime alternatives, also used for font coverage. */
export function groupOverlayItems(groups: GroupGraph | null | undefined, maxWidth: number): TextItem[] {
  const items: TextItem[] = []
  const add = (id: string, field: string, text: string, style: TextItem['style'] = 'node_type') => items.push({ key: groupTextKey(id, field), text, style, maxWidth })
  for (const gid of groups?.order ?? []) {
    const cfg = groups!.groups[gid].config, footer = groupElementID(gid, 'footer')
    for (const s of GROUP_RUN_STATES) add(footer, `overlay:status:${s}`, display(s))
    for (let n = 0; n <= cfg.tasks.length; n++) {
      add(footer, `overlay:progress:done:${n}`, `${n}/${cfg.tasks.length} done`, 'edge_label')
      for (const kind of ['active', 'not started', 'failed']) add(footer, `overlay:progress:${kind}:${n}`, `${n} ${kind}`, 'edge_label')
    }
    for (const task of cfg.tasks) {
      const id = groupElementID(gid, cfg.form === 'workflows' ? 'task' : 'lane', task.key)
      if (cfg.form === 'workflows') add(id, 'redactedTitle', 'hidden dependency', 'node_label')
      add(footer, `overlay:winner:${task.key}`, `winner: ${task.key}`)
      for (const s of GROUP_TASK_STATES) {
        add(id, `overlay:status:${s}`, display(s))
        if (s !== 'unknown') add(id, `overlay:frozen:${s}`, `gathered as ${display(s)}`, 'edge_label')
      }
      for (const badge of ['winner', 'non-blocking', 'cancel requested', 'hidden dependency']) add(id, `overlay:badge:${badge}`, badge)
    }
  }
  return items
}
export function geometryTexts(geo: Geometry, groups?: GroupGraph | null): string[] {
  return [...geo.nodes.flatMap(n => [n.text, n.typeCaption]), ...geo.edges.flatMap(e => e.text ? [e.text.text] : []), ...(geo.groupElements ?? []).flatMap(e => e.texts.map(t => t.text)), ...groupOverlayItems(groups, geo.contract.nodeTextMaxWidth).map(it => it.text)]
}
