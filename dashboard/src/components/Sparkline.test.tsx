import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import Sparkline, { sparklineSegments } from './Sparkline'

describe('sparklineSegments', () => {
  it('treats non-finite measurements as gaps', () => {
    expect(sparklineSegments([10, 20, NaN, 30, 40, Infinity, 50, 60], 100, 140, 32)).toEqual([
      '0,28.8 20,25.6', '60,22.4 80,19.2', '120,16 140,12.8',
    ])
  })
  it('needs at least two samples', () => {
    expect(sparklineSegments([], 100, 120, 32)).toEqual([])
    expect(sparklineSegments([42], 100, 120, 32)).toEqual([])
  })
  it('clamps out-of-range values and tolerates a zero max', () => {
    expect(sparklineSegments([150, -5], 100, 120, 32)).toEqual(['0,0 120,32'])
    expect(sparklineSegments([0, 0], 0, 120, 32)).toEqual(['0,32 120,32'])
  })
  it('scales values into the box, top is max', () => {
    expect(sparklineSegments([0, 50, 100], 100, 120, 32)).toEqual(['0,32 60,16 120,0'])
  })

  it('splits at unknown samples instead of interpolating, dropping single points', () => {
    expect(sparklineSegments([10, null, 30, 40], 100, 120, 32)).toEqual(['80,22.4 120,19.2'])
    expect(sparklineSegments([10, 20, null, 30, 40], 100, 120, 32)).toEqual(['0,28.8 30,25.6', '90,22.4 120,19.2'])
  })
})

describe('Sparkline', () => {
  it('says so when there are not enough samples', () => {
    const { container } = render(<Sparkline label="Load" values={[null, 5]} max={100} latest="—" />)
    expect(container.querySelector('svg')).toBeNull()
    expect(screen.getByText('Not enough samples')).toBeInTheDocument()
  })
  it('renders one polyline per segment with an accessible label and the latest value', () => {
    const { container } = render(<Sparkline label="CPU" values={[10, 20, null, 30, 40]} max={100} latest="40%" />)
    expect(screen.getByRole('img', { name: 'CPU trend' })).toBeInTheDocument()
    expect(Array.from(container.querySelectorAll('polyline'), (line) => line.getAttribute('points'))).toEqual([
      '0,28.8 30,25.6', '90,22.4 120,19.2',
    ])
    expect(container.querySelector('[data-sparkline="CPU"]')).toHaveTextContent('CPU 40%')
  })
})
