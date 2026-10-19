import { describe, it, expect } from 'vitest'
import { QUEUE_POLL_MS, isQueueRefreshEvent } from './queueRefresh'

describe('isQueueRefreshEvent', () => {
  it.each([
    'instance.created',
    'instance.started',
    'instance.completed',
    'instance.failed',
    'instance.cancelled',
    'instance.paused',
    'instance.resumed',
    'instance.retrying',
    'instance.recovered',
    'instance.intervened',
  ])('refetches on lifecycle event %s', (type) => {
    expect(isQueueRefreshEvent(type)).toBe(true)
  })

  // Group lifecycle changes task rows, decisions and independent work without
  // any instance event on the expanded parent.
  it.each([
    'group.launched',
    'group.task_updated',
    'group.decided',
    'group.gathering',
    'group.conflict',
    'group.resolved',
    'group.completed',
  ])('refetches on group event %s', (type) => {
    expect(isQueueRefreshEvent(type)).toBe(true)
  })

  // node.started/failed PATCH rows in place; chat traffic never changes rows;
  // never-emitted event names are not listened for.
  it.each(['node.started', 'node.failed', 'node.completed', 'interactive.token', 'interactive.ended', 'group.', 'instance.state_changed', 'variables.updated', ''])(
    'does not refetch on %s',
    (type) => {
      expect(isQueueRefreshEvent(type)).toBe(false)
    },
  )
})

describe('QUEUE_POLL_MS', () => {
  it('is a bounded 10 s interval', () => {
    expect(QUEUE_POLL_MS).toBe(10_000)
  })
})
