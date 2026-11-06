import { apiClient } from '../client'

// 首字趋势看板（只读）。字段与后端约定一致，见 first-token-trend-contract.md。
const base = '/admin/first-token-trend'

// 粒度由后端定：30m / 1h 每格 1 分钟，2h 每格 2 分钟，6h / 12h 每格 5 分钟。
export type FirstTokenTrendRange = '30m' | '1h' | '2h' | '6h' | '12h'
// 带 from/to 查询时后端返回 custom，粒度按时间长度定。
export type FirstTokenTrendResponseRange = FirstTokenTrendRange | 'custom'
// 首字口径只剩使用记录首字（含上传请求体）；字段只为兼容旧响应保留，前端不再传。
export type FirstTokenTrendMetric = 'usage' | 'switch'
export type FirstTokenTrendDimension = 'account' | 'proxy' | 'model'
export type FirstTokenTrendEventType = 'proxy' | 'concurrency' | 'status'

export interface FirstTokenTrendQuery {
  group_id: number
  // range 和 from/to 二选一：给 from/to 时不要传 range。
  range?: FirstTokenTrendRange
  from?: string
  to?: string
  dimension: FirstTokenTrendDimension
}

// 空桶也会返回（samples=0，数值字段 null），前端按下标对齐。
export interface FirstTokenTrendBucket {
  ts: string
  samples: number
  avg_ms: number | null
  p50_ms: number | null
  p80_ms: number | null
  // 百分数（0–100）
  over10_pct: number | null
  head_p50_ms: number | null
  upload_p50_ms: number | null
  after_p50_ms: number | null
  inflight_avg: number | null
  concurrency_limit_sum: number | null
}

export interface FirstTokenTrendRow {
  key: string
  label: string
  sub?: string | null
  samples: number
  avg_ms: number | null
  p50_ms: number | null
  p80_ms: number | null
  over10_pct: number | null
  inflight_avg: number | null
  // 每个桶的 p50，没数据为 null
  spark: (number | null)[]
}

export interface FirstTokenTrendEvent {
  at: string
  account_id: number
  account_name: string
  type: FirstTokenTrendEventType
  from: string
  to: string
  detail: string
  actor: string
  source: 'audit' | 'autoswitch' | 'system' | string
  p50_before_ms: number | null
  p50_after_ms: number | null
}

export interface FirstTokenTrendData {
  group: { id: number; name: string }
  range: FirstTokenTrendResponseRange
  step_seconds: number
  metric?: FirstTokenTrendMetric
  dimension: FirstTokenTrendDimension
  from: string
  to: string
  limit_is_current: boolean
  buckets: FirstTokenTrendBucket[]
  rows: FirstTokenTrendRow[]
  events: FirstTokenTrendEvent[]
}

export interface FirstTokenTrendGroup {
  id: number
  name: string
}

export async function getTrend(query: FirstTokenTrendQuery): Promise<FirstTokenTrendData> {
  const { data } = await apiClient.get<FirstTokenTrendData>(base, { params: query })
  return { ...data, buckets: data?.buckets ?? [], rows: data?.rows ?? [], events: data?.events ?? [] }
}

export async function listGroups(): Promise<FirstTokenTrendGroup[]> {
  const { data } = await apiClient.get<FirstTokenTrendGroup[] | { items?: FirstTokenTrendGroup[] }>(`${base}/groups`)
  if (Array.isArray(data)) return data
  return data?.items ?? []
}

export const firstTokenTrendAPI = { getTrend, listGroups }

export default firstTokenTrendAPI
