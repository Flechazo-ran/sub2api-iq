import { describe, expect, it } from 'vitest'

import { tickEveryMinutes } from './axis'

describe('tickEveryMinutes', () => {
  it('keeps the desktop rules when the chart width is unknown', () => {
    expect(tickEveryMinutes(720)).toBe(60)
    expect(tickEveryMinutes(360)).toBe(30)
    expect(tickEveryMinutes(120)).toBe(10)
    expect(tickEveryMinutes(60)).toBe(5)
    expect(tickEveryMinutes(30)).toBe(5)
  })

  it('does not thin out labels on a wide chart', () => {
    expect(tickEveryMinutes(720, 1200)).toBe(60)
    expect(tickEveryMinutes(360, 1200)).toBe(30)
  })

  it('widens the interval on a narrow chart until every label has 56px', () => {
    expect(tickEveryMinutes(720, 356)).toBe(120)
    expect(tickEveryMinutes(360, 356)).toBe(60)
    expect(tickEveryMinutes(120, 356)).toBe(20)
  })
})
