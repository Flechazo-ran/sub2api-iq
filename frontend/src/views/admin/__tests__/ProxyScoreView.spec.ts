import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProxyScoreView from '../ProxyScoreView.vue'
import zhProxyScore from '@/i18n/locales/zh/proxyScore'
import type { ProxyScoreResult, ProxyScoreRow } from '@/api/admin/proxyScore'

const { api } = vi.hoisted(() => ({
  api: { getProxyScore: vi.fn() },
}))
vi.mock('@/api/admin/proxyScore', () => ({ getProxyScore: api.getProxyScore, proxyScoreAPI: api, default: api }))

function row(id: number, overrides: Partial<ProxyScoreRow> = {}): ProxyScoreRow {
  return {
    id, name: `代理-${id}`, status: 'active', expires_at: '2027-03-01T00:00:00Z',
    samples: 1000, p50_ms: 5000, mean_ms: 6000, p80_ms: 9000,
    judged_slow: 0, grade: 'S', advice: 'renew', ...overrides,
  }
}

function result(overrides: Partial<ProxyScoreResult> = {}): ProxyScoreResult {
  return { range: '7d', rows: [row(1), row(2, { p50_ms: 11000, grade: 'B', advice: 'observe' })], ...overrides }
}

function mountView() {
  const i18n = createI18n({
    legacy: false, locale: 'zh', missingWarn: false, fallbackWarn: false,
    messages: { zh: { proxyScore: zhProxyScore, nav: { routing: '线路优化' } } },
  })
  return mount(ProxyScoreView, {
    global: {
      plugins: [i18n],
      stubs: { AppLayout: { template: '<div><slot /></div>' }, RoutingNav: true, Icon: true },
    },
  })
}

let wrapper: ReturnType<typeof mountView>
beforeEach(() => { api.getProxyScore.mockResolvedValue(result()) })
afterEach(() => { wrapper?.unmount(); vi.clearAllMocks() })

describe('ProxyScoreView', () => {
  it('calls getProxyScore with 7d on mount and renders rows', async () => {
    wrapper = mountView(); await flushPromises()
    expect(api.getProxyScore).toHaveBeenCalledWith('7d')
    expect(wrapper.findAll('[data-testid^="psc-row-"]')).toHaveLength(2)
  })

  it('switches to 30d when that tab is clicked', async () => {
    wrapper = mountView(); await flushPromises()
    api.getProxyScore.mockResolvedValue(result({ range: '30d' }))
    await wrapper.get('[data-testid="psc-range-30d"]').trigger('click')
    await flushPromises()
    expect(api.getProxyScore).toHaveBeenLastCalledWith('30d')
    expect(api.getProxyScore).toHaveBeenCalledTimes(2)
  })

  it('shows the grade, formatted latency and advice for each row', async () => {
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="psc-grade-1"]').text()).toBe('S')
    expect(wrapper.get('[data-testid="psc-grade-2"]').text()).toBe('B')
    const first = wrapper.get('[data-testid="psc-row-1"]').text()
    expect(first).toContain('5.0s')
    expect(first).toContain('续费')
    expect(wrapper.get('[data-testid="psc-row-2"]').text()).toContain('观察')
  })

  it('shows demote mark when judged_slow >= 3', async () => {
    api.getProxyScore.mockResolvedValue(result({ rows: [row(1, { judged_slow: 3, grade: 'A' }), row(2, { judged_slow: 2 })] }))
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="psc-row-1"]').text()).toContain('↓')
    expect(wrapper.get('[data-testid="psc-row-2"]').text()).not.toContain('↓')
  })

  it('shows no-data grade for proxies without samples', async () => {
    api.getProxyScore.mockResolvedValue(result({ rows: [row(1, { samples: 0, p50_ms: null, mean_ms: null, p80_ms: null, grade: '', advice: 'drop' })] }))
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="psc-grade-1"]').text()).toBe('—')
    expect(wrapper.get('[data-testid="psc-row-1"]').text()).toContain('考虑放弃')
  })

  it('shows error state with the API message on failure', async () => {
    api.getProxyScore.mockRejectedValue({ message: '网络错误' })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="psc-error"]').text()).toContain('网络错误')
  })

  it('shows empty state when rows is empty', async () => {
    api.getProxyScore.mockResolvedValue(result({ rows: [] }))
    wrapper = mountView(); await flushPromises()
    expect(wrapper.find('[data-testid="psc-empty"]').exists()).toBe(true)
  })
})
