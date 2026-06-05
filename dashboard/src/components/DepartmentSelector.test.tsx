import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import DepartmentSelector from './DepartmentSelector'

describe('DepartmentSelector', () => {
  it('renders nothing for a single-department user', () => {
    const { container } = render(
      <DepartmentSelector departments={['engineering']} selection={null} onChange={vi.fn()} />,
    )
    expect(container).toBeEmptyDOMElement()
    expect(screen.queryByRole('combobox')).toBeNull()
  })

  it('renders nothing for a user in only the default group', () => {
    const { container } = render(
      <DepartmentSelector departments={['default']} selection={null} onChange={vi.fn()} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('renders a dropdown with an "All my departments" option plus each department for multi-dept users', () => {
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={null} onChange={vi.fn()} />,
    )
    const select = screen.getByRole('combobox')
    const options = Array.from(select.querySelectorAll('option')).map((o) => o.textContent)
    expect(options).toEqual(['All my departments', 'engineering', 'sales'])
  })

  it('defaults the dropdown to the union ("All my departments") when selection is null', () => {
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={null} onChange={vi.fn()} />,
    )
    expect(screen.getByRole('combobox')).toHaveValue('__all__')
  })

  it('reflects a focused single-department selection', () => {
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={['sales']} onChange={vi.fn()} />,
    )
    expect(screen.getByRole('combobox')).toHaveValue('sales')
  })

  it('emits null when the union option is chosen', () => {
    const onChange = vi.fn()
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={['engineering']} onChange={onChange} />,
    )
    fireEvent.change(screen.getByRole('combobox'), { target: { value: '__all__' } })
    expect(onChange).toHaveBeenCalledWith(null)
  })

  it('emits a single-element array when a department is chosen', () => {
    const onChange = vi.fn()
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={null} onChange={onChange} />,
    )
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'engineering' } })
    expect(onChange).toHaveBeenCalledWith(['engineering'])
  })

  // FA #25 Pass 1: edge case — empty departments array
  it('renders nothing when departments is an empty array', () => {
    const { container } = render(
      <DepartmentSelector departments={[]} selection={null} onChange={vi.fn()} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  // FA #25 Pass 2: robustness — multi-element selection falls back to union
  it('shows union default ("All my departments") when selection has more than one element', () => {
    render(
      <DepartmentSelector
        departments={['engineering', 'sales', 'platform']}
        selection={['engineering', 'sales']}
        onChange={vi.fn()}
      />,
    )
    // A 2-element selection is not a valid focused state; the component
    // falls back to __all__ (the union sentinel) since selection.length !== 1.
    expect(screen.getByRole('combobox')).toHaveValue('__all__')
  })
})
