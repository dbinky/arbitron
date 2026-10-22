import { describe, expect, it, vi } from 'vitest'
import contract from '../graph/arranged/contract.json'
import { fixtureGeometry } from '../graph/arranged/testFixtures'
import { fetchJob, jobTokenFrom, jobUrl, parseJob, RUNTIME_STATES } from './job'

const TOKEN = '0123456789abcdef0123456789abcdef'

const geometry = {
  format: 'arbitron.layout/v2',
  contract,
  options: fixtureGeometry().options,
  nodes: [{ id: 'a', box: { x: 40, y: 40, w: 200, h: 60 }, text: 'Start', typeCaption: 'agent call', textLines: 1, rank: 0 }],
  edges: null,
  crossings: null,
  canvas: { x: 0, y: 0, w: 280, h: 140 },
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('jobTokenFrom', () => {
  it('returns a well-formed token', () => {
    expect(jobTokenFrom(`?job=${TOKEN}`)).toBe(TOKEN)
  })
  it('rejects a missing or malformed token', () => {
    expect(() => jobTokenFrom('')).toThrow(/missing/)
    expect(() => jobTokenFrom('?job=../../etc/passwd')).toThrow(/malformed/)
    expect(() => jobTokenFrom('?job=ABCDEF')).toThrow(/malformed/)
  })
})

describe('parseJob', () => {
  it('accepts a render job and normalises Go null slices', () => {
    const job = parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, geometry, workflowNodeTypes: { a: 'agent_call' }, states: [...RUNTIME_STATES] })
    expect(job.mode).toBe('render')
    if (job.mode !== 'render') return
    expect(job.geometry.edges).toEqual([])
    expect(job.geometry.crossings).toEqual([])
    expect(job.states).toEqual([...RUNTIME_STATES])
    expect(job.workflowNodeTypes).toEqual({ a: 'agent_call' })
  })
  it('rejects absent node types and absent runtime-state coverage', () => {
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, geometry, workflowNodeTypes: null, states: null })).toThrow()
  })
  it('accepts a measure job', () => {
    const job = parseJob({ mode: 'measure', request: { contract, items: [{ key: 'node:a:label', text: 'Start', style: 'node_label', maxWidth: 240 }] } })
    expect(job).toMatchObject({ mode: 'measure', request: { items: [{ key: 'node:a:label' }] } })
  })
  it('rejects an unknown mode', () => {
    expect(() => parseJob({ mode: 'paint' })).toThrow(/unknown mode/)
    expect(() => parseJob(null)).toThrow(/object/)
  })
  it('rejects a render job without geometry or with an empty canvas', () => {
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: {}, states: [...RUNTIME_STATES] })).toThrow(/geometry is required/)
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: { a: 'agent_call' }, geometry: { ...geometry, canvas: { x: 0, y: 0, w: 0, h: 10 } }, states: [] })).toThrow(/canvas/)
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: { a: 'agent_call' }, geometry: { ...geometry, nodes: [] }, states: [] })).toThrow(/no nodes/)
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: { a: 'agent_call' }, geometry: { ...geometry, contract: { ...contract, practicalZoom: 0 } }, states: [] })).toThrow(/practicalZoom/)
  })
  it('rejects unknown runtime states', () => {
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: { a: 'agent_call' }, geometry, states: ['exploding'] })).toThrow(/unknown runtime state/)
  })
  it('rejects malformed measure items', () => {
    expect(() => parseJob({ mode: 'measure', request: { items: [] } })).toThrow(/contract/)
    expect(() => parseJob({ mode: 'measure', request: { contract, items: [{ key: 'k', text: 'x', style: 'huge', maxWidth: 10 }] } })).toThrow(/invalid text item/)
    expect(() => parseJob({ mode: 'measure', request: { contract, items: [{ key: 'k', text: 'x', style: 'edge_label', maxWidth: 0 }] } })).toThrow(/invalid text item/)
  })
})

describe('strict job boundary', () => {
  const valid = () => ({ mode: 'render', revision: 'rev-1', attempt: 1, geometry: structuredClone(geometry), workflowNodeTypes: { a: 'agent_call' }, states: [...RUNTIME_STATES] })
  it.each([undefined, null, '', '../rev', 'rev/one', 'rev\n1'])('rejects revision %s', revision => {
    expect(() => parseJob({ ...valid(), revision })).toThrow(/revision/)
  })
  it.each([undefined, null, 0, -1, 1.2, Infinity])('rejects attempt %s', attempt => {
    expect(() => parseJob({ ...valid(), attempt })).toThrow(/attempt/)
  })
  it.each([null, {}, { a: '' }, { a: 'transform' }, { a: 'agent_call', extra: 'script' }])('rejects type map %j', workflowNodeTypes => {
    expect(() => parseJob({ ...valid(), workflowNodeTypes })).toThrow(/type|caption/)
  })
  it.each([null, [], ['pending'], [...RUNTIME_STATES, 'pending']])('requires complete unique states %j', states => {
    expect(() => parseJob({ ...valid(), states })).toThrow(/state/)
  })
  it('uses the shared geometry validator, not only the version', () => {
    for (const geometry of [null, {}, { ...valid().geometry,format:'arbitron.layout/v1' }, { ...valid().geometry, nodes: [null] }, { ...valid().geometry, options: {} }, { ...valid().geometry, contract: { ...contract, nodeLabelWeight: 400 } }, { ...valid().geometry, canvas: { x: 1, y: 0, w: 280, h: 140 } }]) {
      expect(() => parseJob({ ...valid(), geometry })).toThrow()
    }
  })
  it('requires explicit items and exact contract; rejects duplicate/empty keys', () => {
    for (const request of [{ contract }, { contract, items: null }, { contract: { ...contract, lineHeight: 2 }, items: [] }, { contract, items: [{ key: '', text: 'x', style: 'node_label', maxWidth: 10 }] }, { contract, items: Array(2).fill({ key: 'same', text: 'x', style: 'node_label', maxWidth: 10 }) }]) {
      expect(() => parseJob({ mode: 'measure', request })).toThrow()
    }
  })
})

describe('fetchJob', () => {
  it('fetches the document named by the URL token', async () => {
    const other = 'ffffffffffffffffffffffffffffffff'
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      // Honour the token: only this page's job exists.
      if (String(input) === jobUrl(TOKEN)) return jsonResponse({ mode: 'measure', request: { contract, items: [] } })
      return jsonResponse({ error: 'not found' }, 404)
    }) as unknown as typeof fetch
    const job = await fetchJob(`?job=${TOKEN}`, fetchImpl)
    expect(job.mode).toBe('measure')
    expect(fetchImpl).toHaveBeenCalledWith('/__render/jobs/' + TOKEN + '.json', { cache: 'no-store' })
    await expect(fetchJob(`?job=${other}`, fetchImpl)).rejects.toThrow(/HTTP 404/)
  })
  it('fails without calling fetch when the token is absent', async () => {
    const fetchImpl = vi.fn() as unknown as typeof fetch
    await expect(fetchJob('', fetchImpl)).rejects.toThrow(/missing/)
    expect(fetchImpl).not.toHaveBeenCalled()
  })
  it('surfaces validation errors from the fetched document', async () => {
    const fetchImpl = vi.fn(async () => jsonResponse({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: {}, states: [...RUNTIME_STATES] })) as unknown as typeof fetch
    await expect(fetchJob(`?job=${TOKEN}`, fetchImpl)).rejects.toThrow(/geometry is required/)
  })
})
