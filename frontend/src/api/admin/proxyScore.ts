import { apiClient } from '../client'

const base = '/admin/proxy-score'

export type ProxyScoreRange = '7d' | '30d'
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
}

// 只读看板：按 7d / 30d 给每个代理的首字表现打分。后端返回所有未删除代理，
// 没数据的代理 samples=0、数值为 null、grade 为空字符串。
export async function getProxyScore(range: ProxyScoreRange): Promise<ProxyScoreResult> {
  const { data } = await apiClient.get<ProxyScoreResult>(base, { params: { range } })
  return { ...data, rows: data?.rows ?? [] }
}

export const proxyScoreAPI = { getProxyScore }
export default proxyScoreAPI
