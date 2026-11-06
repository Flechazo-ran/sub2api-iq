// 纵轴压尖峰：上限按当前显示的所有点里 95% 位置的值 × 1.3 算，向上取整到 10。
// 首字类最低 20 秒，同时在跑最低 10 个；没有点超过上限时照原样显示，不标尖峰。
export type SpikeScaleKind = 'seconds' | 'count'

export interface AxisCap {
  max: number
  capped: boolean
}

export interface SpikePoint {
  index: number
  value: number
}

const STEP = 10

// 与设计稿一致：排在 95% 位置的点（floor(p * (n - 1))）。
function percentile95(values: number[]): number {
  if (!values.length) return 0
  const sorted = [...values].sort((a, b) => a - b)
  return sorted[Math.min(sorted.length - 1, Math.floor(0.95 * (sorted.length - 1)))]
}

function roundUp(value: number, minimum: number): number {
  return Math.max(minimum, Math.ceil(value / STEP) * STEP)
}

export function computeAxisCap(
  values: readonly (number | null | undefined)[],
  kind: SpikeScaleKind,
  enabled = true,
): AxisCap {
  const minimum = kind === 'seconds' ? 20 : 10
  const clean = values.filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  if (!clean.length) return { max: minimum, capped: false }

  // 不压尖峰时按最大值显示全貌。
  const actualTop = roundUp(Math.max(...clean), minimum)
  if (!enabled) return { max: actualTop, capped: false }

  const cappedTop = roundUp(percentile95(clean) * 1.3, minimum)
  return cappedTop < actualTop ? { max: cappedTop, capped: true } : { max: actualTop, capped: false }
}

// 超出纵轴上限的点；图表插件按这些下标在图顶画 ▲。
export function pointsAbove(
  values: readonly (number | null | undefined)[],
  max: number,
): SpikePoint[] {
  return values.flatMap((value, index) =>
    typeof value === 'number' && Number.isFinite(value) && value > max ? [{ index, value }] : [],
  )
}
