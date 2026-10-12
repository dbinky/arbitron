import { describe, expect, it } from 'vitest'
import { formatBinding } from './formatBinding'

describe('formatBinding', () => {
  it('agent node: model (effort) · harness · provider', () => {
    expect(
      formatBinding({ state: 'running', model: 'gpt-6.1-sol', effort: 'high', backend: 'codex', provider: 'litellm', nodeType: 'agent_call' }),
    ).toBe('gpt-6.1-sol (high) · codex · litellm')
  })
  it('chat node has no harness', () => {
    expect(
      formatBinding({ state: 'running', model: 'claude-sonnet-5', effort: 'medium', provider: 'claude-cli', nodeType: 'llm_call' }),
    ).toBe('claude-sonnet-5 (medium) · claude-cli')
  })
  it('default effort omits the parentheses', () => {
    expect(
      formatBinding({ state: 'running', model: 'opus-5.5', backend: 'claude-code', provider: 'claude-cli', nodeType: 'agent_call' }),
    ).toBe('opus-5.5 · claude-code · claude-cli')
  })
  it('non-model node shows its type', () => {
    expect(formatBinding({ state: 'running', nodeType: 'script' })).toBe('script')
  })
  it.each(['completed', 'cancelled'])('%s row shows a dash', (state) => {
    expect(formatBinding({ state, model: 'x', nodeType: 'agent_call' })).toBe('—')
  })
  it('nothing known shows a dash', () => {
    expect(formatBinding({ state: 'queued' })).toBe('—')
  })
  it('effort without a model is not shown alone', () => {
    expect(formatBinding({ state: 'running', effort: 'high', backend: 'codex', nodeType: 'agent_call' })).toBe('codex')
  })
})
