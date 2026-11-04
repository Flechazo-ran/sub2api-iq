import { apiClient } from '../client'

// 代理优选（首字或响应头慢自动换代理）。字段与后端约定一致，见 proxy-autoswitch-contract.md。
const base = '/admin/proxy-autoswitch'

export type ProxyAutoSwitchRegionPolicy = 'prefer_same' | 'same_only' | 'any'
// 判慢指标：first_token = 首字（first_semantic − request_written），response_headers = 响应头。
export type ProxyAutoSwitchMetric = 'response_headers' | 'first_token'

export interface ProxyAutoSwitchConfig {
  enabled: boolean
  window_minutes: number
  min_samples: number
  metric: ProxyAutoSwitchMetric
  threshold_ms: number
  consecutive_checks: number
  min_improvement_pct: number
  account_cooldown_hours: number
  max_switches_per_hour: number
  observe_minutes: number
  min_days_to_expiry: number
  region_policy: ProxyAutoSwitchRegionPolicy
  group_ids: number[]
}

export type ProxyAutoSwitchStatus =
  | 'observing'
  | 'kept'
  | 'kept_insufficient'
  | 'reverted'
  | 'revert_skipped'
  | 'no_candidate'
  | 'failed'
  | 'manual_revert'

export interface ProxyAutoSwitchCandidate {
  proxy_id: number
  label: string
  region: string
  probe_ms: number | null
  error: string
}

export interface ProxyAutoSwitchEvent {
  id: number
  created_at: string
  updated_at: string
  account_id: number
  account_name: string
  status: ProxyAutoSwitchStatus
  // 这条记录按哪个指标判的慢（换前 p50 和换后 p50 都是这个指标）
  metric: ProxyAutoSwitchMetric
  reason: string
  from_proxy_id: number
  from_proxy_label: string
  from_region: string
  to_proxy_id: number | null
  to_proxy_label: string | null
  to_region: string | null
  before_p50_ms: number | null
  before_samples: number | null
  current_probe_ms: number | null
  candidate_probe_ms: number | null
  after_p50_ms: number | null
  after_samples: number | null
  candidates: ProxyAutoSwitchCandidate[] | null
}

export interface ProxyAutoSwitchEventPage {
  items: ProxyAutoSwitchEvent[]
  next_cursor: number
}

export interface ProxyAutoSwitchRecent {
  account_id: number
  event_id: number
  at: string
  status: ProxyAutoSwitchStatus
  from_proxy_label: string
  to_proxy_label: string
  reason: string
}

export async function getConfig(): Promise<ProxyAutoSwitchConfig> {
  const { data } = await apiClient.get<ProxyAutoSwitchConfig>(`${base}/config`)
  return data
}

export async function updateConfig(config: ProxyAutoSwitchConfig): Promise<ProxyAutoSwitchConfig> {
  const { data } = await apiClient.put<ProxyAutoSwitchConfig>(`${base}/config`, config)
  return data
}

export async function listEvents(params: { before_id?: number; limit?: number; account_id?: number } = {}): Promise<ProxyAutoSwitchEventPage> {
  const query: Record<string, number> = { before_id: params.before_id ?? 0, limit: params.limit ?? 50 }
  if (params.account_id) query.account_id = params.account_id
  const { data } = await apiClient.get<Partial<ProxyAutoSwitchEventPage>>(`${base}/events`, { params: query })
  return { items: data?.items ?? [], next_cursor: data?.next_cursor ?? 0 }
}

export async function revertEvent(id: number): Promise<ProxyAutoSwitchEvent> {
  const { data } = await apiClient.post<ProxyAutoSwitchEvent>(`${base}/events/${id}/revert`)
  return data
}

export async function listRecent(days = 7): Promise<ProxyAutoSwitchRecent[]> {
  const { data } = await apiClient.get<{ items?: ProxyAutoSwitchRecent[] }>(`${base}/recent`, { params: { days } })
  return data?.items ?? []
}

export const proxyAutoSwitchAPI = { getConfig, updateConfig, listEvents, revertEvent, listRecent }

export default proxyAutoSwitchAPI
