import { render, screen, within, cleanup } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { SpawnDetails, mappingKind } from './SpawnDetails'

afterEach(cleanup)

const show = (config: Record<string, unknown>) =>
  render(
    <MemoryRouter>
      <SpawnDetails config={config} />
    </MemoryRouter>,
  )

const EMPTY =
  'No inputs mapped — every required input without a default will be prepared by the LLM at run time.'

describe('SpawnDetails', () => {
  it('links the child workflow and states the mode in words', () => {
    show({ childWorkflow: 'stage', spawnMode: 'wait_for_child' })
    const link = screen.getByRole('link', { name: 'stage' })
    expect(link.getAttribute('href')).toBe('/workflows/stage')
    expect(screen.getByText('waits for child')).toBeInTheDocument()
  })

  it('says fire and forget for the fire_and_forget mode', () => {
    show({ childWorkflow: 'stage', spawnMode: 'fire_and_forget' })
    expect(screen.getByText('fire and forget')).toBeInTheDocument()
  })

  it('lists input rows sorted by key with the right badge each', () => {
    show({
      childWorkflow: 'stage',
      inputs: { zed: true, alpha: '{{a.b}}', mid: 'x-{{a}}' },
    })
    const rows = screen.getAllByTestId('spawn-input-row')
    expect(rows.map((r) => within(r).getByTestId('spawn-input-key').textContent)).toEqual([
      'alpha',
      'mid',
      'zed',
    ])
    expect(within(rows[0]).getByText('reference')).toBeInTheDocument()
    expect(within(rows[1]).getByText('template')).toBeInTheDocument()
    expect(within(rows[2]).getByText('literal')).toBeInTheDocument()
  })

  it('shows the empty state when there are no inputs', () => {
    show({ childWorkflow: 'stage' })
    expect(screen.getByText(EMPTY)).toBeInTheDocument()
    cleanup()
    show({ childWorkflow: 'stage', inputs: {} })
    expect(screen.getByText(EMPTY)).toBeInTheDocument()
  })

  it('renders a nested object literal as compact JSON', () => {
    show({ childWorkflow: 'stage', inputs: { cfg: { x: 1 } } })
    expect(screen.getByText('{"x":1}')).toBeInTheDocument()
  })

  it('does not throw when inputs is a string or childWorkflow is missing', () => {
    show({ inputs: 'oops' })
    expect(screen.getByTestId('spawn-child').textContent).toBe('—')
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.getByText(EMPTY)).toBeInTheDocument()
  })

  it('truncates a literal past 120 characters and keeps the full value in title', () => {
    const long = 'a'.repeat(121)
    show({ childWorkflow: 'stage', inputs: { k: long } })
    const cell = screen.getByTestId('spawn-input-value')
    expect(cell.textContent).toBe(`"${'a'.repeat(120)}…`)
    expect(cell.getAttribute('title')).toBe(JSON.stringify(long))
  })

  it('leaves a literal of exactly 120 characters whole', () => {
    const exact = 'a'.repeat(120)
    show({ childWorkflow: 'stage', inputs: { k: exact } })
    const cell = screen.getByTestId('spawn-input-value')
    expect(cell.textContent).toBe(JSON.stringify(exact))
    expect(cell.textContent).not.toContain('…')
  })

  it('renders keys with dots and dashes verbatim', () => {
    show({ childWorkflow: 'stage', inputs: { 'a.b-c': 1 } })
    expect(screen.getByTestId('spawn-input-key').textContent).toBe('a.b-c')
  })
})

describe('mappingKind', () => {
  it('treats a leading-space string as a template, not a reference', () => {
    expect(mappingKind(' {{a}}')).toBe('template')
  })
  it('treats a padded whole reference as a reference', () => {
    expect(mappingKind('{{ a }}')).toBe('reference')
  })
  it('classifies the degenerate and non-string cases', () => {
    expect(mappingKind('{{}}')).toBe('template')
    expect(mappingKind('{{ }}')).toBe('template')
    expect(mappingKind('{{a}}{{b}}')).toBe('template')
    expect(mappingKind('plain')).toBe('literal')
    expect(mappingKind(3)).toBe('literal')
    expect(mappingKind(null)).toBe('literal')
    expect(mappingKind({ a: '{{x}}' })).toBe('literal')
  })
})
