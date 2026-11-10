import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import FirstTokenTrendView from '../FirstTokenTrendView.vue'
import zhFirstTokenTrend from '@/i18n/locales/zh/firstTokenTrend'
import type { FirstTokenTrendBucket, FirstTokenTrendData, FirstTokenTrendRange } from '@/api/admin/firstTokenTrend'

const { api } = vi.hoisted(() => ({ api: { getTrend: vi.fn(), listGroups: vi.fn() } }))
vi.mock('@/api/admin/firstTokenTrend', () => ({ firstTokenTrendAPI: api, default: api }))
vi.mock('vue-chartjs', () => ({
  Line: { name: 'Line', props: ['data', 'options', 'plugins'], template: '<div class="line-chart" />' },
  Bar: { name: 'Bar', props: ['data', 'options', 'plugins'], template: '<div class="bar-chart" />' },
}))

// 2 小时、2 分钟一格的末尾 4 格；最后一格（16:00 起）还没走完，6 个数应取 15:58 那格。
// 时间都是北京时间：07:54Z = 15:54。
function bucket(ts: string, overrides: Partial<FirstTokenTrendBucket> = {}): FirstTokenTrendBucket {
  return { ts, samples: 400, avg_ms: 20000, p50_ms: 16000, p80_ms: 27000, over10_pct: 70, head_p50_ms: 1300, upload_p50_ms: 8000, after_p50_ms: 6800, inflight_avg: 29.4, concurrency_limit_sum: 30, ...overrides }
}
function trend(overrides: Partial<FirstTokenTrendData> = {}): FirstTokenTrendData {
  return {
    group: { id: 16, name: 'GPT-速刷-AUTO' }, range: '2h', step_seconds: 120, metric: 'usage', dimension: 'account',
    account_id: null,
    accounts: [
      { id: 98, label: '#98 jeremiassmith649', status: 'active', schedulable: true },
      { id: 97, label: '#97 fingermaikon7', status: 'error', schedulable: false },
      { id: 99, label: '#99 new', status: 'active', schedulable: false },
    ],
    from: '2026-11-05T06:02:00Z', to: '2026-11-05T08:01:00Z', limit_is_current: false,
    buckets: [
      bucket('2026-11-05T07:54:00Z'),
      bucket('2026-11-05T07:56:00Z', { avg_ms: 22000, p50_ms: 17600, p80_ms: 28000, over10_pct: 75, inflight_avg: 28, samples: 462 }),
      bucket('2026-11-05T07:58:00Z', { avg_ms: 12600, p50_ms: 9400, p80_ms: 15200, over10_pct: 32, inflight_avg: 18, samples: 527 }),
      bucket('2026-11-05T08:00:00Z', { samples: 0, avg_ms: null, p50_ms: null, p80_ms: null, over10_pct: null, head_p50_ms: null, upload_p50_ms: null, after_p50_ms: null, inflight_avg: null }),
    ],
    rows: [
      { key: '98', label: '#98 jeremiassmith649', sub: '当前代理 121', samples: 9950, avg_ms: 21400, p50_ms: 16800, p80_ms: 27900, over10_pct: 78, inflight_avg: 24.6, spark: [15000, 17000, 9000, null] },
      { key: '97', label: '#97 fingermaikon7', sub: '14:49 起 token 过期', samples: 1690, avg_ms: 9000, p50_ms: 8600, p80_ms: 11000, over10_pct: 20, inflight_avg: 7.1, spark: [null, null, null, null] },
      { key: '99', label: '#99 new', sub: null, samples: 0, avg_ms: null, p50_ms: null, p80_ms: null, over10_pct: null, inflight_avg: null, spark: [null, null, null, null] },
    ],
    events: [
      { at: '2026-11-05T07:57:12Z', account_id: 98, account_name: 'jeremiassmith649', type: 'proxy', from: '77', to: '99', detail: '代理 77 → 99（越南河内）', actor: 'Claude', source: 'audit', p50_before_ms: 18300, p50_after_ms: 9400 },
      { at: '2026-11-05T08:00:13Z', account_id: 98, account_name: 'jeremiassmith649', type: 'concurrency', from: '30', to: '100', detail: '并发上限 30 → 100', actor: '1241552681@qq.com', source: 'audit', p50_before_ms: 9400, p50_after_ms: null },
      { at: '2026-11-05T07:55:10Z', account_id: 97, account_name: 'fingermaikon7', type: 'status', from: 'active', to: 'error', detail: 'token 过期，变成 error', actor: '系统', source: 'system', p50_before_ms: 15800, p50_after_ms: 18500 },
    ],
    ...overrides,
  }
}
// 生成一整段连续桶，用来验证 12 小时 / 6 小时的刻度密度。
function rangeTrend(range: FirstTokenTrendRange, stepSeconds: number, count: number, from: string, to: string): FirstTokenTrendData {
  const start = new Date(from).getTime()
  const buckets = Array.from({ length: count }, (_, index) => bucket(new Date(start + index * stepSeconds * 1000).toISOString(), {
    avg_ms: 6000, p50_ms: 5000, p80_ms: 7000, over10_pct: 0, head_p50_ms: 1000, upload_p50_ms: 2000, after_p50_ms: 2000, inflight_avg: 2, samples: 10,
  }))
  return trend({ range, step_seconds: stepSeconds, from, to, buckets })
}
// 10 个点里放一个 96 秒的 p80 尖峰，其余都在 5–8 秒。
function spikeTrend(withSpike: boolean): FirstTokenTrendData {
  const start = new Date('2026-11-05T07:00:00Z').getTime()
  const buckets = Array.from({ length: 10 }, (_, index) => bucket(new Date(start + index * 60000).toISOString(), {
    avg_ms: 6000 + index * 100,
    p50_ms: 5500 + index * 100,
    p80_ms: withSpike && index === 5 ? 96000 : 6500 + index * 100,
    over10_pct: withSpike && index === 5 ? 100 : 0,
    samples: 100,
  }))
  return trend({ range: '30m', step_seconds: 60, from: '2026-11-05T07:00:00Z', to: '2026-11-05T07:10:00Z', buckets })
}

