import { render } from '@testing-library/react'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { ReactFlowProvider } from '@xyflow/react'
import type { GroupGraph } from '../../api/types'
import type { GroupLiveSummary } from '../groupLayout'
import type { Geometry } from './types'
import type { WorkflowDefinition } from '../types'
import { buildArrangedFlow, type ArrangedGroupElementData } from './buildArrangedFlow'
import ReviewedGroupElement from './ReviewedGroupElement'
import v2 from './groupGeometry.v2.json'

const fixture = v2[2] as unknown as { definition: WorkflowDefinition; geometry: Geometry; groups: GroupGraph }
const nodeTypes = Object.fromEntries(fixture.definition.nodes.map(n => [n.id, n.type]))

beforeAll(() => vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} }))

function live(): GroupLiveSummary {
  const tasks = fixture.groups.groups.fan.config.tasks
  return {
    instanceId: 'i', groupRunId: 'r', state: 'running',
    progress: { total: tasks.length, completed: 0, active: tasks.length, notStarted: 0, failed: 0, cancelled: 0 },
    tasks: Object.fromEntries(tasks.map(t => [t.key, { key: t.key, status: 'running', frozenStatus: '', childInstanceId: null,
      blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false, winner: false, activeNodes: [] }])),
  }
}

function mount(role: 'footer' | 'task', summary: GroupLiveSummary) {
  const onOverlayOverflow = vi.fn()
  const { nodes } = buildArrangedFlow(fixture.geometry, nodeTypes, {}, 'i', new Set(), { groups: fixture.groups, groupLive: { fan: summary }, onOverlayOverflow })
  const data = nodes.map(n => n.data.reviewed as ArrangedGroupElementData | undefined).find(r => r?.element.role === role)!
  const { container } = render(<ReactFlowProvider><ReviewedGroupElement data={data} /></ReactFlowProvider>)
  return { container, onOverlayOverflow }
}

// Paired row 3: a live value outside the reviewed vocabulary has no measured row,
// so the reviewed tile must be reported (route fallback), never drawn unmeasured.
describe('GroupOverlay — live values outside the reviewed vocabulary', () => {
  it('draws a supported live value within its reserved row without reporting', () => {
    const { container, onOverlayOverflow } = mount('footer', live())
    expect(container.querySelector('[data-group-text$=\'overlay:status:running"]\']')?.textContent).toBe('running')
    expect(onOverlayOverflow).not.toHaveBeenCalled()
  })

  const cases: Array<[string, 'footer' | 'task', (s: GroupLiveSummary) => void]> = [
    ['an unprobed run state', 'footer', s => { s.state = 'suspended' }],
    ['a progress count above the roster', 'footer', s => { s.progress!.completed = 99 }],
    ['an unmeasured winner', 'footer', s => { s.winner = 'not-a-task' }],
    ['an unprobed task status', 'task', s => { for (const t of Object.values(s.tasks)) t.status = 'skipped' }],
    ['an unmeasured frozen status', 'task', s => { for (const t of Object.values(s.tasks)) t.frozenStatus = 'unknown' }],
  ]
  for (const [name, role, mutate] of cases) {
    it(`reports and hides ${name}`, () => {
      const s = live()
      mutate(s)
      const { container, onOverlayOverflow } = mount(role, s)
      expect(onOverlayOverflow).toHaveBeenCalled()
      expect((container.querySelector('[data-group-overlay]') as HTMLElement).style.visibility).toBe('hidden')
    })
  }
})
