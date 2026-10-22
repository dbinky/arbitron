import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api } from './client'

describe('api.system.updateTeamsConfig', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ enabled: true, webhookUrl: '**REDACTED**' }),
    } as Response)
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  // Success: PUTs both fields to the teams endpoint.
  it('PUTs webhookUrl + enabled to /api/v1/system/config/teams', async () => {
    await api.system.updateTeamsConfig({ webhookUrl: 'https://x.example.com', enabled: true })

    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/system/config/teams',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ webhookUrl: 'https://x.example.com', enabled: true }),
      }),
    )
  })

  // Edge-case: a toggle-only body omits webhookUrl entirely.
  it('omits webhookUrl when not provided', async () => {
    await api.system.updateTeamsConfig({ enabled: false })

    const init = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1] as RequestInit
    expect(JSON.parse(init.body as string)).toEqual({ enabled: false })
  })
})

describe('api.instances dismissal', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: 'ok' }),
    } as Response)
  })
  afterEach(() => vi.restoreAllMocks())

  it('POSTs dismiss to /instances/{id}/dismiss', async () => {
    await api.instances.dismiss('abc')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/abc/dismiss',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('POSTs restore to /instances/{id}/restore', async () => {
    await api.instances.restore('abc')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/abc/restore',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('POSTs dismissAll with the state query param', async () => {
    await api.instances.dismissAll('failed')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/dismiss-all?state=failed',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('POSTs dismissAll with a since window when provided', async () => {
    await api.instances.dismissAll('completed', '2026-01-01T00:00:00.000Z')
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain('state=completed')
    expect(url).toContain('since=')
  })

  it('list includes includeDismissed=true when requested', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ state: 'failed', includeDismissed: true })
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain('includeDismissed=true')
    expect(url).toContain('state=failed')
  })

  it('list omits includeDismissed when false/absent', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ state: 'failed' })
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).not.toContain('includeDismissed')
  })

  it('list narrows by parent when requested, and omits it otherwise', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ parent: 'p1' })
    await api.instances.list({ state: 'failed' })
    const calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls
    expect(calls[0][0] as string).toContain('parent=p1')
    expect(calls[1][0] as string).not.toContain('parent')
  })

  // ListInstancesParams.parent's own doc comment documents THREE wire values
  // (omit, 'roots', an instance UUID) matching domain/instance/spawn_link.go's
  // ParseParentScope, but only "omit" and "a UUID" were ever exercised above —
  // the literal 'roots' scope was undocumented-by-test at the client boundary.
  it('list sends the literal roots scope verbatim', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ parent: 'roots' })
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain('parent=roots')
  })

  // Regression guard for the hierarchy fields' wire contract: the server emits
  // these key names (adapter/http/instance_handler.go's enrichedSummaryDTO),
  // and TypeScript's unchecked `res.json() as T` cast cannot itself catch a
  // typo'd field name in types.ts — only a test built from raw string-keyed
  // JSON, not a `row()`-style typed object literal, can.
  it('list surfaces parentInstanceId and childCount from a raw wire-format response verbatim', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => [
        {
          id: 'c1',
          ownerId: 'u1',
          workflowName: 'wf-a',
          state: 'running',
          priority: 'normal',
          createdAt: '2026-01-01T00:00:00.000Z',
          updatedAt: '2026-01-01T00:00:00.000Z',
          runName: 'run #1',
          retryCount: 0,
          parentInstanceId: 'p1',
          childCount: 2,
        },
      ],
    } as Response)
    const out = await api.instances.list({ parent: 'p1' })
    expect(out[0].parentInstanceId).toBe('p1')
    expect(out[0].childCount).toBe(2)
  })
})

describe('api.schedules.list', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ schedules: [{ id: 's1', workflowName: 'wf-a' }] }),
    } as Response)
  })
  afterEach(() => vi.restoreAllMocks())

  it('GETs /schedules?workflowName= and unwraps the schedules array', async () => {
    const out = await api.schedules.list('wf-a')
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toBe('/api/v1/schedules?workflowName=wf-a')
    expect(out).toEqual([{ id: 's1', workflowName: 'wf-a' }])
  })

  it('omits the query when no workflow name is given', async () => {
    await api.schedules.list()
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toBe('/api/v1/schedules')
  })
})

