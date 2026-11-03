import { describe, expect, it } from 'vitest'

import { formatQualitySchedule, parseRandomSchedule } from '../qualitySchedule'

describe('parseRandomSchedule', () => {
  it('parses valid random intervals', () => {
    expect(parseRandomSchedule('@random 10-15')).toEqual({ min: 10, max: 15 })
    expect(parseRandomSchedule('@random 10 - 15')).toEqual({ min: 10, max: 15 })
    expect(parseRandomSchedule('  @random 5-5  ')).toEqual({ min: 5, max: 5 })
    expect(parseRandomSchedule('@random 1-1440')).toEqual({ min: 1, max: 1440 })
  })

  it('rejects invalid random intervals and other expressions', () => {
    for (const expr of [
      '@random 15-10',
      '@random 0-5',
      '@random 10-2000',
      '@random',
      '@random abc',
      '@random 10',
      '@random 10-15,20-25',
      '*/30 * * * *',
    ]) {
      expect(parseRandomSchedule(expr)).toBeNull()
    }
  })
})

describe('formatQualitySchedule', () => {
  const t = (key: string, params?: Record<string, unknown>) => `${key}:${JSON.stringify(params ?? {})}`

  it('formats random and fixed intervals', () => {
    expect(formatQualitySchedule('@random 10-15', t)).toBe('qualityOps.randomSchedule:{"min":10,"max":15}')
    expect(formatQualitySchedule('@random 5-5', t)).toBe('qualityOps.fixedIntervalSchedule:{"minutes":5}')
  })

  it('returns other expressions unchanged', () => {
    expect(formatQualitySchedule('*/30 * * * *', t)).toBe('*/30 * * * *')
    expect(formatQualitySchedule(' @random 15-10 ', t)).toBe(' @random 15-10 ')
  })

  it('shows field watch rules as judged per request instead of their stored cron', () => {
    expect(formatQualitySchedule('*/30 * * * *', t, 'field_watch')).toBe('qualityOps.fieldWatchTag:{}')
    expect(formatQualitySchedule('@random 10-15', t, 'field_watch')).toBe('qualityOps.fieldWatchTag:{}')
    expect(formatQualitySchedule('*/30 * * * *', t, 'candy')).toBe('*/30 * * * *')
  })
})
