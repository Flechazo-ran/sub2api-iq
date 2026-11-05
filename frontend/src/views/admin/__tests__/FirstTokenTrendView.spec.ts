import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import FirstTokenTrendView from '../FirstTokenTrendView.vue'
import zhFirstTokenTrend from '@/i18n/locales/zh/firstTokenTrend'
import type { FirstTokenTrendBucket, FirstTokenTrendData } from '@/api/admin/firstTokenTrend'

const { api } = vi.hoisted(() => ({ api: { getTrend: vi.fn(), listGroups: vi.fn() } }))
vi.mock('@/api/admin/firstTokenTrend', () => ({ firstTokenTrendAPI: api, default: api }))
vi.mock('vue-chartjs', () => ({
  Line: { name: 'Line', props: ['data', 'options', 'plugins'], template: '<div class="line-chart" />' },
  Bar: { name: 'Bar', props: ['data', 'options', 'plugins'], template: '<div class="bar-chart" />' },
}))

// 6 小时、5 分钟一格的末尾 4 格；最后一格（16:00 起）还没走完，6 个数应取 15:55 那格。
// 时间都是北京时间：07:45Z = 15:45。
function bucket(ts: string, overrides: Partial<FirstTokenTrendBucket> = {}): FirstTokenTrendBucket {
  return { ts, samples: 400, avg_ms: 20000, p50_ms: 16000, p80_ms: 27000, over10_pct: 70, head_p50_ms: 1300, upload_p50_ms: 8000, after_p50_ms: 6800, inflight_avg: 29.4, concurrency_limit_sum: 30, ...overrides }
}
function trend(overrides: Partial<FirstTokenTrendData> = {}): FirstTokenTrendData {
  return {
    group: { id: 16, name: 'GPT-速刷-AUTO' }, range: '6h', step_seconds: 300, metric: 'usage', dimension: 'account',
    from: '2026-11-05T02:00:00Z', to: '2026-11-05T08:02:30Z', limit_is_current: false,
    buckets: [
      bucket('2026-11-05T07:45:00Z'),
      bucket('2026-11-05T07:50:00Z', { avg_ms: 22000, p50_ms: 17600, p80_ms: 28000, over10_pct: 75, inflight_avg: 28, samples: 462 }),
      bucket('2026-11-05T07:55:00Z', { avg_ms: 12600, p50_ms: 9400, p80_ms: 15200, over10_pct: 32, inflight_avg: 18, samples: 527 }),
      bucket('2026-11-05T08:00:00Z', { samples: 0, avg_ms: null, p50_ms: null, p80_ms: null, over10_pct: null, head_p50_ms: null, upload_p50_ms: null, after_p50_ms: null, inflight_avg: null }),
    ],
    rows: [
      { key: '98', label: '#98 jeremiassmith649', sub: '当前代理 121', samples: 9950, avg_ms: 21400, p50_ms: 16800, p80_ms: 27900, over10_pct: 78, inflight_avg: 24.6, spark: [15000, 17000, 9000, null] },
      { key: '97', label: '#97 fingermaikon7', sub: '14:49 起 token 过期', samples: 1690, avg_ms: 9000, p50_ms: 8600, p80_ms: 11000, over10_pct: 20, inflight_avg: 7.1, spark: [null, null, null, null] },
      { key: '99', label: '#99 new', sub: null, samples: 0, avg_ms: null, p50_ms: null, p80_ms: null, over10_pct: null, inflight_avg: null, spark: [null, null, null, null] },
    ],
    events: [
      { at: '2026-11-05T07:53:12Z', account_id: 98, account_name: 'jeremiassmith649', type: 'proxy', from: '77', to: '99', detail: '代理 77 → 99（越南河内）', actor: 'Claude', source: 'audit', p50_before_ms: 18300, p50_after_ms: 9400 },
      { at: '2026-11-05T08:00:13Z', account_id: 98, account_name: 'jeremiassmith649', type: 'concurrency', from: '30', to: '100', detail: '并发上限 30 → 100', actor: '1241552681@qq.com', source: 'audit', p50_before_ms: 9400, p50_after_ms: null },
      { at: '2026-11-05T07:49:10Z', account_id: 97, account_name: 'fingermaikon7', type: 'status', from: 'active', to: 'error', detail: 'token 过期，变成 error', actor: '系统', source: 'system', p50_before_ms: 15800, p50_after_ms: 18500 },
    ],
    ...overrides,
  }
}

