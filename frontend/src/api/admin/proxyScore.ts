import { apiClient } from '../client'

const base = '/admin/proxy-score'

export type ProxyScoreRange = '3d' | '7d' | '30d'
export type ProxyScoreGrade = 'S' | 'A' | 'B' | 'C' | 'D' | ''
export type ProxyScoreAdvice = 'renew' | 'observe' | 'drop'

export interface ProxyScoreRow {
  id: number
  name: string
  status: string
  expires_at: string | null
  samples: number
  p50_ms: number | null
  mean_ms: number | null
  p80_ms: number | null
  judged_slow: number
  grade: ProxyScoreGrade
  advice: ProxyScoreAdvice
}

export interface ProxyScoreResult {
  range: ProxyScoreRange
  rows: ProxyScoreRow[]
  // 后台预计算数据最近一次重算的时间；还没算过时为 null。
  computed_at: string | null
}

// 只读看板：按 3d / 7d / 30d 给每个代理的首字表现打分。数据由后台每 10 分钟预计算，
// 后端返回所有未删除代理，没数据的代理 samples=0、数值为 null、grade 为空字符串。
export async function getProxyScore(range: ProxyScoreRange): Promise<ProxyScoreResult> {
  const { data } = await apiClient.get<ProxyScoreResult>(base, { params: { range } })
  return { ...data, rows: data?.rows ?? [], computed_at: data?.computed_at ?? null }
}

export const proxyScoreAPI = { getProxyScore }
export default proxyScoreAPI
