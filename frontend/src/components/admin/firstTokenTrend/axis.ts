// x 轴刻度间隔：先按时间跨度选一个基准间隔，窄屏时再往上加，保证每个标签至少 56px。
const TICK_STEPS = [5, 10, 20, 30, 60, 120, 180, 240, 360] as const
const MIN_TICK_SPACING_PX = 56

export function tickEveryMinutes(spanMinutes: number, chartWidthPx = 0): number {
  const base = spanMinutes > 360 ? 60 : spanMinutes > 120 ? 30 : spanMinutes > 60 ? 10 : 5
  // 不知道宽度（例如 jsdom）时保持原来的密度。
  if (!(chartWidthPx > 0) || !(spanMinutes > 0)) return base

  const start = TICK_STEPS.findIndex(step => step >= base)
  for (let i = start < 0 ? TICK_STEPS.length - 1 : start; i < TICK_STEPS.length; i++) {
    const step = TICK_STEPS[i]
    if (chartWidthPx / (spanMinutes / step) >= MIN_TICK_SPACING_PX) return step
  }
  return TICK_STEPS[TICK_STEPS.length - 1]
}