function mountView() {
  const i18n = createI18n({ legacy: false, locale: 'zh', missingWarn: false, fallbackWarn: false, messages: { zh: { firstTokenTrend: zhFirstTokenTrend, nav: { routing: '线路优化' } } } })
  return mount(FirstTokenTrendView, { global: { plugins: [i18n], stubs: { AppLayout: { template: '<main><slot /></main>' }, RoutingNav: true } } })
}
let wrapper: ReturnType<typeof mountView>
const lastQuery = () => api.getTrend.mock.calls.at(-1)?.[0]
const charts = (name: 'Line' | 'Bar') => wrapper.findAllComponents({ name })
// 总览条在 jsdom 里没有布局，给它一个 1000px 宽的矩形，方便按比例拖。
function dragOverview(fromX: number, toX: number) {
  const overview = wrapper.get('[data-testid="ftt-overview"]')
  overview.element.getBoundingClientRect = () => ({
    left: 0, top: 0, width: 1000, height: 70, right: 1000, bottom: 70, x: 0, y: 0, toJSON: () => ({}),
  })
  const fire = (type: string, clientX: number) => {
    overview.element.dispatchEvent(new MouseEvent(type, { bubbles: true, clientX, button: 0 }))
  }
  fire('pointerdown', fromX)
  fire('pointermove', toX)
  fire('pointerup', toX)
}
function selectAccount(value: string) {
  return wrapper.get('[data-testid="ftt-account"]').setValue(value)
}

beforeEach(() => {
  // 用例里的响应都固定在 2026-11-05；把 Date.now 钉在数据附近，否则真实时钟一旦晚于数据
  // 超过 12 小时，按当前时钟夹取会把框选窗口整个夹掉。只桩 Date.now，定时器保持真实。
  vi.spyOn(Date, 'now').mockReturnValue(new Date('2026-11-05T08:31:00Z').getTime())
  localStorage.clear()
  api.getTrend.mockReset().mockResolvedValue(trend())
  api.listGroups.mockReset().mockResolvedValue([{ id: 16, name: 'GPT-速刷-AUTO' }, { id: 20, name: '1028GPT-PRO' }])
})
afterEach(() => { wrapper?.unmount(); vi.restoreAllMocks() })

