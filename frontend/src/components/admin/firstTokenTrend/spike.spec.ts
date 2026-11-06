import { describe, expect, it } from 'vitest'

import { computeAxisCap, pointsAbove } from './spike'

describe('computeAxisCap', () => {
  it('caps one 96 s spike by the 95th percentile, rounded up to 10 s with a 20 s floor', () => {
    expect(computeAxisCap([5, 6, 7, 8, 96], 'seconds', true)).toEqual({ max: 20, capped: true })
  })

  it('keeps the real height when every point fits under the cap', () => {
    expect(computeAxisCap([5, 6, 7, 8], 'seconds', true)).toEqual({ max: 20, capped: false })
  })

  it('shows the full range when capping is switched off', () => {
    expect(computeAxisCap([5, 6, 7, 8, 96], 'seconds', false)).toEqual({ max: 100, capped: false })
  })

  it('uses a 10 floor and no cap for small in-flight counts', () => {
    expect(computeAxisCap([1, 2, 3], 'count', true)).toEqual({ max: 10, capped: false })
  })

  it('caps an in-flight spike at 10', () => {
    expect(computeAxisCap([1, 2, 3, 40], 'count', true)).toEqual({ max: 10, capped: true })
  })

  it('does not cap when 1.3 × p95 already covers the maximum', () => {
    expect(computeAxisCap([10, 11, 12, 13], 'seconds', true)).toEqual({ max: 20, capped: false })
  })

  it('handles empty and null-only data', () => {
    expect(computeAxisCap([], 'seconds', true)).toEqual({ max: 20, capped: false })
    expect(computeAxisCap([null, undefined], 'count', true)).toEqual({ max: 10, capped: false })
  })
})

describe('pointsAbove', () => {
  it('returns the indices and actual values above the axis max', () => {
    expect(pointsAbove([5, 96, null, 21, 20], 20)).toEqual([
      { index: 1, value: 96 },
      { index: 3, value: 21 },
    ])
  })
})