describe('api layout endpoints', () => {
  afterEach(() => vi.unstubAllGlobals())

  function respond(url: string, body: unknown, expectedInit?: RequestInit) {
    const fetcher = vi.fn(async (actualURL: string, init?: RequestInit) => {
      expect(actualURL).toBe(url)
      expect(init).toEqual(expectedInit)
      return { ok: true, json: async () => body } as Response
    })
    vi.stubGlobal('fetch', fetcher)
    return fetcher
  }

  it('GETs the workflow wire LayoutView verbatim, including findings and generations', async () => {
    const wire = {
      savedRevision: 'rev-9', jobGeneration: 3, resultGeneration: 2,
      displayStatus: 'needs_attention', attemptsUsed: 5, attemptLimit: 5,
      findings: [{ criterion: 'text_clipped', severity: 'blocking', source: 'review', evidence: 'label clipped' }],
      provenance: 'arranged', unreviewed: false, retryEligible: true, available: true,
    }
    respond('/api/v1/workflows/pr-review/layout', wire)
    const view = await api.workflows.getLayout('pr-review')
    expect(view).toBe(wire)
    expect(view.savedRevision).toBe('rev-9')
    expect(view.jobGeneration).toBe(3)
    expect(view.resultGeneration).toBe(2)
    expect(view.displayStatus).toBe('needs_attention')
    expect(view.retryEligible).toBe(true)
    expect(view.findings?.[0].criterion).toBe('text_clipped')
  })

  it('POSTs the expected workflow revision and returns the acknowledgement', async () => {
    const wire = { savedRevision: 'rev-9', jobGeneration: 4, state: 'pending', displayStatus: 'pending' }
    respond('/api/v1/workflows/pr-review/layout/retry', wire, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ expectedRevision: 'rev-9' }),
    })
    expect(await api.workflows.retryLayout('pr-review', 'rev-9')).toEqual(wire)
  })

  it('GETs the instance snapshot layout', async () => {
    respond('/api/v1/instances/inst-1/layout', { savedRevision: 'rev-1', displayStatus: 'pending' })
    const view = await api.instances.getLayout('inst-1')
    expect(view.savedRevision).toBe('rev-1')
    expect(view.displayStatus).toBe('pending')
  })

  it('POSTs the snapshot revision for instance retry', async () => {
    const wire = { savedRevision: 'rev-1', jobGeneration: 2, state: 'pending', displayStatus: 'pending' }
    respond('/api/v1/instances/inst-1/layout/retry', wire, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ expectedRevision: 'rev-1' }),
    })
    expect(await api.instances.retryLayout('inst-1', 'rev-1')).toEqual(wire)
  })

  it('surfaces Instance.layout and WorkflowDefinition.presentation from raw JSON', async () => {
    respond('/api/v1/instances/inst-1', { id: 'inst-1', layout: { savedRevision: 'rev-1', displayStatus: 'complete' } })
    expect((await api.instances.get('inst-1')).layout?.savedRevision).toBe('rev-1')
    respond('/api/v1/workflows/wf', { name: 'wf', nodes: [], edges: [], presentation: { revision: 'rev-1', reason: 'update' } })
    expect((await api.workflows.get('wf')).presentation?.revision).toBe('rev-1')
  })

  it('propagates fetch failures instead of fabricating a layout', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    await expect(api.instances.getLayout('inst-1')).rejects.toThrow('offline')
  })

  it.each([
    [{ error: 'layout: expected revision is not current', code: 'CONFLICT', message: 'legacy' }, 'layout: expected revision is not current'],
    [{ message: 'legacy' }, 'legacy'],
    [{ error: '', message: 'legacy' }, 'legacy'],
    [{ error: 123, message: 'legacy' }, 'legacy'],
    [{ error: {}, message: [] }, 'Conflict'],
    [{}, 'Conflict'], [null, 'Conflict'], ['', 'Conflict'],
    [{ error: '   ', message: '' }, 'Conflict'],
  ])('prefers the server error, then legacy message, then statusText (%j)', async (body, expected) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 409, statusText: 'Conflict', json: async () => body }))
    await expect(api.workflows.retryLayout('pr-review', 'rev-old')).rejects.toThrow(`API 409: ${expected}`)
  })

  it('falls back to statusText for non-JSON error bodies', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 502, statusText: 'Bad Gateway', json: async () => { throw new SyntaxError('not JSON') } }))
    await expect(api.instances.getLayout('inst-1')).rejects.toThrow('API 502: Bad Gateway')
  })

  it('reports the sanitized status-store error on 503', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 503, statusText: 'Service Unavailable', json: async () => ({ error: 'internal server error', code: 'UNAVAILABLE' }) }))
    await expect(api.instances.getLayout('inst-1')).rejects.toThrow('API 503: internal server error')
  })
})

describe('api.workflows — manual layout writes removed', () => {
  it('no longer exposes saveLayout', () => {
    expect('saveLayout' in api.workflows).toBe(false)
  })

  it('exposes only read, trigger, stats, group-layout and arrangement-status methods for workflows', () => {
    expect(Object.keys(api.workflows).sort()).toEqual(['get', 'getLayout', 'getStats', 'groupLayout', 'list', 'retryLayout', 'trigger'])
  })
})

