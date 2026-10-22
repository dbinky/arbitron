import { describe, expect, it, vi } from 'vitest'
import * as report from './report'
import type { Geometry } from '../graph/arranged/types'

// These are DOM-shaped collector tests, not browser capture acceptance.
describe('actual group report collectors', () => {
  it('observes text and paint, and rejects missing/duplicate logical identities', () => {
    const id = JSON.stringify(['group', '中', 'task', 'a'])
    const key = JSON.stringify(['text', id, 'title'])
    const geo = { groupElements: [{ id, texts: [{ key, text: 'Expected' }] }] } as unknown as Geometry
    document.body.innerHTML = '<div><span>Actual 中</span></div>'
    const el = document.body.firstElementChild!, text = el.firstElementChild!
    el.setAttribute('data-arranged-group-element', id)
    text.setAttribute('data-group-text', key)
    el.setAttribute('style', 'border:2px solid rgb(1,2,3);background:rgb(4,5,6)')
    const ctx = { doc: document, win: window, root: document.body, zoom: 1, origin: { x: 0, y: 0 } }
    const collect = report.collectGroupElements
    expect(collect(geo, ctx)).toMatchObject([{ id, borderPx: 2, borderColor: 'rgb(1, 2, 3)', backgroundColor: 'rgb(4, 5, 6)', texts: [{ key, text: { content: 'Actual 中' } }] }])
    text.remove()
    expect(() => collect(geo, ctx)).toThrow(/text.*missing or duplicate/)
    el.append(text, text.cloneNode(true))
    expect(() => collect(geo, ctx)).toThrow(/text.*missing or duplicate/)
    el.after(el.cloneNode(true))
    expect(() => collect(geo, ctx)).toThrow(/element.*missing or duplicate/)
    vi.restoreAllMocks()
  })
})
