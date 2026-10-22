import { describe, expect, it } from 'vitest'
import contract from './layoutView.contract.json'
import type { DisplayStatus, LayoutView, Provenance, RetryAck } from './types'

// domain/layout/view_contract_test.go checks the Go View/RetryAck JSON tags and
// status constants against the same file. These records are exhaustive at
// compile time, so tsc fails if types.ts drifts and this test fails if the
// server's wire contract does.
type RequiredKeys<T> = { [K in keyof T]-?: object extends Pick<T, K> ? never : K }[keyof T]
type OptionalKeys<T> = Exclude<keyof T, RequiredKeys<T>>

const viewRequired: Record<RequiredKeys<LayoutView>, true> = {
  savedRevision: true, jobGeneration: true, resultGeneration: true, displayStatus: true,
  attemptsUsed: true, attemptLimit: true, findings: true, provenance: true,
  unreviewed: true, retryEligible: true, available: true,
}
const viewOptional: Record<OptionalKeys<LayoutView>, true> = {
  state: true, stage: true, failureStage: true, failureReason: true, evidence: true,
  contract: true, geometry: true, groups: true, retained: true, availabilityError: true,
}
const ackRequired: Record<RequiredKeys<RetryAck>, true> = {
  savedRevision: true, jobGeneration: true, state: true, displayStatus: true,
}
const ackOptional: Record<OptionalKeys<RetryAck>, true> = {}
const displayStatuses: Record<DisplayStatus, true> = {
  pending: true, arranging: true, rendering: true, reviewing: true, complete: true,
  needs_attention: true, failed: true, legacy: true, unavailable: true,
}
const provenances: Record<Provenance, true> = {
  arranged: true, retained_geometry: true, retained_positions: true, legacy: true,
}

const keys = (o: object) => Object.keys(o).sort()

describe('LayoutView wire contract shared with domain/layout.View', () => {
  it('has the server’s required and omitempty view fields', () => {
    expect(keys(viewRequired)).toEqual(contract.view.required)
    expect(keys(viewOptional)).toEqual(contract.view.optional)
  })

  it('has the server’s retry acknowledgement fields', () => {
    expect(keys(ackRequired)).toEqual(contract.retryAck.required)
    expect(keys(ackOptional)).toEqual(contract.retryAck.optional)
  })

  it('knows every display status and provenance the server can send', () => {
    expect(keys(displayStatuses)).toEqual(contract.displayStatus)
    expect(keys(provenances)).toEqual(contract.provenance)
  })
})