describe('FirstTokenTrendView', () => {
  it('loads group 16, 2h, by account and renders the latest complete bucket', async () => {
    wrapper = mountView(); await flushPromises()
    expect(api.getTrend).toHaveBeenCalledTimes(1)
    expect(lastQuery()).toEqual({ group_id: 16, range: '2h', dimension: 'account' })
    expect(lastQuery()).not.toHaveProperty('metric')
    expect((wrapper.get('[data-testid="ftt-group"]').element as HTMLSelectElement).value).toBe('16')
    expect(wrapper.get('[data-testid="ftt-range-2h"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[data-testid="ftt-step"]').text()).toBe('2 分钟一格')
    expect(wrapper.get('[data-testid="ftt-auto-refresh"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[data-testid="ftt-kpi-note"]').text()).toContain('15:58–16:00')
    const p50 = wrapper.get('[data-testid="ftt-kpi-p50"]')
    expect(p50.get('.v').text()).toBe('9.4s')
    expect(p50.get('.d').text()).toBe('↓ 8.2s')
    expect(p50.get('.d').classes()).toContain('down')
    expect(wrapper.get('[data-testid="ftt-kpi-over10"] .v').text()).toBe('32%')
    expect(wrapper.get('[data-testid="ftt-kpi-inflight"] .d').classes()).not.toContain('down')
    expect(wrapper.get('[data-testid="ftt-kpi-samples"] .v').text()).toBe('527')
    expect(wrapper.get('[data-testid="ftt-metric-hint"]').text()).toBe('首字和使用记录页一致，从请求进网关算，含上传请求体的时间')
  })

  it('lists all accounts as the default and then every account returned for the group', async () => {
    wrapper = mountView(); await flushPromises()
    expect(lastQuery()).not.toHaveProperty('account_id')
    const options = wrapper.findAll('[data-testid="ftt-account"] option')
    expect(options.map(option => option.text())).toEqual([
      '全部账号（不区分）',
      '#98 jeremiassmith649',
      '#97 fingermaikon7（不可用）',
      '#99 new（不可用）',
    ])
  })

  it('requests only the selected account and goes back to the whole group', async () => {
    wrapper = mountView(); await flushPromises()
    await selectAccount('98'); await flushPromises()
    expect(lastQuery()).toEqual({ group_id: 16, range: '2h', dimension: 'account', account_id: 98 })
    await selectAccount('null'); await flushPromises()
    expect(lastQuery()).toEqual({ group_id: 16, range: '2h', dimension: 'account' })
  })

  it('resets the account filter when the group changes', async () => {
    wrapper = mountView(); await flushPromises()
    await selectAccount('98'); await flushPromises()
    await wrapper.get('[data-testid="ftt-group"]').setValue('20'); await flushPromises()
    expect(api.getTrend).toHaveBeenCalledTimes(3)
    expect(lastQuery()).toEqual({ group_id: 20, range: '2h', dimension: 'account' })
    expect((wrapper.get('[data-testid="ftt-account"]').element as HTMLSelectElement).selectedIndex).toBe(0)
  })

  it('empties the account dropdown while the new group loads and refills it afterwards', async () => {
    let releaseGroup: (value: FirstTokenTrendData) => void = () => {}
    const groupPending = new Promise<FirstTokenTrendData>(resolve => { releaseGroup = resolve })
    api.getTrend.mockImplementation((query: { group_id: number }) =>
      query.group_id === 20 ? groupPending : Promise.resolve(trend()))
    wrapper = mountView(); await flushPromises()
    await selectAccount('98'); await flushPromises()
    await wrapper.get('[data-testid="ftt-group"]').setValue('20')
    const optionTexts = () => wrapper.findAll('[data-testid="ftt-account"] option').map(option => option.text())
    // 新分组的请求还挂着，下拉里不能留旧分组的号。
    expect(optionTexts()).toEqual(['全部账号（不区分）'])
    expect(api.getTrend.mock.calls.filter(call => call[0].group_id === 20).every(call => !('account_id' in call[0]))).toBe(true)
    releaseGroup(trend({ group: { id: 20, name: '1028GPT-PRO' }, accounts: [{ id: 201, label: '#201 pro-a', status: 'active', schedulable: true }] }))
    await flushPromises()
    expect(optionTexts()).toEqual(['全部账号（不区分）', '#201 pro-a'])
    expect((wrapper.get('[data-testid="ftt-account"]').element as HTMLSelectElement).selectedIndex).toBe(0)
  })

  it('keeps the selected window when switching accounts', async () => {
    wrapper = mountView(); await flushPromises()
    dragOverview(100, 400); await flushPromises()
    const window = { from: lastQuery().from, to: lastQuery().to }
    await selectAccount('98'); await flushPromises()
    expect(lastQuery()).toMatchObject({ group_id: 16, dimension: 'account', account_id: 98, from: window.from, to: window.to })
    expect(lastQuery()).not.toHaveProperty('range')
    expect(wrapper.get('[data-testid="ftt-selection-label"]').exists()).toBe(true)
  })

  it('refreshes the overview strip for the new account while a selection is active', async () => {
    const accountTrend = trend({
      buckets: [
        bucket('2026-11-05T07:54:00Z', { p50_ms: 30000 }),
        bucket('2026-11-05T07:56:00Z', { p50_ms: 30000 }),
        bucket('2026-11-05T07:58:00Z', { p50_ms: 30000 }),
        bucket('2026-11-05T08:00:00Z', { p50_ms: 30000 }),
      ],
    })
    api.getTrend.mockImplementation((query: { account_id?: number; range?: string }) =>
      query.account_id === 98 && query.range ? Promise.resolve(accountTrend) : Promise.resolve(trend()))
    wrapper = mountView(); await flushPromises()
    dragOverview(100, 400); await flushPromises()
    const before = wrapper.findAll('[data-testid="ftt-overview-line"]').map(line => line.attributes('points'))
    await selectAccount('98'); await flushPromises()
    const queries = api.getTrend.mock.calls.map(call => call[0])
    // ① 总览条请求：整范围 + 新账号，不带 from/to。
    expect(queries).toContainEqual({ group_id: 16, range: '2h', dimension: 'account', account_id: 98 })
    // ② 主区请求：带 from/to + 新账号，不带 range。
    const windowQueries = queries.filter(query => query.from && query.to && query.account_id === 98)
    expect(windowQueries.length).toBeGreaterThan(0)
    expect(windowQueries.every(query => !query.range)).toBe(true)
    // ③ 最后一次仍是主区请求。
    expect(lastQuery()).toMatchObject({ account_id: 98, from: expect.any(String), to: expect.any(String) })
    expect(lastQuery()).not.toHaveProperty('range')
    // ④ 总览条换成新账号的数据，框选状态还在。
    const after = wrapper.findAll('[data-testid="ftt-overview-line"]').map(line => line.attributes('points'))
    expect(after).not.toEqual(before)
    expect(wrapper.get('[data-testid="ftt-selection-label"]').exists()).toBe(true)
  })

  it('ignores a stale overview refresh after the selection is cleared', async () => {
    let release: (value: FirstTokenTrendData) => void = () => {}
    const pending = new Promise<FirstTokenTrendData>(resolve => { release = resolve })
    const stale = trend({
      buckets: [
        bucket('2026-11-05T07:54:00Z', { p50_ms: 30000 }),
        bucket('2026-11-05T07:56:00Z', { p50_ms: 30000 }),
        bucket('2026-11-05T07:58:00Z', { p50_ms: 30000 }),
        bucket('2026-11-05T08:00:00Z', { p50_ms: 30000 }),
      ],
    })
    const fresh = trend({
      buckets: [
        bucket('2026-11-05T07:54:00Z', { p50_ms: 2000 }),
        bucket('2026-11-05T07:56:00Z', { p50_ms: 2000 }),
        bucket('2026-11-05T07:58:00Z', { p50_ms: 2000 }),
        bucket('2026-11-05T08:00:00Z', { p50_ms: 2000 }),
      ],
    })
    // 同样形状的整范围 query 按调用顺序区分：第一次挂起（换号时的总览条），第二次返回 B（取消框选）。
    let rangeCalls = 0
    api.getTrend.mockImplementation((query: { account_id?: number; range?: string }) => {
      if (query.account_id === 98 && query.range) {
        rangeCalls += 1
        return rangeCalls === 1 ? pending : Promise.resolve(fresh)
      }
      return Promise.resolve(trend())
    })
    wrapper = mountView(); await flushPromises()
    dragOverview(100, 400); await flushPromises()
    await selectAccount('98'); await flushPromises()
    await wrapper.get('[data-testid="ftt-clear-selection"]').trigger('click'); await flushPromises()
    const afterCancel = wrapper.findAll('[data-testid="ftt-overview-line"]').map(line => line.attributes('points'))
    release(stale); await flushPromises()
    expect(rangeCalls).toBe(2)
    expect(wrapper.findAll('[data-testid="ftt-overview-line"]').map(line => line.attributes('points'))).toEqual(afterCancel)
  })

  it('clamps the selection start to now minus 12 hours plus 1 minute when the overview is stale', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    try {
      vi.setSystemTime(new Date('2026-11-05T08:31:00Z'))
      api.getTrend.mockResolvedValue(rangeTrend('12h', 300, 144, '2026-11-04T20:01:00Z', '2026-11-05T08:01:00Z'))
      wrapper = mountView(); await flushPromises()
      dragOverview(0, 500); await flushPromises()
      const query = lastQuery()
      expect(query.from).toBe('2026-11-04T20:32:00.000Z')
      expect(new Date(query.to).getTime()).toBeLessThanOrEqual(new Date('2026-11-05T08:31:00Z').getTime())
    } finally {
      vi.useRealTimers()
    }
  })

  it('clamps the selection end to now when the browser clock is behind the response', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    try {
      vi.setSystemTime(new Date('2026-11-05T07:31:00Z'))
      api.getTrend.mockResolvedValue(rangeTrend('12h', 300, 144, '2026-11-04T20:01:00Z', '2026-11-05T08:01:00Z'))
      wrapper = mountView(); await flushPromises()
      dragOverview(500, 1000); await flushPromises()
      expect(lastQuery().to).toBe('2026-11-05T07:31:00.000Z')
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not request a selection that is shorter than 10 minutes after clamping', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    try {
      vi.setSystemTime(new Date('2026-11-05T19:56:00Z'))
      api.getTrend.mockResolvedValue(rangeTrend('12h', 300, 144, '2026-11-04T20:01:00Z', '2026-11-05T08:01:00Z'))
      wrapper = mountView(); await flushPromises()
      dragOverview(800, 1000); await flushPromises()
      expect(api.getTrend).toHaveBeenCalledTimes(1)
      expect(wrapper.find('[data-testid="ftt-selection-label"]').exists()).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it('re-clamps the selection start to the clock when switching accounts after waiting', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    try {
      vi.setSystemTime(new Date('2026-11-05T08:00:00Z'))
      api.getTrend.mockResolvedValue(rangeTrend('12h', 300, 144, '2026-11-04T20:01:00Z', '2026-11-05T08:01:00Z'))
      wrapper = mountView(); await flushPromises()
      dragOverview(0, 500); await flushPromises()
      // 此刻「现在 − 12 小时 + 1 分钟」正好是 20:01，没被夹。
      expect(lastQuery().from).toBe('2026-11-04T20:01:00.000Z')
      vi.setSystemTime(new Date('2026-11-05T08:10:00Z'))
      await selectAccount('98'); await flushPromises()
      expect(lastQuery()).toMatchObject({ account_id: 98, from: '2026-11-04T20:11:00.000Z' })
      expect(new Date(lastQuery().to).getTime()).toBeLessThanOrEqual(new Date('2026-11-05T08:10:00Z').getTime())
      expect(wrapper.get('[data-testid="ftt-selection-label"]').exists()).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not resend the selection when the clock moves it under 10 minutes', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    try {
      vi.setSystemTime(new Date('2026-11-05T08:00:00Z'))
      api.getTrend.mockResolvedValue(rangeTrend('12h', 300, 144, '2026-11-04T20:01:00Z', '2026-11-05T08:01:00Z'))
      wrapper = mountView(); await flushPromises()
      dragOverview(0, 15); await flushPromises()
      expect(lastQuery().from).toBeTruthy()
      const windowCalls = () => api.getTrend.mock.calls.filter(call => call[0].from).length
      const before = windowCalls()
      vi.setSystemTime(new Date('2026-11-05T08:29:00Z'))
      await selectAccount('98'); await flushPromises()
      // 换号时总览条会发一次不带 from/to 的整范围请求，这个不算。
      expect(windowCalls()).toBe(before)
    } finally {
      vi.useRealTimers()
    }
  })

  it('draws a custom 12h response whose first bucket starts before the returned from', async () => {
    const start = new Date('2026-11-05T06:55:00Z').getTime()
    const buckets = Array.from({ length: 145 }, (_, index) => bucket(new Date(start + index * 300000).toISOString(), {
      samples: 10, avg_ms: 5000, p50_ms: 4000, p80_ms: 6000, over10_pct: 0,
    }))
    api.getTrend.mockResolvedValue(trend({ range: 'custom', step_seconds: 300, from: '2026-11-05T07:00:00Z', to: '2026-11-05T19:00:00Z', buckets }))
    wrapper = mountView(); await flushPromises()
    expect(charts('Line')[0].props('data').labels).toHaveLength(145)
    expect(wrapper.get('[data-testid="ftt-step"]').text()).toBe('5 分钟一格')
    expect(wrapper.find('[data-testid="ftt-overview"]').exists()).toBe(true)
  })

  it('draws three lines plus the 10 s line, in-flight vs limit, and the stacked split, in seconds', async () => {
    wrapper = mountView(); await flushPromises()
    const [trendChart, concChart] = charts('Line')
    const trendSets = trendChart.props('data').datasets
    expect(trendSets.map((set: { label: string }) => set.label)).toEqual(['平均', 'p50', 'p80', '10 秒判慢线'])
    expect(trendSets[1].data).toEqual([16, 17.6, 9.4, null])
    expect(trendSets[3].data).toEqual([10, 10, 10, 10])
    expect(trendChart.props('data').labels).toEqual(['15:54', '15:56', '15:58', '16:00'])
    // 2 小时范围约每 10 分钟标一个时间
    expect([0, 1, 2, 3].map(i => trendChart.props('options').scales.x.ticks.callback(0, i))).toEqual(['', '', '', '16:00'])
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

  it('shows the breakdown with status by p50 and a sparkline, sorted by name by default', async () => {
    wrapper = mountView(); await flushPromises()
    const rows = wrapper.findAll('[data-testid="ftt-row"]')
    // 默认按名称升序：#97、#98、#99（接口返回的顺序是 98、97、99）。
    expect(rows.map(row => row.get('.name > span').text())).toEqual(['#97 fingermaikon7', '#98 jeremiassmith649', '#99 new'])
    expect(rows.map(row => row.get('[data-testid="ftt-row-status"]').text())).toEqual(['正常', '偏慢', '暂无'])
    expect(rows[1].text()).toContain('16.8s')
    expect(rows[1].text()).toContain('78%')
    expect(rows[1].find('[data-testid="ftt-spark"]').exists()).toBe(true)
    expect(rows[0].find('[data-testid="ftt-spark"]').exists()).toBe(false)
    expect(rows[2].text()).toContain(zhFirstTokenTrend.dims.noRowData)
    expect(wrapper.get('[data-testid="ftt-sort-label"]').element.closest('th')!.getAttribute('aria-sort')).toBe('ascending')
  })

  it('sorts the breakdown by any column, toggling direction and keeping empty rows last', async () => {
    wrapper = mountView(); await flushPromises()
    const names = () => wrapper.findAll('[data-testid="ftt-row"]').map(row => row.get('.name > span').text().split(' ')[0])

    // 数值列第一次点是降序（大的在前），没数据的 #99 始终排最后。
    await wrapper.get('[data-testid="ftt-sort-p50_ms"]').trigger('click')
    expect(names()).toEqual(['#98', '#97', '#99'])
    expect(wrapper.get('[data-testid="ftt-sort-p50_ms"]').element.closest('th')!.getAttribute('aria-sort')).toBe('descending')
    await wrapper.get('[data-testid="ftt-sort-p50_ms"]').trigger('click')
    expect(names()).toEqual(['#97', '#98', '#99'])
    expect(wrapper.get('[data-testid="ftt-sort-p50_ms"]').element.closest('th')!.getAttribute('aria-sort')).toBe('ascending')

    // 样本数：0 也是有效数值，降序排最后。
    await wrapper.get('[data-testid="ftt-sort-samples"]').trigger('click')
    expect(names()).toEqual(['#98', '#97', '#99'])

    // 状态按严重程度：很慢 > 偏慢 > 正常，暂无排最后。
    await wrapper.get('[data-testid="ftt-sort-status"]').trigger('click')
    expect(names()).toEqual(['#98', '#97', '#99'])

    // 名称再点一次回到升序，再点降序。
    await wrapper.get('[data-testid="ftt-sort-label"]').trigger('click')
    expect(names()).toEqual(['#97', '#98', '#99'])
    await wrapper.get('[data-testid="ftt-sort-label"]').trigger('click')
    expect(names()).toEqual(['#99', '#98', '#97'])

    // 趋势列不能排序。
    expect(wrapper.find('[data-testid="ftt-sort-spark"]').exists()).toBe(false)
  })

  it('pages the change log 20 rows at a time and resets to page 1 when the filter changes', async () => {
    const many = Array.from({ length: 45 }, (_, i) => ({
      at: new Date(Date.UTC(2026, 10, 5, 7, 0, 0) + i * 60000).toISOString(),
      account_id: 98, account_name: 'acc', type: 'concurrency' as const, from: '10', to: String(i), detail: `并发 → ${i}`,
      actor: '', source: 'audit', p50_before_ms: null, p50_after_ms: null,
    }))
    api.getTrend.mockResolvedValue(trend({ events: many }))
    wrapper = mountView(); await flushPromises()

    expect(wrapper.findAll('[data-testid="ftt-event-row"]')).toHaveLength(20)
    expect(wrapper.get('[data-testid="ftt-events-page"]').text()).toBe('1 / 3')
    expect(wrapper.get('[data-testid="ftt-events-pager"]').text()).toContain('第 1–20 条，共 45 条')
    // 最新的在第一页第一行。
    expect(wrapper.findAll('[data-testid="ftt-event-row"]')[0].text()).toContain('并发 → 44')
    expect(wrapper.get('[data-testid="ftt-events-prev"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-testid="ftt-events-next"]').trigger('click')
    await wrapper.get('[data-testid="ftt-events-next"]').trigger('click')
    expect(wrapper.findAll('[data-testid="ftt-event-row"]')).toHaveLength(5)
    expect(wrapper.get('[data-testid="ftt-events-pager"]').text()).toContain('第 41–45 条，共 45 条')
    expect(wrapper.get('[data-testid="ftt-events-next"]').attributes('disabled')).toBeDefined()

    // 换范围回到第一页。
    await wrapper.get('[data-testid="ftt-range-1h"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="ftt-events-page"]').text()).toBe('1 / 3')
  })

  it('hides the pager when the change log fits on one page', async () => {
    wrapper = mountView(); await flushPromises()
    expect(wrapper.findAll('[data-testid="ftt-event-row"]')).toHaveLength(3)
    expect(wrapper.find('[data-testid="ftt-events-pager"]').exists()).toBe(false)
  })

  it('has no measure switch: always usage-log first token', async () => {
    wrapper = mountView(); await flushPromises()
    expect(wrapper.find('[data-testid="ftt-metric-usage"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="ftt-metric-switch"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('代理优选首字')
    expect(wrapper.text()).not.toContain('不含上传')
    await wrapper.get('[data-testid="ftt-range-30m"]').trigger('click'); await flushPromises()
    expect(lastQuery()).toEqual({ group_id: 16, range: '30m', dimension: 'account' })
  })

  it('reloads when the breakdown dimension or range changes', async () => {
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="ftt-dim-proxy"]').trigger('click'); await flushPromises()
    expect(lastQuery()).toMatchObject({ dimension: 'proxy' })
    expect(wrapper.get('[data-testid="ftt-dim-proxy"]').attributes('aria-checked')).toBe('true')
    await wrapper.get('[data-testid="ftt-range-30m"]').trigger('click'); await flushPromises()
    expect(lastQuery()).toMatchObject({ range: '30m', dimension: 'proxy' })
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

  it('offers 30 min through 12 hours and labels long ranges about hourly / half-hourly', async () => {
    wrapper = mountView(); await flushPromises()
    expect(wrapper.findAll('[data-testid^="ftt-range-"]').map(button => button.text())).toEqual(['30 分钟', '1 小时', '2 小时', '6 小时', '12 小时'])
    expect(wrapper.get('[data-testid="ftt-range-2h"]').attributes('aria-checked')).toBe('true')

    api.getTrend.mockResolvedValue(rangeTrend('12h', 300, 144, '2026-11-05T07:00:00Z', '2026-11-05T19:00:00Z'))
    await wrapper.get('[data-testid="ftt-range-12h"]').trigger('click'); await flushPromises()
    expect(lastQuery()).toMatchObject({ range: '12h' })
    expect(wrapper.get('[data-testid="ftt-step"]').text()).toBe('5 分钟一格')
    const ticks12 = charts('Line')[0].props('options').scales.x.ticks
    const labels12 = Array.from({ length: 144 }, (_, index) => ticks12.callback(0, index)).filter(Boolean)
    expect(labels12).toHaveLength(12)
    expect(labels12[0]).toBe('15:00')

    api.getTrend.mockResolvedValue(rangeTrend('6h', 300, 72, '2026-11-05T07:00:00Z', '2026-11-05T13:00:00Z'))
    await wrapper.get('[data-testid="ftt-range-6h"]').trigger('click'); await flushPromises()
    const ticks6 = charts('Line')[0].props('options').scales.x.ticks
    const labels6 = Array.from({ length: 72 }, (_, index) => ticks6.callback(0, index)).filter(Boolean)
    expect(labels6).toHaveLength(12)
    expect(labels6[0]).toBe('15:00')

    api.getTrend.mockResolvedValue(trend({ range: '1h', step_seconds: 60, from: '2026-11-05T06:54:00Z', to: '2026-11-05T07:54:00Z', buckets: ['07:54', '07:55', '07:56', '07:57'].map(hm => bucket(`2026-11-05T${hm}:00Z`)) }))
    await wrapper.get('[data-testid="ftt-range-1h"]').trigger('click'); await flushPromises()
    expect(lastQuery()).toMatchObject({ range: '1h' })
    expect(wrapper.get('[data-testid="ftt-step"]').text()).toBe('1 分钟一格')
    expect([0, 1, 2, 3].map(i => charts('Line')[0].props('options').scales.x.ticks.callback(0, i))).toEqual(['', '15:55', '', ''])
  })

  it('selects a >=10 min window on the overview, reloads by from/to, then cancels back to the range', async () => {
    wrapper = mountView(); await flushPromises()
    const before = wrapper.findAll('[data-testid="ftt-overview-line"]').map(line => line.attributes('points'))
    expect(before.length).toBeGreaterThan(0)

    dragOverview(100, 400)
    await flushPromises()
    expect(api.getTrend).toHaveBeenCalledTimes(2)
    const query = lastQuery()
    expect(query).toEqual({ group_id: 16, dimension: 'account', from: expect.any(String), to: expect.any(String) })
    expect(query).not.toHaveProperty('range')
    expect(query).not.toHaveProperty('metric')
    expect(wrapper.get('[data-testid="ftt-selection-label"]').text()).toMatch(/^已框选 \d{2}:\d{2}–\d{2}:\d{2}$/)
    expect(wrapper.find('[data-testid="ftt-overview-selection"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="ftt-range-2h"]').attributes('aria-checked')).toBe('false')
    // 总览条仍是框选前那次请求的整段 buckets
    expect(wrapper.findAll('[data-testid="ftt-overview-line"]').map(line => line.attributes('points'))).toEqual(before)

    await wrapper.get('[data-testid="ftt-clear-selection"]').trigger('click'); await flushPromises()
    expect(api.getTrend).toHaveBeenCalledTimes(3)
    expect(lastQuery()).toEqual({ group_id: 16, range: '2h', dimension: 'account' })
    expect(wrapper.find('[data-testid="ftt-selection-label"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="ftt-overview-selection"]').exists()).toBe(false)
  })

  it('accepts a selection of exactly 10 minutes', async () => {
    wrapper = mountView(); await flushPromises()
    // 2 小时范围约 119 分钟，1000px 里 85px 正好是 10 分钟出头。
    dragOverview(0, 85)
    await flushPromises()
    expect(api.getTrend).toHaveBeenCalledTimes(2)
    expect(lastQuery()).not.toHaveProperty('range')
  })

  it('does not request anything when the dragged window is under 10 minutes', async () => {
    wrapper = mountView(); await flushPromises()
    dragOverview(100, 120)
    await flushPromises()
    expect(api.getTrend).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="ftt-selection-label"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="ftt-overview-selection"]').exists()).toBe(false)
  })

  it('keeps the overview and its highlight when the selected window has no samples', async () => {
    wrapper = mountView(); await flushPromises()
    const empty = trend()
    api.getTrend.mockResolvedValue({
      ...empty,
      buckets: empty.buckets.map(item => ({ ...item, samples: 0, avg_ms: null, p50_ms: null, p80_ms: null, over10_pct: null })),
      rows: [],
      events: [],
    })
    dragOverview(100, 400); await flushPromises()
    expect(wrapper.find('[data-testid="ftt-overview"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="ftt-overview-selection"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="ftt-selection-label"]').text()).toContain('已框选')
  })

  it('clears an active selection when a range button is clicked', async () => {
    wrapper = mountView(); await flushPromises()
    dragOverview(100, 400); await flushPromises()
    await wrapper.get('[data-testid="ftt-range-6h"]').trigger('click'); await flushPromises()
    expect(lastQuery()).toEqual({ group_id: 16, range: '6h', dimension: 'account' })
    expect(wrapper.find('[data-testid="ftt-selection-label"]').exists()).toBe(false)
  })

  it('caps a 96 s spike at 20 s with a ▲ marker and shows the full range when capping is off', async () => {
    api.getTrend.mockResolvedValue(spikeTrend(true))
    wrapper = mountView(); await flushPromises()
    const chart = () => charts('Line')[0]
    expect(chart().props('options').scales.y.max).toBe(20)
    expect(wrapper.get('[data-testid="ftt-trend-spike"]').text()).toContain('▲')
    expect(wrapper.get('[data-testid="ftt-cap-spikes"]').attributes('aria-checked')).toBe('true')

    await wrapper.get('[data-testid="ftt-cap-spikes"]').trigger('click'); await flushPromises()
    expect(chart().props('options').scales.y.max).toBeGreaterThanOrEqual(96)
    expect(wrapper.find('[data-testid="ftt-trend-spike"]').exists()).toBe(false)
    expect(localStorage.getItem('ftt.capSpikes')).toBe('false')
  })

  it('leaves flat data unmarked at the 20 s floor', async () => {
    api.getTrend.mockResolvedValue(spikeTrend(false))
    wrapper = mountView(); await flushPromises()
    expect(charts('Line')[0].props('options').scales.y.max).toBe(20)
    expect(wrapper.find('[data-testid="ftt-trend-spike"]').exists()).toBe(false)
  })

  it('remembers the cap-spikes switch in localStorage and falls back to on when storage fails', async () => {
    localStorage.setItem('ftt.capSpikes', 'false')
    api.getTrend.mockResolvedValue(spikeTrend(true))
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="ftt-cap-spikes"]').attributes('aria-checked')).toBe('false')
    expect(charts('Line')[0].props('options').scales.y.max).toBeGreaterThanOrEqual(96)

    wrapper.unmount()
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    api.getTrend.mockResolvedValue(spikeTrend(true))
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="ftt-cap-spikes"]').attributes('aria-checked')).toBe('true')
    getItem.mockRestore()
  })

  it('notes when the concurrency limit is only the current value', async () => {
    api.getTrend.mockResolvedValue(trend({ limit_is_current: true }))
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="ftt-limit-current"]').text()).toBe(zhFirstTokenTrend.concurrency.limitIsCurrent)
  })

  it('pauses auto refresh while a window is selected and resumes after cancelling', async () => {
    // 只假定时器，不假 setTimeout / setImmediate，否则 flushPromises 会卡住。
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    try {
      wrapper = mountView(); await flushPromises()
      expect(api.getTrend).toHaveBeenCalledTimes(1)

      dragOverview(100, 400); await flushPromises()
      expect(api.getTrend).toHaveBeenCalledTimes(2)
      expect(wrapper.find('[data-testid="ftt-selection-label"]').exists()).toBe(true)

      // 框选期间过了两个刷新周期也不该发请求。
      vi.advanceTimersByTime(130000); await flushPromises()
      expect(api.getTrend).toHaveBeenCalledTimes(2)

      await wrapper.get('[data-testid="ftt-clear-selection"]').trigger('click'); await flushPromises()
      expect(api.getTrend).toHaveBeenCalledTimes(3)

      // 取消框选后自动刷新恢复。
      vi.advanceTimersByTime(130000); await flushPromises()
      expect(api.getTrend.mock.calls.length).toBeGreaterThan(3)
    } finally {
      vi.useRealTimers()
    }
  })
})
