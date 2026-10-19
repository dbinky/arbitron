import type { GroupProgress } from '../api/types'

const RULE_TEXT: Record<string, string> = {
  all_succeed: 'All succeed',
  first_success: 'First success',
}
const UNFINISHED_TEXT: Record<string, string> = {
  wait_for_remaining: 'wait for remaining',
  cancel_unfinished: 'cancel unfinished',
  finish_independently: 'finish independently',
}
const GATHER_TEXT: Record<string, string> = {
  results: 'results',
  results_and_files: 'results + files',
}

/** Human text for a completion rule ("All succeed" / "First success"). */
export function ruleText(rule: string): string {
  return RULE_TEXT[rule] ?? rule
}

/** Human text for an unfinished-task behaviour. */
export function unfinishedText(behavior: string): string {
  return UNFINISHED_TEXT[behavior] ?? behavior
}

/** Human text for a gather mode. */
export function gatherText(mode: string): string {
  return GATHER_TEXT[mode] ?? mode
}

/** "2/3 done · 1 active · 1 failed" from a group progress record. */
export function progressText(p: GroupProgress): string {
  const finished = p.completed + p.failed + p.cancelled
  const parts = [`${finished}/${p.total} done`]
  if (p.active > 0) parts.push(`${p.active} active`)
  if (p.notStarted > 0) parts.push(`${p.notStarted} not started`)
  if (p.failed > 0) parts.push(`${p.failed} failed`)
  return parts.join(' · ')
}

/** "resolving_conflicts" -> "resolving conflicts". */
export function stateText(state: string): string {
  return state.replace(/_/g, ' ')
}

const WAITING_TEXT: Record<string, string> = {
  launching: 'Launching tasks',
  preparing_files: 'Preparing files',
  gathering: 'Gathering files',
  resolving_conflicts: 'Resolving conflicts',
  rollback_failed: 'Rollback failed',
  continuation_pending: 'Waiting for parent continuation',
}

/** Decode the server's waiting-reason vocabulary, preserving task order. */
export function waitingReasonText(reason: string): string {
  for (const [prefix, label] of [
    ['waiting_for_tasks:', 'Waiting for tasks'],
    ['cancelling:', 'Cancelling tasks'],
  ]) {
    if (reason.startsWith(prefix)) {
      const keys = reason.slice(prefix.length).split(',').filter(Boolean)
      return keys.length > 0 ? `${label}: ${keys.join(', ')}` : label
    }
  }
  return WAITING_TEXT[reason] ?? reason
}
