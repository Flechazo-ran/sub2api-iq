import { apiClient } from '../client'

// 首字趋势看板（只读）。字段与后端约定一致，见 first-token-trend-contract.md。
const base = '/admin/first-token-trend'

export type FirstTokenTrendRange = '1h' | '6h' | '24h' | '7d'
// usage = 使用记录首字（含上传请求体）；switch = 代理优选首字（first_semantic − request_written）。
export type FirstTokenTrendMetric = 'usage' | 'switch'
export type FirstTokenTrendDimension = 'account' | 'proxy' | 'model'
export type FirstTokenTrendEventType = 'proxy' | 'concurrency' | 'status'

export interface FirstTokenTrendQuery {
  group_id: number
  range: FirstTokenTrendRange
  metric: FirstTokenTrendMetric
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
  range: FirstTokenTrendRange
  step_seconds: number
  metric: FirstTokenTrendMetric
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
