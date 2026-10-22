import { describe, expect, it } from 'vitest'
import type { GroupScope } from '../../api/types'
import { groupContainerLabel } from '../groupLayout'
import { gatherText, ruleText, unfinishedText, stateText } from '../groupText'
import cases from './groupText.contract.json'
import wire from './groupGeometry.wire.json'
import v2 from './groupGeometry.v2.json'
import invalidV2 from './groupGeometry.v2.invalid.json'
import fields from './groupGeometry.fields.json'
import type {
  Attachment,
  Geometry,
  GroupConnector,
  GroupCrossing,
  GroupElement,
  GroupLane,
  GroupText,
  Side,
} from './types'
import { fixtureGeometry, fixtureWorkflow } from './testFixtures'
import { RENDERER_CONTRACT, resolveDisplayGeometry, validateGeometry } from './validate'

// The Go wire test decodes/re-encodes these same values through Geometry.
// These assignments require every mirrored field (including synthetic-only
// endpoint identity) without pretending that the fixture is an arranged graph.
const element: GroupElement = {
  ...wire.element,
  texts: wire.element.texts.map((text) => ({
    ...text,
    style: text.style as GroupText['style'],
  })),
}
const lane: GroupLane = wire.lane
const connector: GroupConnector = {
  ...wire.connector,
  sourceAttach: {
    ...wire.connector.sourceAttach,
    side: wire.connector.sourceAttach.side as Side,
  },
  targetAttach: {
    ...wire.connector.targetAttach,
    side: wire.connector.targetAttach.side as Side,
  },
}
const crossing: GroupCrossing = wire.crossing

type RequiredKeys<T> = {
  [K in keyof T]-?: object extends Pick<T, K> ? never : K
}[keyof T]
type OptionalKeys<T> = Exclude<keyof T, RequiredKeys<T>>
function wireFields<T>(
  required: Record<RequiredKeys<T>, true>,
  optional: Record<OptionalKeys<T>, true>,
) {
  return {
    required: Object.keys(required).sort(),
    optional: Object.keys(optional).sort(),
  }
}

describe('group geometry v2 contract', () => {
  it('accepts complete Go-produced ordinary/branches/workflows/nested/mixed fixtures', () => {
    expect(v2).toHaveLength(5)
    for (const fixture of v2 as unknown as Array<{
      definition: import('../types').WorkflowDefinition
      groups: import('../../api/types').GroupGraph | null
      geometry: Geometry
    }>) {
      expect(fixture.geometry.format).toBe('arbitron.layout/v2')
      expect(fixture.geometry.contract).toEqual(RENDERER_CONTRACT)
      // The public boundary must consume the captured ownership, not just boxes.
      const validate = validateGeometry
      expect(
        validate(
          fixture.definition,
          fixture.geometry,
          RENDERER_CONTRACT,
          fixture.groups,
        ),
      ).toEqual({ ok: true, issues: [] })
      if (fixture.groups) {
        const stale = structuredClone(fixture.groups)
        stale.groups[stale.order[0]].config.tasks.reverse()
        expect(
          validate(
            fixture.definition,
            fixture.geometry,
            RENDERER_CONTRACT,
            stale,
          ).ok,
        ).toBe(false)
        expect(
          validate(
            fixture.definition,
            fixture.geometry,
            RENDERER_CONTRACT,
            null,
          ).ok,
        ).toBe(false)
        for (const field of [
          'groupElements',
          'groupLanes',
          'groupConnectors',
          'groupCrossings',
        ] as const) {
          for (let i = 0; i < (fixture.geometry[field]?.length ?? 0); i++) {
            const bad = structuredClone(fixture.geometry)
            bad[field]!.splice(i, 1)
            expect(
              validate(
                fixture.definition,
                bad,
                RENDERER_CONTRACT,
                fixture.groups,
              ).ok,
            ).toBe(false)
            for (const key of Object.keys(fixture.geometry[field]![i])) {
              const badField = structuredClone(fixture.geometry)
              delete (
                badField[field]![i] as unknown as Record<string, unknown>
              )[key]
              expect(
                validate(
                  fixture.definition,
                  badField,
                  RENDERER_CONTRACT,
                  fixture.groups,
                ).ok,
                `${field}/${i}/${key}`,
              ).toBe(false)
            }
          }
        }
      }
    }
  })
})

describe('shared rejected group geometry (domain/layout TestGroupGeometry_SharedV2InvalidFixtures)', () => {
  type Fixture = { definition: import('../types').WorkflowDefinition; groups: unknown; geometry: Geometry }
  const display = (f: Fixture) => resolveDisplayGeometry(f.definition, {
    savedRevision: f.definition.presentation?.revision ?? '', displayStatus: 'complete',
    geometry: f.geometry, groups: f.groups,
  } as unknown as import('./types').LayoutView)

  it('resolves every untampered fixture to its reviewed geometry', () => {
    for (const f of v2 as unknown as Fixture[]) expect(display(f)).toEqual({ geometry: f.geometry, issues: [] })
  })

  it.each(invalidV2.map(c => [c.name, c] as const))('%s falls back instead of drawing a reviewed scene', (_, c) => {
    const doc = structuredClone(v2[c.fixture]) as unknown as Record<string, unknown>
    for (const { path, value } of c.set) {
      let at: Record<string | number, unknown> = doc
      for (const key of path.slice(0, -1)) at = at[key] as Record<string | number, unknown>
      at[path[path.length - 1]] = structuredClone(value)
    }
    const resolved = display(doc as unknown as Fixture)
    expect(resolved.geometry).toBeNull()
    expect(resolved.issues.length).toBeGreaterThan(0)
  })
})

