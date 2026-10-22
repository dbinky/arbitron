import type { GroupGraph } from '../../api/types'
import type { WorkflowDefinition } from '../types'
import { groupContainerLabel } from '../groupLayout'
import { gatherText, ruleText, unfinishedText } from '../groupText'
import type { Geometry, GroupElement, Rect } from './types'

const object = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)
const own = (o: object, k: PropertyKey) =>
  Object.prototype.hasOwnProperty.call(o, k)
const equal = (a: unknown, b: unknown): boolean => {
  if (Array.isArray(a) && Array.isArray(b))
    return a.length === b.length && a.every((v, i) => equal(v, b[i]))
  if (object(a) && object(b))
    return (
      Object.keys(a).length === Object.keys(b).length &&
      Object.keys(a).every((k) => own(b, k) && equal(a[k], b[k]))
    )
  return a === b
}
export const groupElementID = (id: string, role: string, key = '') =>
  JSON.stringify(
    role === 'header' && !key ? ['node', id] : ['group', id, role, key],
  )
const textKey = (id: string, field: string) =>
  JSON.stringify(['text', id, field])
const connectorID = (
  gid: string,
  kind: string,
  key: string,
  source: string,
  target: string,
) => JSON.stringify(['connector', gid, kind, key, source, target])

/** Checks captured membership/config identity, not a second reachability analysis. */
function validGroupOwnership(
  def: WorkflowDefinition,
  raw: unknown,
): raw is GroupGraph {
  const ids = def.nodes.map((n) => n.id),
    groupIds = def.nodes
      .filter((n) => n.type === 'parallel_group')
      .map((n) => n.id)
  if (!groupIds.length) return raw == null
  if (
    !object(raw) ||
    !object(raw.groups) ||
    !object(raw.owner) ||
    !equal(raw.order, groupIds) ||
    Object.keys(raw.groups).length !== groupIds.length
  )
    return false
  const groups = raw as unknown as GroupGraph,
    owners = new Map<string, { groupNodeId: string; taskKey: string }>()
  for (const id of groupIds) {
    if (!own(groups.groups, id)) return false
    const s = groups.groups[id]
    if (
      !object(s) ||
      s.nodeId !== id ||
      !object(s.config) ||
      !object(s.bodies) ||
      !Array.isArray(s.nested)
    )
      return false
    const cfg = s.config,
      original = def.nodes.find((n) => n.id === id)?.config?.group
    if (
      !equal(cfg, original) ||
      !['branches', 'workflows'].includes(cfg.form) ||
      !['all_succeed', 'first_success'].includes(cfg.completionRule) ||
      ![
        'wait_for_remaining',
        'cancel_unfinished',
        'finish_independently',
      ].includes(cfg.unfinishedBehavior) ||
      !['results', 'results_and_files'].includes(cfg.gatherMode) ||
      !Array.isArray(cfg.tasks) ||
      !cfg.tasks.length
    )
      return false
    const keys = new Set<string>()
    for (const task of cfg.tasks) {
      if (
        !object(task) ||
        typeof task.key !== 'string' ||
        !task.key.trim() ||
        keys.has(task.key)
      )
        return false
      keys.add(task.key)
      if (cfg.form === 'workflows') {
        if (
          typeof task.workflow !== 'string' ||
          !task.workflow.trim() ||
          task.entry
        )
          return false
      } else {
        const body = s.bodies[task.key]
        if (
          typeof task.entry !== 'string' ||
          task.workflow ||
          !Array.isArray(body) ||
          !body.includes(task.entry) ||
          body.includes(id) ||
          !equal(
            body,
            ids.filter((n) => body.includes(n)),
          )
        )
          return false
        for (const node of body) {
          if (owners.has(node)) return false
          owners.set(node, { groupNodeId: id, taskKey: task.key })
        }
      }
    }
    if (
      cfg.form === 'workflows'
        ? Object.keys(s.bodies).length !== 0
        : Object.keys(s.bodies).length !== keys.size ||
          Object.keys(s.bodies).some((k) => !keys.has(k))
    )
      return false
    for (const [port, target] of [
      ['success', s.successTarget],
      ['failure', s.failureTarget],
    ]) {
      const edges = def.edges.filter(
        (e) => e.source === id && e.output === port,
      )
      if (edges.length !== 1 || edges[0].target !== target) return false
    }
    const edges = def.edges.filter(
      (e) => e.source === id && e.output === 'task',
    )
    if (
      cfg.form === 'workflows'
        ? edges.length !== 0
        : edges.length !== cfg.tasks.length ||
          cfg.tasks.some(
            (t) => edges.filter((e) => e.target === t.entry).length !== 1,
          )
    )
      return false
  }
  if (
    Object.keys(groups.owner).length !== owners.size ||
    Object.entries(groups.owner).some(([id, o]) => !equal(o, owners.get(id)))
  )
    return false
  for (const id of groupIds) {
    const s = groups.groups[id],
      parent = owners.get(id)
    if (parent ? !equal(s.parent, parent) : s.parent != null) return false
    if (
      !equal(
        s.nested,
        groupIds.filter((n) => owners.get(n)?.groupNodeId === id),
      )
    )
      return false
    const seen = new Set([id])
    let ancestor = parent?.groupNodeId
    while (ancestor) {
      if (seen.has(ancestor)) return false
      seen.add(ancestor)
      ancestor = owners.get(ancestor)?.groupNodeId
    }
  }
  return true
}

