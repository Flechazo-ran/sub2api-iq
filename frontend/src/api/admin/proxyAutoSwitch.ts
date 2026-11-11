import { apiClient } from '../client'

// 代理优选 v2：按每个号在当前代理上的使用记录首字判断，从代理池（人工从 IP 管理里选的）里按延迟排名换代理。
// 字段与后端约定一致，见 proxy-autoswitch-v2-contract.md。
const base = '/admin/proxy-autoswitch'

export type ProxyAutoSwitchStrategy = 'random' | 'sequential' | 'region'

export interface ProxyAutoSwitchConfig {
  enabled: boolean
  group_ids: number[]
  window_minutes: number
  min_samples: number
  p60_threshold_ms: number
  mean_threshold_ms: number
  max_accounts_per_proxy: number
  // 代理池：人工从 IP 管理里选进来的代理 ID，换代理只从这里挑。
  candidate_proxy_ids: number[]
  strategy: ProxyAutoSwitchStrategy
}

export type ProxyAutoSwitchUpdate = ProxyAutoSwitchConfig

// 新记录的状态；observing / kept 等是旧记录的状态，老数据照常返回。
export type ProxyAutoSwitchStatus =
  | 'measuring'
  | 'done'
  | 'no_candidate'
  | 'failed'
  | 'observing'
  | 'kept'
  | 'kept_insufficient'
  | 'not_faster'
  | 'reverted'
  | 'revert_skipped'
  | 'manual_revert'

export type ProxyAutoSwitchAccountState = 'ok' | 'insufficient' | 'slow' | 'unavailable' | 'no_proxy'

export interface ProxyAutoSwitchEventStats {
  samples: number | null
  p60_ms: number | null
  mean_ms: number | null
  duration_mean_ms: number | null
}

export interface ProxyAutoSwitchEvent {
  id: number
  created_at: string
  switched_at: string | null
  status: ProxyAutoSwitchStatus
  account_id: number
  account_name: string
  group_id: number | null
  strategy: ProxyAutoSwitchStrategy | null
  reason: string
  from_proxy_id: number | null
  from_proxy_label: string | null
  from_region: string | null
  to_proxy_id: number | null
  to_proxy_label: string | null
  to_region: string | null
  window_minutes: number | null
  before: ProxyAutoSwitchEventStats | null
  after: ProxyAutoSwitchEventStats | null
  from_probe_ms: number | null
  to_probe_ms: number | null
  after_finished_at: string | null
}

export interface ProxyAutoSwitchEventPage {
  items: ProxyAutoSwitchEvent[]
  next_cursor: number
}

export interface ProxyAutoSwitchAccount {
  account_id: number
  account_name: string
  group_ids: number[]
  proxy_id: number | null
  proxy_label: string | null
  region: string | null
  samples: number
  p60_ms: number | null
  mean_ms: number | null
  duration_mean_ms: number | null
  state: ProxyAutoSwitchAccountState
  last_switch_at: string | null
  note: string
}

export interface ProxyAutoSwitchAccountPage {
  items: ProxyAutoSwitchAccount[]
  window_minutes: number
}

export interface ProxyAutoSwitchProxy {
  id: number
  name: string
  status: string
  region: string
  expires_at: string | null
  expired: boolean
  account_count: number
  // 已保存的代理池里有没有它（页面上的草稿以 candidate_proxy_ids 为准）
  in_pool: boolean
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

export async function updateConfig(config: ProxyAutoSwitchUpdate): Promise<ProxyAutoSwitchConfig> {
  const { data } = await apiClient.put<ProxyAutoSwitchConfig>(`${base}/config`, config)
  return data
}

export async function listEvents(params: { before_id?: number; limit?: number; account_id?: number } = {}): Promise<ProxyAutoSwitchEventPage> {
  const query: Record<string, number> = { before_id: params.before_id ?? 0, limit: params.limit ?? 50 }
  if (params.account_id) query.account_id = params.account_id
  const { data } = await apiClient.get<Partial<ProxyAutoSwitchEventPage>>(`${base}/events`, { params: query })
  return { items: data?.items ?? [], next_cursor: data?.next_cursor ?? 0 }
}

export async function listAccounts(): Promise<ProxyAutoSwitchAccountPage> {
  const { data } = await apiClient.get<Partial<ProxyAutoSwitchAccountPage>>(`${base}/accounts`)
  return { items: data?.items ?? [], window_minutes: data?.window_minutes ?? 0 }
}

export async function listProxies(): Promise<ProxyAutoSwitchProxy[]> {
  const { data } = await apiClient.get<{ items?: ProxyAutoSwitchProxy[] }>(`${base}/proxies`)
  return data?.items ?? []
}

export async function listRecent(days = 7): Promise<ProxyAutoSwitchRecent[]> {
  const { data } = await apiClient.get<{ items?: ProxyAutoSwitchRecent[] }>(`${base}/recent`, { params: { days } })
  return data?.items ?? []
}

export const proxyAutoSwitchAPI = { getConfig, updateConfig, listEvents, listAccounts, listProxies, listRecent }

export default proxyAutoSwitchAPI