function mountView() {
  const i18n = createI18n({ legacy: false, locale: 'zh', missingWarn: false, fallbackWarn: false, messages: { zh: { firstTokenTrend: zhFirstTokenTrend, nav: { routing: '线路优化' } } } })
  return mount(FirstTokenTrendView, { global: { plugins: [i18n], stubs: { AppLayout: { template: '<main><slot /></main>' }, RoutingNav: true } } })
}
let wrapper: ReturnType<typeof mountView>
const lastQuery = () => api.getTrend.mock.calls.at(-1)?.[0]
const charts = (name: 'Line' | 'Bar') => wrapper.findAllComponents({ name })

beforeEach(() => {
  localStorage.clear()
  api.getTrend.mockReset().mockResolvedValue(trend())
  api.listGroups.mockReset().mockResolvedValue([{ id: 16, name: 'GPT-速刷-AUTO' }, { id: 20, name: '1028GPT-PRO' }])
})
afterEach(() => wrapper?.unmount())

describe('FirstTokenTrendView', () => {
  it('loads group 16, 6h, usage, by account and renders the latest complete bucket', async () => {
    wrapper = mountView(); await flushPromises()
    expect(api.getTrend).toHaveBeenCalledTimes(1)
    expect(lastQuery()).toEqual({ group_id: 16, range: '6h', metric: 'usage', dimension: 'account' })
    expect((wrapper.get('[data-testid="ftt-group"]').element as HTMLSelectElement).value).toBe('16')
    expect(wrapper.get('[data-testid="ftt-range-6h"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[data-testid="ftt-step"]').text()).toBe('5 分钟一格')
    expect(wrapper.get('[data-testid="ftt-auto-refresh"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[data-testid="ftt-kpi-note"]').text()).toContain('15:55–16:00')
    const p50 = wrapper.get('[data-testid="ftt-kpi-p50"]')
    expect(p50.get('.v').text()).toBe('9.4s')
    expect(p50.get('.d').text()).toBe('↓ 8.2s')
    expect(p50.get('.d').classes()).toContain('down')
    expect(wrapper.get('[data-testid="ftt-kpi-over10"] .v').text()).toBe('32%')
    expect(wrapper.get('[data-testid="ftt-kpi-inflight"] .d').classes()).not.toContain('down')
    expect(wrapper.get('[data-testid="ftt-kpi-samples"] .v').text()).toBe('527')
    expect(wrapper.get('[data-testid="ftt-metric-hint"]').text()).toBe(zhFirstTokenTrend.metricHint.usage)
  })

  it('draws three lines plus the 10 s line, in-flight vs limit, and the stacked split, in seconds', async () => {
    wrapper = mountView(); await flushPromises()
    const [trendChart, concChart] = charts('Line')
    const trendSets = trendChart.props('data').datasets
    expect(trendSets.map((set: { label: string }) => set.label)).toEqual(['平均', 'p50', 'p80', '10 秒判慢线'])
    expect(trendSets[1].data).toEqual([16, 17.6, 9.4, null])
    expect(trendSets[3].data).toEqual([10, 10, 10, 10])
    expect(trendChart.props('data').labels).toEqual(['15:45', '15:50', '15:55', '16:00'])
    const concSets = concChart.props('data').datasets
    expect(concSets[0].data).toEqual([29.4, 28, 18, null])
    expect(concSets[1].borderDash).toEqual([5, 4])
    const split = charts('Bar')[0]
    expect(split.props('data').datasets.map((set: { data: (number | null)[] }) => set.data[2])).toEqual([1.3, 8, 6.8])
    expect(split.props('options').scales.y.stacked).toBe(true)
    expect(wrapper.find('[data-testid="ftt-limit-current"]').exists()).toBe(false)
  })

  it('keeps at least one series switched on', async () => {
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="ftt-series-avg"]').trigger('click')
    await wrapper.get('[data-testid="ftt-series-p80"]').trigger('click')
    await wrapper.get('[data-testid="ftt-series-p50"]').trigger('click')
    expect(wrapper.get('[data-testid="ftt-series-p50"]').attributes('aria-pressed')).toBe('true')
    expect(charts('Line')[0].props('data').datasets.map((set: { label: string }) => set.label)).toEqual(['p50', '10 秒判慢线'])
  })

  it('lists changes newest first with before/after p50 and the effect', async () => {
    wrapper = mountView(); await flushPromises()
    const rows = wrapper.findAll('[data-testid="ftt-event-row"]')
    expect(rows).toHaveLength(3)
    expect(rows[0].text()).toContain('16:00:13')
    expect(rows[0].text()).toContain('并发上限')
    expect(rows[0].get('[data-testid="ftt-effect"]').text()).toBe('等数据')
    expect(rows[1].text()).toContain('代理 77 → 99（越南河内）')
    expect(rows[1].text()).toContain('18.3s')
    expect(rows[1].get('[data-testid="ftt-effect"]').text()).toBe('↓ 快了 8.9s')
    expect(rows[2].get('[data-testid="ftt-effect"]').text()).toBe('↑ 慢了 2.7s')
  })

  it('shows the breakdown with status by p50 and a sparkline', async () => {
    wrapper = mountView(); await flushPromises()
    const rows = wrapper.findAll('[data-testid="ftt-row"]')
    expect(rows.map(row => row.get('[data-testid="ftt-row-status"]').text())).toEqual(['偏慢', '正常', '暂无'])
    expect(rows[0].text()).toContain('16.8s')
    expect(rows[0].text()).toContain('78%')
    expect(rows[0].find('[data-testid="ftt-spark"]').exists()).toBe(true)
    expect(rows[1].find('[data-testid="ftt-spark"]').exists()).toBe(false)
    expect(rows[2].text()).toContain(zhFirstTokenTrend.dims.noRowData)
  })

  it('reloads when the measure changes and swaps the explanation', async () => {
    wrapper = mountView(); await flushPromises()
    api.getTrend.mockResolvedValueOnce(trend({ metric: 'switch' }))
    await wrapper.get('[data-testid="ftt-metric-switch"]').trigger('click'); await flushPromises()
    expect(api.getTrend).toHaveBeenCalledTimes(2)
    expect(lastQuery()).toMatchObject({ metric: 'switch', dimension: 'account' })
    expect(wrapper.get('[data-testid="ftt-metric-hint"]').text()).toBe(zhFirstTokenTrend.metricHint.switch)
  })

  it('reloads when the breakdown dimension or range changes', async () => {
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="ftt-dim-proxy"]').trigger('click'); await flushPromises()
    expect(lastQuery()).toMatchObject({ dimension: 'proxy', metric: 'usage' })
    expect(wrapper.get('[data-testid="ftt-dim-proxy"]').attributes('aria-checked')).toBe('true')
    await wrapper.get('[data-testid="ftt-range-24h"]').trigger('click'); await flushPromises()
    expect(lastQuery()).toMatchObject({ range: '24h', dimension: 'proxy' })
    expect(api.getTrend).toHaveBeenCalledTimes(3)
  })

  it('shows an empty hint when the range has no samples', async () => {
    const empty = trend()
    api.getTrend.mockResolvedValue({ ...empty, buckets: empty.buckets.map(b => ({ ...b, samples: 0, avg_ms: null, p50_ms: null, p80_ms: null })), rows: [], events: [] })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="ftt-empty"]').text()).toContain('这段时间没有数据')
    expect(charts('Line')).toHaveLength(0)
    expect(wrapper.find('[data-testid="ftt-kpis"]').exists()).toBe(false)
  })

  it('shows loading first, then the error with a working retry', async () => {
    let reject: (reason: unknown) => void = () => {}
    api.getTrend.mockReturnValueOnce(new Promise((_, r) => { reject = r }))
    wrapper = mountView(); await flushPromises()
    expect(wrapper.find('[data-testid="ftt-loading"]').exists()).toBe(true)
    reject({ message: '查询超时' }); await flushPromises()
    expect(wrapper.get('[data-testid="ftt-error"]').text()).toContain('查询超时')
    await wrapper.get('[data-testid="ftt-retry"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="ftt-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="ftt-kpis"]').exists()).toBe(true)
  })

  it('notes when the concurrency limit is only the current value', async () => {
    api.getTrend.mockResolvedValue(trend({ limit_is_current: true }))
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="ftt-limit-current"]').text()).toBe(zhFirstTokenTrend.concurrency.limitIsCurrent)
  })
})