describe('group geometry wire identities', () => {
  it('matches every Go field and omitempty tag, not just sample values', () => {
    expect({
      groupText: wireFields<GroupText>(
        { key: true, text: true, style: true, box: true, lines: true },
        {},
      ),
      groupElement: wireFields<GroupElement>(
        { id: true, groupNodeId: true, role: true, box: true, texts: true },
        { taskKey: true, overlayBox: true },
      ),
      groupLane: wireFields<GroupLane>(
        { groupNodeId: true, taskKey: true, box: true, nodeIds: true },
        {},
      ),
      groupConnector: wireFields<GroupConnector>(
        {
          id: true,
          groupNodeId: true,
          taskKey: true,
          kind: true,
          sourceElementId: true,
          targetElementId: true,
          points: true,
          sourceAttach: true,
          targetAttach: true,
        },
        {},
      ),
      groupCrossing: wireFields<GroupCrossing>(
        { point: true, over: true, under: true },
        {},
      ),
      attachment: wireFields<Attachment>(
        { nodeId: true, side: true, point: true },
        { elementId: true },
      ),
      geometry: wireFields<Geometry>(
        {
          format: true,
          contract: true,
          options: true,
          nodes: true,
          edges: true,
          crossings: true,
          canvas: true,
        },
        {
          groupElements: true,
          groupLanes: true,
          groupConnectors: true,
          groupCrossings: true,
        },
      ),
    }).toEqual(fields)
  })
  it('does not bless group additions that the v1 renderer cannot draw', () => {
    for (const addition of [
      { groupElements: [element] },
      { groupLanes: [lane] },
      { groupConnectors: [connector] },
      { groupCrossings: [crossing] },
    ]) {
      const result = validateGeometry(
        fixtureWorkflow(),
        { ...fixtureGeometry(), ...addition },
        RENDERER_CONTRACT,
      )
      expect(result.ok).toBe(false)
      expect(result.issues.join('; ')).toMatch(/group/i)
    }
    const geometry = fixtureGeometry()
    geometry.edges[0].sourceAttach.elementId = 'wrong-element'
    expect(
      validateGeometry(fixtureWorkflow(), geometry, RENDERER_CONTRACT).ok,
    ).toBe(false)
  })
  it('shares collision-free element, text and connector identities with Go', () => {
    expect(element.texts[0].style).toBe('node_label')
    expect(connector.sourceAttach.side).toBe('bottom')
    expect(connector.targetAttach.side).toBe('top')
    expect(element.id).toBe(
      JSON.stringify([
        'group',
        element.groupNodeId,
        element.role,
        element.taskKey,
      ]),
    )
    expect(element.texts[0].key).toBe(
      JSON.stringify(['text', element.id, 'title']),
    )
    expect(connector.id).toBe(
      JSON.stringify([
        'connector',
        connector.groupNodeId,
        connector.kind,
        connector.taskKey,
        connector.sourceElementId,
        connector.targetElementId,
      ]),
    )
    expect(connector.targetElementId).toBe(element.id)
    expect(connector.targetAttach.elementId).toBe(element.id)
    expect(connector.targetAttach.nodeId).toBe('')
    expect(connector.sourceAttach.elementId).toBe(
      JSON.stringify(['node', element.groupNodeId]),
    )
    expect(lane.box).toEqual(element.box)
    expect(crossing.under).toBe(connector.id)
    expect(crossing.over).toBe('edge:0')
  })
})

describe('shared group typography vocabulary', () => {
  it.each(cases)('$form / $completionRule / $unfinishedBehavior', (entry) => {
    const scope: GroupScope = {
      nodeId: 'fan',
      config: {
        form: entry.form as GroupScope['config']['form'],
        tasks: ['second', 'first'].map((key) =>
          entry.form === 'branches'
            ? { key, entry: key }
            : { key, workflow: entry.workflow },
        ),
        completionRule:
          entry.completionRule as GroupScope['config']['completionRule'],
        unfinishedBehavior:
          entry.unfinishedBehavior as GroupScope['config']['unfinishedBehavior'],
        gatherMode: entry.gatherMode as GroupScope['config']['gatherMode'],
        resolverProfile:
          entry.gatherMode === 'results_and_files' ? 'resolver' : undefined,
      },
      bodies: {},
      nested: [],
      successTarget: 'done',
      failureTarget: 'oops',
    }
    expect(groupContainerLabel(scope)).toBe(entry.caption)
    expect(ruleText(scope.config.completionRule)).toBe(entry.rule)
    expect(unfinishedText(scope.config.unfinishedBehavior)).toBe(
      entry.unfinished,
    )
    expect(gatherText(scope.config.gatherMode)).toBe(entry.gather)
    expect(stateText('not_started')).toBe(entry.footerNotStarted)
  })
})