describe('api.instances.graphState', () => {
  afterEach(() => vi.restoreAllMocks())

  // Unknown URLs fail so a missing/wrong production endpoint cannot pass.
  function serve(byUrl: Record<string, unknown>) {
    globalThis.fetch = vi.fn(async (url: string | URL | Request) => {
      const key = String(url)
      if (!(key in byUrl)) {
        return { ok: false, status: 404, statusText: 'Not Found', json: async () => ({ message: 'no route' }) } as Response
      }
      return { ok: true, json: async () => byUrl[key] } as Response
    }) as typeof fetch
  }

  it('GETs /api/v1/instances/{id}/graph-state and returns the aggregate verbatim', async () => {
    const wire = {
      rootInstanceId: 'r1', version: 'v1', active: true,
      contexts: [
        { instanceId: 'r1', kind: 'root', state: 'waiting', blocking: true, redacted: false, nodes: [{ instanceId: 'r1', nodeId: 'g', state: 'waiting' }] },
        { instanceId: 'b1', parentInstanceId: 'r1', groupRunId: 'gr1', taskKey: 'a', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [{ instanceId: 'b1', nodeId: 'a1', state: 'running', attempt: 1 }] },
      ],
      groups: [{
        groupRunId: 'gr1', instanceId: 'r1', nodeId: 'g', form: 'branches', completionRule: 'all_succeed',
        unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results', state: 'running',
        tasks: [{ key: 'a', kind: 'branch', origin: 'a1', childInstanceId: 'b1', liveStatus: 'running', frozenStatus: '', blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false }],
        progress: { total: 1, completed: 0, failed: 0, cancelled: 0, active: 1, notStarted: 0 },
        waitingReasons: ['Waiting for task a'],
      }],
      layout: { groups: { g: { nodeId: 'g', config: { form: 'branches', tasks: [{ key: 'a', entry: 'a1' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' }, bodies: { a: ['a1'] }, nested: [], successTarget: 'done', failureTarget: 'oops' } }, owner: { a1: { groupNodeId: 'g', taskKey: 'a' } }, order: ['g'] },
      warnings: [],
    }
    serve({ '/api/v1/instances/r1/graph-state': wire })

    const out = await api.instances.graphState('r1')

    expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/instances/r1/graph-state', undefined)
    expect(out).toEqual(wire)
    expect(out.contexts[1].nodes[0]).toEqual({ instanceId: 'b1', nodeId: 'a1', state: 'running', attempt: 1 })
    expect(out.groups[0].tasks[0].childInstanceId).toBe('b1')
  })

  it('normalizes Go nulls (contexts, nodes, groups, tasks, waitingReasons, layout collections) to empties', async () => {
    serve({
      '/api/v1/instances/r2/graph-state': {
        rootInstanceId: 'r2', version: 'v0', active: false,
        contexts: [{ instanceId: 'r2', kind: 'root', state: 'completed', blocking: true, redacted: false, nodes: null }],
        groups: [{
          groupRunId: 'gr', instanceId: 'r2', nodeId: 'g', form: 'workflows', completionRule: 'first_success',
          unfinishedBehavior: 'finish_independently', gatherMode: 'results', state: 'done',
          tasks: null, progress: null, waitingReasons: null,
        }],
        layout: { groups: { g: { nodeId: 'g', config: { form: 'workflows', tasks: null, completionRule: 'first_success', unfinishedBehavior: 'finish_independently', gatherMode: 'results' }, bodies: null, nested: null, successTarget: 's', failureTarget: 'f' } }, owner: null, order: null },
        warnings: null,
      },
    })

    const out = await api.instances.graphState('r2')

    expect(out.contexts[0].nodes).toEqual([])
    expect(out.groups[0].tasks).toEqual([])
    expect(out.groups[0].waitingReasons).toEqual([])
    expect(out.groups[0].progress).toEqual({ total: 0, completed: 0, failed: 0, cancelled: 0, active: 0, notStarted: 0 })
    expect(out.layout!.groups.g.bodies).toEqual({})
    expect(out.layout!.groups.g.nested).toEqual([])
    expect(out.layout!.groups.g.config.tasks).toEqual([])
    expect(out.layout!.owner).toEqual({})
    expect(out.layout!.order).toEqual([])
    expect(out.warnings).toEqual([])
  })

  it('normalizes a descendant workflow context layout without dropping its graph or session', async () => {
    const session = { groupRunId: 'gr-child', instanceId: 'child', nodeId: 'inner', mergeIndex: 1, attempt: 2 }
    const context = {
      instanceId: 'child', kind: 'workflow', state: 'waiting', blocking: true, redacted: false,
      nodes: [{ instanceId: 'child', nodeId: 'inner', state: 'waiting' }],
      graph: { name: 'child-wf', nodes: [{ id: 'inner', type: 'parallel_group' }] },
      layout: { groups: null, owner: null, order: null },
    }
    serve({ '/api/v1/instances/r1/graph-state': {
      rootInstanceId: 'r1', version: 'v-child', active: true, contexts: [context],
      groups: [{
        groupRunId: 'gr-child', instanceId: 'child', nodeId: 'inner', form: 'branches',
        completionRule: 'all_succeed', unfinishedBehavior: 'wait_for_remaining',
        gatherMode: 'results_and_files', state: 'resolving_conflicts', tasks: null,
        progress: { total: 0, completed: 0, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
        waitingReasons: ['Resolve conflict'], activeSession: session,
      }],
    } })
    const out = await api.instances.graphState('r1')
    expect(out.contexts[0].layout).toEqual({ groups: {}, owner: {}, order: [] })
    expect(out.contexts[0].graph).toEqual(context.graph)
    expect(out.contexts[0].nodes).toEqual(context.nodes)
    expect(out.groups[0].activeSession).toEqual(session)
    expect(out.groups[0].waitingReasons).toEqual(['Resolve conflict'])
  })

  it('a null contexts/groups envelope and an absent layout normalize to empties and undefined', async () => {
    serve({ '/api/v1/instances/r3/graph-state': { rootInstanceId: 'r3', version: 'v', active: false, contexts: null, groups: null } })
    const out = await api.instances.graphState('r3')
    expect(out.contexts).toEqual([])
    expect(out.groups).toEqual([])
    expect(out.layout).toBeUndefined()
  })

  it('rejects with the server status and message on a non-2xx response', async () => {
    serve({})
    await expect(api.instances.graphState('hidden')).rejects.toThrow('API 404: no route')
  })
})

describe('api.instances.list — active-descendant ancestor discovery (P4-T10)', () => {
  afterEach(() => vi.restoreAllMocks())

  it('sends activeDescendantAncestors=true when requested', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ activeDescendantAncestors: true, includeDismissed: true })
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances?includeDismissed=true&activeDescendantAncestors=true',
      undefined,
    )
  })

  it('composes with the roots scope', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ parent: 'roots', activeDescendantAncestors: true })
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/instances?parent=roots&activeDescendantAncestors=true', undefined)
  })

  it('omits the parameter when false or absent', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ state: 'running', activeDescendantAncestors: false })
    await api.instances.list({ state: 'running' })
    const calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls
    expect(calls[0][0]).toBe('/api/v1/instances?state=running')
    expect(calls[1][0]).toBe('/api/v1/instances?state=running')
  })
})