export function groupGeometryIssues(
  def: WorkflowDefinition,
  geo: Geometry,
  raw: unknown,
  validRect: (v: unknown) => v is Rect,
  contains: (a: Rect, b: Rect) => boolean,
): string[] {
  const fail = (s: string) => [`group geometry: ${s}`]
  if (!validGroupOwnership(def, raw))
    return fail('missing or mismatched ownership')
  if (raw == null) {
    return [
      'groupElements',
      'groupLanes',
      'groupConnectors',
      'groupCrossings',
    ].some((k) => {
      const a = (geo as unknown as Record<string, unknown>)[k]
      return a != null && (!Array.isArray(a) || a.length > 0)
    })
      ? fail('unexpected group arrays')
      : []
  }
  const groups = raw as GroupGraph
  if (
    !geo.edges.every(
      (e) =>
        object(e) &&
        typeof e.source === 'string' &&
        typeof e.target === 'string' &&
        Array.isArray(e.points),
    )
  )
    return fail('invalid edge routes')
  if (
    !geo.nodes.every(
      (n) => object(n) && typeof n.id === 'string' && validRect(n.box),
    )
  )
    return fail('invalid node boxes')
  if (
    !Array.isArray(geo.groupElements) ||
    !Array.isArray(geo.groupLanes) ||
    !Array.isArray(geo.groupConnectors) ||
    (geo.groupCrossings != null && !Array.isArray(geo.groupCrossings))
  )
    return fail('missing arrays')
  const elements = new Map<string, GroupElement>()
  for (const e of geo.groupElements) {
    if (!object(e) || typeof e.id !== 'string' || elements.has(e.id))
      return fail('duplicate/malformed element')
    elements.set(e.id, e)
  }
  let count = 0,
    lanes = 0
  const connectors: Array<{
    id: string
    groupNodeId: string
    taskKey: string
    kind: string
    sourceElementId: string
    targetElementId: string
  }> = []
  const add = (
    gid: string,
    key: string,
    kind: string,
    source: string,
    target: string,
  ) =>
    connectors.push({
      id: connectorID(gid, kind, key, source, target),
      groupNodeId: gid,
      taskKey: key,
      kind,
      sourceElementId: source,
      targetElementId: target,
    })
  for (const gid of groups.order) {
    const s = groups.groups[gid],
      cfg = s.config
    const roles: Array<[string, string, Array<[string, string, string]>]> = [
      ['container', '', [['caption', groupContainerLabel(s), 'edge_label']]],
      [
        'header',
        '',
        [
          ['fanout', 'fan-out', 'node_type'],
          [
            'metadata',
            `${cfg.tasks.length} ${cfg.tasks.length === 1 ? 'task' : 'tasks'} · ${cfg.form}`,
            'edge_label',
          ],
        ],
      ],
      [
        'footer',
        '',
        [
          ['title', `gather · ${ruleText(cfg.completionRule)}`, 'node_label'],
          [
            'metadata',
            `${unfinishedText(cfg.unfinishedBehavior)} · ${gatherText(cfg.gatherMode)}`,
            'edge_label',
          ],
        ],
      ],
    ]
    for (const [i, t] of cfg.tasks.entries())
      roles.push([
        cfg.form === 'branches' ? 'lane' : 'task',
        t.key,
        [
          ['caption', `task ${i + 1} · ${t.key}`, 'node_type'],
          ...(cfg.form === 'workflows'
            ? [['title', t.workflow!, 'node_label'] as [string, string, string]]
            : []),
        ],
      ])
    const container = elements.get(groupElementID(gid, 'container')),
      header = elements.get(groupElementID(gid, 'header')),
      footer = elements.get(groupElementID(gid, 'footer'))
    if (
      !container ||
      !header ||
      !footer ||
      !validRect(container.box) ||
      !validRect(header.box) ||
      !validRect(footer.box)
    )
      return fail('missing container/header/footer')
    for (const [role, key, texts] of roles) {
      count++
      const id = groupElementID(gid, role, key),
        e = elements.get(id)
      if (
        !e ||
        e.role !== role ||
        e.groupNodeId !== gid ||
        (e.taskKey ?? '') !== key ||
        !validRect(e.box) ||
        !validRect(geo.canvas) ||
        !contains(geo.canvas, e.box) ||
        !contains(container.box, e.box) ||
        !Array.isArray(e.texts) ||
        e.texts.length !== texts.length
      )
        return fail(`element ${id}`)
      for (const [i, [field, text, style]] of texts.entries()) {
        const t = e.texts[i]
        if (
          !object(t) ||
          t.key !== textKey(id, field) ||
          t.text !== text ||
          t.style !== style ||
          !Number.isInteger(t.lines) ||
          t.lines < 1 ||
          !validRect(t.box) ||
          !contains(e.box, t.box)
        )
          return fail(`text ${id}`)
      }
      const overlay = ['footer', 'lane', 'task'].includes(role)
      if (
        overlay
          ? !validRect(e.overlayBox) || !contains(e.box, e.overlayBox)
          : e.overlayBox != null
      )
        return fail(`live reservation ${id}`)
      if (
        role === 'header' &&
        !equal(e.box, geo.nodes.find((n) => n.id === gid)?.box)
      )
        return fail('header must share real node box')
    }
    const ownedLanes = geo.groupLanes.filter(
      (l) => object(l) && l.groupNodeId === gid,
    )
    if (ownedLanes.length !== cfg.tasks.length) return fail('lane coverage')
    let right = -Infinity
    for (const [i, t] of cfg.tasks.entries()) {
      lanes++
      const l = ownedLanes[i],
        role = cfg.form === 'branches' ? 'lane' : 'task',
        e = elements.get(groupElementID(gid, role, t.key))!
      if (
        l.taskKey !== t.key ||
        !Array.isArray(l.nodeIds) ||
        !equal(l.nodeIds, s.bodies[t.key] ?? []) ||
        !validRect(l.box) ||
        !contains(container.box, l.box) ||
        !contains(l.box, e.box) ||
        l.box.x < right ||
        l.box.y <= header.box.y + header.box.h ||
        l.box.y + l.box.h >= footer.box.y
      )
        return fail('lane ownership/order/box')
      right = l.box.x + l.box.w
      for (const node of l.nodeIds) {
        const box = own(groups.groups, node)
          ? elements.get(groupElementID(node, 'container'))?.box
          : geo.nodes.find((n) => n.id === node)?.box
        if (!validRect(box) || !contains(l.box, box))
          return fail('body outside owning lane')
      }
      const foot = groupElementID(gid, 'footer')
      if (cfg.form === 'workflows') {
        const card = groupElementID(gid, 'task', t.key)
        add(gid, t.key, 'fanout', groupElementID(gid, 'header'), card)
        add(gid, t.key, 'gather', card, foot)
      } else
        for (const node of s.bodies[t.key]) {
          if (
            !def.edges.some(
              (edge) =>
                edge.source === node &&
                (!own(groups.groups, node) || edge.output !== 'task'),
            )
          )
            add(
              gid,
              t.key,
              'sink',
              groupElementID(
                node,
                own(groups.groups, node) ? 'footer' : 'header',
              ),
              foot,
            )
        }
    }
  }
  if (
    elements.size !== count ||
    geo.groupLanes.length !== lanes ||
    geo.groupConnectors.length !== connectors.length
  )
    return fail('element/lane/connector coverage')
  const boxes = new Map(
    geo.nodes.map((n) => [groupElementID(n.id, 'header'), n.box]),
  )
  for (const e of elements.values()) boxes.set(e.id, e.box)
  for (const [i, c] of geo.groupConnectors.entries()) {
    const want = connectors[i]
    if (
      !object(c) ||
      Object.entries(want).some(
        ([k, v]) => (c as unknown as Record<string, unknown>)[k] !== v,
      ) ||
      !Array.isArray(c.points) ||
      c.points.length < 2
    )
      return fail('connector identity')
    for (const [a, id, side, p] of [
      [c.sourceAttach, c.sourceElementId, 'bottom', c.points[0]],
      [c.targetAttach, c.targetElementId, 'top', c.points[c.points.length - 1]],
    ] as const) {
      const box = boxes.get(id)
      if (
        !object(a) ||
        a.nodeId !== '' ||
        a.elementId !== id ||
        a.side !== side ||
        !equal(a.point, p) ||
        !box ||
        !p ||
        p.x <= box.x ||
        p.x >= box.x + box.w ||
        Math.abs(p.y - (side === 'bottom' ? box.y + box.h : box.y)) > 0.01
      )
        return fail('connector attachment')
    }
    for (const [j, p] of c.points.entries())
      if (
        !object(p) ||
        !Number.isFinite(p.x) ||
        !Number.isFinite(p.y) ||
        !contains(geo.canvas, { ...p, w: 0, h: 0 }) ||
        (j > 0 && (p.x === c.points[j - 1].x) === (p.y === c.points[j - 1].y))
      )
        return fail('connector route')
  }
  const routes = [
    ...geo.edges.map((e, i) => ({ key: `edge:${i}`, points: e.points })),
    ...geo.groupConnectors.map((e) => ({ key: e.id, points: e.points })),
  ]
  if (
    routes.some(
      (r) =>
        !Array.isArray(r.points) ||
        r.points.some(
          (p) => !object(p) || !Number.isFinite(p.x) || !Number.isFinite(p.y),
        ),
    )
  )
    return fail('invalid routes')
  const crossings: Array<{
    point: { x: number; y: number }
    over: number
    under: number
  }> = []
  const seen = new Set<string>()
  const interior = (
    p: { x: number; y: number },
    a: { x: number; y: number },
    b: { x: number; y: number },
  ) =>
    a.y === b.y
      ? p.x > Math.min(a.x, b.x) + 0.01 && p.x < Math.max(a.x, b.x) - 0.01
      : p.y > Math.min(a.y, b.y) + 0.01 && p.y < Math.max(a.y, b.y) - 0.01
  for (let i = 0; i < routes.length; i++)
    for (let j = i + 1; j < routes.length; j++) {
      if (i < geo.edges.length && j < geo.edges.length) continue
      const a = routes[i].points,
        b = routes[j].points
      for (let u = 1; u < a.length; u++)
        for (let v = 1; v < b.length; v++) {
          const ah = a[u - 1].y === a[u].y,
            bh = b[v - 1].y === b[v].y
          if (ah === bh) continue
          const p = { x: ah ? b[v].x : a[u].x, y: ah ? a[u].y : b[v].y }
          if (!interior(p, a[u - 1], a[u]) || !interior(p, b[v - 1], b[v]))
            continue
          const c = { point: p, over: ah ? i : j, under: ah ? j : i },
            key = JSON.stringify(c)
          if (!seen.has(key)) {
            seen.add(key)
            crossings.push(c)
          }
        }
    }
  crossings.sort(
    (a, b) =>
      a.under - b.under ||
      a.point.y - b.point.y ||
      a.point.x - b.point.x ||
      a.over - b.over,
  )
  if (
    !equal(
      geo.groupCrossings ?? [],
      crossings.map((c) => ({
        point: c.point,
        over: routes[c.over].key,
        under: routes[c.under].key,
      })),
    )
  )
    return fail('mixed/connector crossing coverage')
  return []
}
