/**
 * Bounded periodic queue refetch while any run is active or pending. Events
 * are not enough on their own: the bus drops when buffers fill, several
 * transitions have no emitter, and a dropped final event would otherwise leave
 * an expanded hierarchy level stale forever. Each refetch also refreshes every
 * open hierarchy level (QueueSection.refreshSignal -> useRunHierarchy.invalidate).
 */
export const QUEUE_POLL_MS = 10_000

/** Instance lifecycle events (existing emitters) that change which rows exist or their state. */
const LIFECYCLE_EVENTS = new Set([
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
])

const GROUP_PREFIX = 'group.'

/**
 * Whether a system-stream event should refetch the queue (and so refresh the
 * open hierarchy levels). Group lifecycle events (`group.launched`,
 * `group.task_updated`, `group.decided`, `group.gathering`, `group.conflict`,
 * `group.resolved`, `group.completed`) change task rows, markers and
 * independent work with no instance event on the expanded parent. node.started
 * / node.failed are NOT refetches — QueueView patches those rows in place.
 */
export function isQueueRefreshEvent(type: string): boolean {
  if (LIFECYCLE_EVENTS.has(type)) return true
  return type.startsWith(GROUP_PREFIX) && type.length > GROUP_PREFIX.length
}