describe('api.workflows.groupLayout', () => {
  afterEach(() => vi.restoreAllMocks())

  it('GETs /api/v1/workflows/{name}/group-layout and returns the GroupGraph', async () => {
    const graph = {
      groups: {
        g: { nodeId: 'g', config: { form: 'workflows', tasks: [{ key: 'x', workflow: 'child-a' }], completionRule: 'all_succeed', unfinishedBehavior: 'wait_for_remaining', gatherMode: 'results' }, bodies: {}, nested: [], successTarget: 's', failureTarget: 'f' },
      },
      owner: {}, order: ['g'],
    }
    globalThis.fetch = vi.fn(async (url: string | URL | Request) =>
      String(url) === '/api/v1/workflows/wf-a/group-layout'
        ? ({ ok: true, json: async () => graph } as Response)
        : ({ ok: false, status: 404, statusText: 'Not Found', json: async () => ({}) } as Response),
    ) as typeof fetch

    const out = await api.workflows.groupLayout('wf-a')

    expect(out).toEqual(graph)
    expect(out.groups.g.config.tasks[0].workflow).toBe('child-a')
  })

  it('normalizes an all-null GroupGraph (workflow without groups) to empties', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: true, json: async () => ({ groups: null, owner: null, order: null }) }) as Response) as typeof fetch
    const out = await api.workflows.groupLayout('plain')
    expect(out).toEqual({ groups: {}, owner: {}, order: [] })
  })

  it('rejects on a non-2xx response', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: false, status: 404, statusText: 'Not Found', json: async () => ({ message: 'workflow not found' }) }) as Response) as typeof fetch
    await expect(api.workflows.groupLayout('nope')).rejects.toThrow('API 404: workflow not found')
  })
})
