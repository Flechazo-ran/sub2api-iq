import { FIELD_WATCH_QUESTION } from './intelligenceTest'

const RANDOM_SCHEDULE_PATTERN = /^@random\s+(\d+)\s*-\s*(\d+)$/

// 与后端 computeNextRun 保持一致：仅接受 1 <= min <= max <= 1440 的 @random A-B。
export function parseRandomSchedule(expr: string): { min: number; max: number } | null {
  if (typeof expr !== 'string') return null
  const match = RANDOM_SCHEDULE_PATTERN.exec(expr.trim())
  if (!match) return null
  const min = Number(match[1])
  const max = Number(match[2])
  if (!Number.isSafeInteger(min) || !Number.isSafeInteger(max)) return null
  if (min < 1 || max > 1440 || min > max) return null
  return { min, max }
}

// 字段看护规则不定时跑题（后端忽略它的 cron），只显示「按请求判定」，不显示保存着的 cron。
export function formatQualitySchedule(
  expr: string,
  t: (key: string, params?: Record<string, unknown>) => string,
  questionKind?: string,
): string {
  if (questionKind === FIELD_WATCH_QUESTION) return t('qualityOps.fieldWatchTag')
  const interval = parseRandomSchedule(expr)
  if (!interval) return expr ?? ''
  if (interval.min === interval.max) return t('qualityOps.fixedIntervalSchedule', { minutes: interval.min })
  return t('qualityOps.randomSchedule', { min: interval.min, max: interval.max })
}
