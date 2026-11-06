import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProxyAutoSwitchView from '../ProxyAutoSwitchView.vue'
import zhProxyAutoSwitch from '@/i18n/locales/zh/proxyAutoSwitch'
import type { ProxyAutoSwitchConfig, ProxyAutoSwitchEvent, ProxyAutoSwitchStatus } from '@/api/admin/proxyAutoSwitch'

const { api, getAllGroups } = vi.hoisted(() => ({
  api: { getConfig: vi.fn(), updateConfig: vi.fn(), listEvents: vi.fn(), revertEvent: vi.fn(), listRecent: vi.fn() },
  getAllGroups: vi.fn(),
}))
vi.mock('@/api/admin/proxyAutoSwitch', () => ({ proxyAutoSwitchAPI: api, default: api }))
vi.mock('@/api/admin/groups', () => ({ getAll: getAllGroups, default: { getAll: getAllGroups } }))

// not_faster 排在最后，前面的行号（rowFor）不变。reverted / revert_skipped 只是旧记录。
const statuses: ProxyAutoSwitchStatus[] = ['observing', 'kept', 'kept_insufficient', 'reverted', 'revert_skipped', 'no_candidate', 'failed', 'manual_revert', 'not_faster']
// no_candidate 的文案带「至少快 N%」，按已保存配置（默认 30）填进去。
const statusText: Record<string, string> = Object.fromEntries(Object.entries(zhProxyAutoSwitch.status).map(([k, v]) => [k, String(v).replace('{pct}', '30')]))

function config(overrides: Partial<ProxyAutoSwitchConfig> = {}): ProxyAutoSwitchConfig {
  return { enabled: false, window_minutes: 10, min_samples: 10, metric: 'response_headers', threshold_ms: 2500, consecutive_checks: 2, min_improvement_pct: 30, account_cooldown_minutes: 10, max_switches_per_hour: 5, observe_minutes: 10, min_days_to_expiry: 7, region_policy: 'prefer_same', group_ids: [16], ...overrides }
}
function event(id: number, status: ProxyAutoSwitchStatus, overrides: Partial<ProxyAutoSwitchEvent> = {}): ProxyAutoSwitchEvent {
  const switched = status !== 'no_candidate' && status !== 'failed'
  return {
    id, created_at: '2026-11-03T12:14:00Z', updated_at: '2026-11-03T12:30:00Z', account_id: 60 + id, account_name: `GPT-速刷号-${id}`, status, metric: 'response_headers', reason: `原因-${status}`,
    from_proxy_id: 91, from_proxy_label: '东京-91', from_region: 'JP',
    to_proxy_id: switched ? 96 : null, to_proxy_label: switched ? '东京-96' : null, to_region: switched ? 'JP' : null,
    before_p50_ms: 3513, before_samples: 42, current_probe_ms: 1800, candidate_probe_ms: switched ? 1120 : null,
    after_p50_ms: status === 'kept' ? 1300 : null, after_samples: status === 'kept' ? 37 : null,
    candidates: [
      { proxy_id: 97, label: '东京-97', region: 'JP', probe_ms: null, error: 'connect timeout' },
      { proxy_id: 96, label: '东京-96', region: 'JP', probe_ms: 1120, error: '' },
      { proxy_id: 98, label: '大阪-98', region: 'JP', probe_ms: 1500, error: '' },
    ],
    ...overrides,
  }
}

function mountView() {
  const i18n = createI18n({ legacy: false, locale: 'zh', missingWarn: false, fallbackWarn: false, messages: { zh: { proxyAutoSwitch: zhProxyAutoSwitch, nav: { routing: '线路优化' } } } })
  return mount(ProxyAutoSwitchView, { global: { plugins: [i18n], stubs: { AppLayout: { template: '<main><slot /></main>' }, RoutingNav: true, Teleport: true } } })
}
let wrapper: ReturnType<typeof mountView>
const rowFor = (id: number) => wrapper.get(`[data-event-id="${id}"] [data-testid="pas-event-row"]`)
const buttonByText = (text: string) => wrapper.findAll('button').find(button => button.text() === text)

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  getAllGroups.mockReset().mockResolvedValue([
    { id: 16, name: 'GPT-速刷-AUTO', platform: 'openai' },
    { id: 20, name: '1028GPT-PRO', platform: 'openai' },
    { id: 31, name: 'Claude 分组', platform: 'anthropic' },
  ])
  api.getConfig.mockResolvedValue(config())
  api.listEvents.mockResolvedValue({ items: [], next_cursor: 0 })
})
afterEach(() => wrapper?.unmount())

describe('ProxyAutoSwitchView settings', () => {
  it('loads the saved config with two modes, the metric choice and only OpenAI groups', async () => {
    wrapper = mountView(); await flushPromises()
    expect(api.getConfig).toHaveBeenCalledTimes(1)
    expect(getAllGroups).toHaveBeenCalledWith('openai')
    expect(wrapper.get('[data-testid="pas-state"]').text()).toBe('已关闭')
    expect(wrapper.find('[role="radiogroup"]').findAll('button').map(button => button.text())).toEqual(['关闭', '自动换代理'])
    expect(wrapper.get('[data-testid="pas-mode-off"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.text()).not.toContain('只记录建议')
    const metric = wrapper.get('[data-testid="pas-metric"]')
    expect(metric.findAll('button').map(button => button.text())).toEqual(['使用记录首字', '上游首字', '响应头时间'])
    expect(wrapper.get('[data-testid="pas-metric-response_headers"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[data-testid="pas-metric-first_token"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-testid="pas-metric-hint"]').text()).toBe(zhProxyAutoSwitch.metricHint.response_headers)
    expect(wrapper.get('[data-testid="pas-hint-threshold"]').text()).toBe('看窗口里响应头时间的中位数')
    expect((wrapper.get('[data-testid="pas-threshold_s"]').element as HTMLInputElement).value).toBe('2.5')
    expect((wrapper.get('[data-testid="pas-window_minutes"]').element as HTMLInputElement).value).toBe('10')
    const chips = wrapper.findAll('[data-testid="pas-groups"] label')
    expect(chips.map(chip => chip.text())).toEqual(['GPT-速刷-AUTO', '1028GPT-PRO'])
    expect(chips[0].classes()).toContain('on')
    expect((chips[1].get('input').element as HTMLInputElement).checked).toBe(false)
    const region = wrapper.get('[data-testid="pas-region"]')
    expect((region.element as HTMLSelectElement).value).toBe('prefer_same')
    expect(region.findAll('option').map(option => option.text())).toEqual(['优先同地区，没有再不限', '只同地区', '不限地区'])
    expect(wrapper.get('[data-testid="pas-save"]').attributes('disabled')).toBeDefined()
  })

  it('edits the per-account interval in minutes and says observing only records the effect', async () => {
    api.updateConfig.mockImplementation(async (body: ProxyAutoSwitchConfig) => body)
    wrapper = mountView(); await flushPromises()
    const cooldown = wrapper.get('[data-testid="pas-account_cooldown_minutes"]')
    expect((cooldown.element as HTMLInputElement).value).toBe('10')
    expect(cooldown.element.closest('.field')?.textContent).toContain('同一个号多久最多换一次')
    expect(cooldown.element.parentElement?.textContent).toContain('分钟')
    expect(cooldown.element.parentElement?.textContent).not.toContain('小时')
    expect(wrapper.get('[data-testid="pas-hint-cooldown"]').text()).toBe('没找到更快的代理或换失败，也隔这么久再试')
    expect((wrapper.get('[data-testid="pas-observe_minutes"]').element as HTMLInputElement).value).toBe('10')
    const observeHint = wrapper.get('[data-testid="pas-hint-observe"]').text()
    expect(observeHint).toContain('只记录效果，不换回')
    expect(observeHint).toContain('观察期间不再换这个号')
    expect(wrapper.text()).not.toContain('自动换回')
    expect(wrapper.text()).toContain('只记录有没有变快，不换回')

    await cooldown.setValue('-1')
    expect(cooldown.attributes('aria-invalid')).toBe('true')
    expect(wrapper.text()).toContain('要填整数，范围 0–1440')
    expect(wrapper.get('[data-testid="pas-save"]').attributes('disabled')).toBeDefined()
    await cooldown.setValue('1441')
    expect(cooldown.attributes('aria-invalid')).toBe('true')
    await cooldown.setValue('15')
    expect(cooldown.attributes('aria-invalid')).toBeUndefined()
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(api.updateConfig).toHaveBeenCalledTimes(1)
    const payload = api.updateConfig.mock.calls[0][0] as Record<string, unknown>
    expect(payload).toEqual(config({ account_cooldown_minutes: 15 }))
    expect(payload).not.toHaveProperty('account_cooldown_hours')
    expect((wrapper.get('[data-testid="pas-account_cooldown_minutes"]').element as HTMLInputElement).value).toBe('15')
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(false)
  })

  it('keeps configured group ids that are not in the OpenAI list so saving does not drop them', async () => {
    api.getConfig.mockResolvedValue(config({ group_ids: [16, 44] }))
    wrapper = mountView(); await flushPromises()
    const chip = wrapper.get('[data-group-id="44"]')
    expect(chip.text()).toBe('#44')
    expect(chip.classes()).toContain('on')
  })

  it('saves the full config and shows the saved state', async () => {
    api.updateConfig.mockImplementation(async (body: ProxyAutoSwitchConfig) => body)
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="pas-mode-auto"]').trigger('click')
    await wrapper.get('[data-testid="pas-threshold_s"]').setValue('3')
    await wrapper.get('[data-testid="pas-observe_minutes"]').setValue('20')
    await wrapper.get('[data-group-id="20"] input').setValue(true)
    await wrapper.get('[data-testid="pas-region"]').setValue('same_only')
    expect(wrapper.get('[data-testid="pas-dirty"]').text()).toBe('有未保存的修改')
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(api.updateConfig).toHaveBeenCalledWith(config({ enabled: true, threshold_ms: 3000, observe_minutes: 20, group_ids: [16, 20], region_policy: 'same_only' }))
    expect(wrapper.get('[data-testid="pas-state"]').text()).toBe('运行中 · 自动换代理')
    expect(wrapper.get('[data-testid="pas-notice"]').text()).toContain('设置已保存')
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="pas-save"]').attributes('disabled')).toBeDefined()
  })

  it('switches the metric to first token with a threshold in seconds and saves both', async () => {
    api.updateConfig.mockImplementation(async (body: ProxyAutoSwitchConfig) => body)
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="pas-metric-first_token"]').trigger('click')
    expect(wrapper.get('[data-testid="pas-metric-first_token"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[data-testid="pas-metric-response_headers"]').attributes('aria-checked')).toBe('false')
    const hint = wrapper.get('[data-testid="pas-metric-hint"]').text()
    expect(hint).toContain('含模型思考时间')
    expect(hint).toContain('不含上传时间')
    expect(wrapper.get('[data-testid="pas-hint-threshold"]').text()).toBe('看窗口里上游首字时间的中位数')
    expect(wrapper.get('[data-testid="pas-hint-observe"]').text()).toContain('上游首字时间中位数降到换前的 80% 及以下记「变快了」')

    // Up to 120 s is allowed now; above that is blocked.
    await wrapper.get('[data-testid="pas-threshold_s"]').setValue('121')
    expect(wrapper.text()).toContain('要在 0.2–120 之间')
    expect(wrapper.get('[data-testid="pas-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="pas-threshold_s"]').setValue('10')
    expect(wrapper.get('[data-testid="pas-dirty"]').text()).toBe('有未保存的修改')
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(api.updateConfig).toHaveBeenCalledWith(config({ metric: 'first_token', threshold_ms: 10000 }))
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(false)

    // Discarding goes back to the saved metric.
    await wrapper.get('[data-testid="pas-metric-response_headers"]').trigger('click')
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(true)
    await wrapper.get('[data-testid="pas-discard"]').trigger('click')
    expect(wrapper.get('[data-testid="pas-metric-first_token"]').attributes('aria-checked')).toBe('true')
  })

  it('edits the 11-06 rules: usage-log first token, any region and 0 = no limit', async () => {
    api.updateConfig.mockImplementation(async (body: ProxyAutoSwitchConfig) => body)
    wrapper = mountView(); await flushPromises()
    for (const [key, label] of [
      ['max_switches_per_hour', '每小时上限（0 = 不限）'],
      ['account_cooldown_minutes', '同一个号多久最多换一次（0 = 不限）'],
      ['observe_minutes', '换完观察（0 = 不拦）'],
      ['min_improvement_pct', '新代理至少快（0 = 不比，挑最快的）'],
      ['min_days_to_expiry', '离过期至少（0 = 没过期就行）'],
    ]) {
      expect(wrapper.get(`[data-testid="pas-${key}"]`).element.closest('.field')?.textContent).toContain(label)
    }
    expect(wrapper.text()).not.toContain('6 小时')
    expect(wrapper.text()).not.toContain('快 20%')

    await wrapper.get('[data-testid="pas-mode-auto"]').trigger('click')
    await wrapper.get('[data-testid="pas-metric-usage_first_token"]').trigger('click')
    expect(wrapper.get('[data-testid="pas-metric-hint"]').text()).toBe('和使用记录页、首字趋势的首字一致：从请求进网关算，含上传请求体的时间和模型思考时间。')
    expect(wrapper.get('[data-testid="pas-hint-threshold"]').text()).toBe('看窗口里使用记录首字的中位数')
    await wrapper.get('[data-testid="pas-window_minutes"]').setValue('5')
    await wrapper.get('[data-testid="pas-min_samples"]').setValue('1')
    await wrapper.get('[data-testid="pas-threshold_s"]').setValue('10')
    for (const key of ['min_improvement_pct', 'account_cooldown_minutes', 'max_switches_per_hour', 'observe_minutes', 'min_days_to_expiry']) {
      const input = wrapper.get(`[data-testid="pas-${key}"]`)
      await input.setValue('0')
      expect(input.attributes('aria-invalid'), key).toBeUndefined()
    }
    expect(wrapper.get('[data-testid="pas-hint-improvement"]').text()).toBe('每个空闲代理测速 3 次取中位数，直接换测速最快的，不要求比当前代理快')
    expect(wrapper.get('[data-testid="pas-hint-cooldown"]').text()).toBe('不冷却：刚换过或上次没换成，下一轮还慢照样换')
    expect(wrapper.get('[data-testid="pas-hint-observe"]').text()).toContain('观察不拦号，换完过一个统计窗口记一次效果')
    await wrapper.get('[data-testid="pas-region"]').setValue('any')
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(api.updateConfig).toHaveBeenCalledWith(config({
      enabled: true, metric: 'usage_first_token', window_minutes: 5, min_samples: 1, threshold_ms: 10000, consecutive_checks: 2,
      min_improvement_pct: 0, account_cooldown_minutes: 0, max_switches_per_hour: 0, observe_minutes: 0, min_days_to_expiry: 0, region_policy: 'any',
    }))
    expect((wrapper.get('[data-testid="pas-metric-usage_first_token"]').attributes('aria-checked'))).toBe('true')
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(false)

    // Negative values are still out of range.
    await wrapper.get('[data-testid="pas-max_switches_per_hour"]').setValue('-1')
    expect(wrapper.text()).toContain('要填整数，范围 0–50')
  })

  it('says no usable idle proxy when the saved improvement is 0', async () => {
    api.getConfig.mockResolvedValue(config({ min_improvement_pct: 0 }))
    api.listEvents.mockResolvedValue({ items: [event(2, 'no_candidate')], next_cursor: 0 })
    wrapper = mountView(); await flushPromises()
    expect(rowFor(2).get('[data-testid="pas-status"]').text()).toBe('没找到能用的空闲代理')
  })

  it('treats a config without metric (older backend) as response headers', async () => {
    const legacy: Partial<ProxyAutoSwitchConfig> = config()
    delete legacy.metric
    api.getConfig.mockResolvedValue(legacy)
    api.updateConfig.mockImplementation(async (body: ProxyAutoSwitchConfig) => body)
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="pas-metric-response_headers"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(false)
    await wrapper.get('[data-testid="pas-min_samples"]').setValue('20')
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(api.updateConfig).toHaveBeenCalledWith(config({ min_samples: 20 }))
  })

  it('shows the backend message when saving fails and keeps the edits', async () => {
    api.updateConfig.mockRejectedValue({ status: 400, code: 400, message: 'group_ids 只能是 OpenAI 平台分组' })
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="pas-min_samples"]').setValue('20')
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[data-testid="pas-save-error"]').text()).toBe('group_ids 只能是 OpenAI 平台分组')
    expect(wrapper.get('[data-testid="pas-state"]').text()).toBe('已关闭')
    expect((wrapper.get('[data-testid="pas-min_samples"]').element as HTMLInputElement).value).toBe('20')
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(true)
  })

  it('blocks saving out-of-range values before calling the backend', async () => {
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="pas-window_minutes"]').setValue('3')
    expect(wrapper.get('[data-testid="pas-window_minutes"]').attributes('aria-invalid')).toBe('true')
    expect(wrapper.text()).toContain('要填整数，范围 5–60')
    expect(wrapper.get('[data-testid="pas-invalid"]').text()).toContain('1')
    expect(wrapper.get('[data-testid="pas-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(api.updateConfig).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="pas-discard"]').trigger('click')
    expect((wrapper.get('[data-testid="pas-window_minutes"]').element as HTMLInputElement).value).toBe('10')
    expect(wrapper.find('[data-testid="pas-invalid"]').exists()).toBe(false)
  })

  it('shows a config load error and retries', async () => {
    api.getConfig.mockRejectedValueOnce({ status: 500, message: '读取配置失败' })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="pas-config-error"]').text()).toContain('读取配置失败')
    expect(wrapper.find('[data-testid="pas-settings"]').exists()).toBe(false)
    await wrapper.get('[data-testid="pas-config-error"] button').trigger('click')
    await flushPromises()
    expect(api.getConfig).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="pas-config-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pas-settings"]').exists()).toBe(true)
  })
})

describe('ProxyAutoSwitchView switch log', () => {
  it('renders every status with its label and only offers 换回 while the switch can still be undone', async () => {
    api.listEvents.mockResolvedValue({ items: statuses.map((status, index) => event(index + 1, status)), next_cursor: 0 })
    wrapper = mountView(); await flushPromises()
    expect(api.listEvents).toHaveBeenCalledWith({ before_id: 0, limit: 50 })
    statuses.forEach((status, index) => {
      const row = rowFor(index + 1)
      expect(row.get('[data-testid="pas-status"]').text()).toBe(statusText[status])
      expect(row.find('[data-testid="pas-revert"]').exists()).toBe(['observing', 'kept', 'kept_insufficient', 'not_faster'].includes(status))
    })
    expect(wrapper.findAll('[data-testid="pas-revert"]')).toHaveLength(4)
    const kept = rowFor(2)
    expect(kept.text()).toContain('GPT-速刷号-2')
    expect(kept.get('[data-testid="pas-proxy"]').text()).toBe('#91 东京-91JP→#96 东京-96JP')
    expect(kept.get('[data-testid="pas-latency"]').text()).toContain('3.51 s → 1.12 s')
    expect(kept.get('[data-testid="pas-latency"]').text()).toContain('当前代理测速 1.80 s')
    expect(kept.text()).toContain('换后 1.30 s（37 个样本）')
    const noCandidate = rowFor(6)
    expect(noCandidate.get('[data-testid="pas-proxy"]').text()).toBe('#91 东京-91JP')
    expect(noCandidate.get('[data-testid="pas-latency"]').text()).not.toContain('→')
    expect(wrapper.text()).toContain('没有更多了')
  })

  it('shows not_faster as a neutral hint that kept the new proxy and can still be switched back by hand', async () => {
    api.listEvents.mockResolvedValue({ items: [
      event(1, 'not_faster', { after_p50_ms: 3000, after_samples: 37, reason: '观察 10 分钟：新代理响应头 p50 3000ms（37 个样本），没有比换前 3513ms 低 20% 以上；不换回，还慢就接着换' }),
      event(2, 'failed'),
    ], next_cursor: 0 })
    api.revertEvent.mockResolvedValue(event(1, 'manual_revert'))
    wrapper = mountView(); await flushPromises()
    const badge = rowFor(1).get('[data-testid="pas-status"]')
    expect(badge.text()).toBe('没变快，不换回；还慢就接着换')
    expect(badge.attributes('data-status')).toBe('not_faster')
    expect(badge.classes()).toContain('tone-warning')
    expect(badge.classes()).not.toContain('tone-danger')
    expect(rowFor(2).get('[data-testid="pas-status"]').classes()).toContain('tone-danger')
    expect(rowFor(1).get('[data-testid="pas-proxy"]').text()).toBe('#91 东京-91JP→#96 东京-96JP')
    expect(rowFor(1).text()).toContain('换后 3.00 s（37 个样本）')
    expect(rowFor(1).text()).toContain('不换回，还慢就接着换')
    await rowFor(1).get('[data-testid="pas-revert"]').trigger('click')
    await wrapper.get('[data-testid="pas-revert-confirm"]').trigger('click')
    await flushPromises()
    expect(api.revertEvent).toHaveBeenCalledWith(1)
    expect(rowFor(1).get('[data-testid="pas-status"]').text()).toBe('已手动换回')
  })

  it('labels each record with the metric it was judged by', async () => {
    api.listEvents.mockResolvedValue({ items: [
      event(1, 'kept', { metric: 'first_token', before_p50_ms: 12000, before_samples: 40 }),
      event(2, 'no_candidate'),
      event(3, 'kept', { metric: undefined as unknown as ProxyAutoSwitchEvent['metric'] }),
      event(4, 'observing', { metric: 'usage_first_token', before_p50_ms: 15020, before_samples: 1 }),
    ], next_cursor: 0 })
    wrapper = mountView(); await flushPromises()
    expect(rowFor(1).get('[data-testid="pas-metric-tag"]').text()).toBe('上游首字')
    expect(rowFor(1).get('[data-testid="pas-latency"]').text()).toContain('上游首字12.00 s → 1.12 s')
    expect(rowFor(4).get('[data-testid="pas-metric-tag"]').text()).toBe('记录首字')
    expect(rowFor(2).get('[data-testid="pas-metric-tag"]').text()).toBe('响应头')
    expect(rowFor(3).get('[data-testid="pas-metric-tag"]').text()).toBe('响应头')
    await rowFor(1).trigger('click')
    expect(wrapper.get('[data-event-id="1"] [data-testid="pas-detail-metric"]').text()).toBe('按上游首字时间判慢：换前 p50 12.00 s（40 个样本）')
    await rowFor(4).trigger('click')
    expect(wrapper.get('[data-event-id="4"] [data-testid="pas-detail-metric"]').text()).toBe('按使用记录首字判慢：换前 p50 15.02 s（1 个样本）')
    await rowFor(2).trigger('click')
    expect(wrapper.get('[data-event-id="2"] [data-testid="pas-detail-metric"]').text()).toBe('按响应头时间判慢：换前 p50 3.51 s（42 个样本）')
  })

  it('opens a row to show candidate probes, fastest first, with the chosen proxy marked', async () => {
    api.listEvents.mockResolvedValue({ items: [event(1, 'kept'), event(2, 'no_candidate', { candidates: [] })], next_cursor: 0 })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.find('[data-testid="pas-detail"]').exists()).toBe(false)
    await rowFor(1).trigger('click')
    const detail = wrapper.get('[data-event-id="1"] [data-testid="pas-detail"]')
    const candidates = detail.findAll('[data-testid="pas-candidate"]')
    expect(candidates.map(row => row.findAll('td')[0].text())).toEqual(['#96 东京-96已选', '#98 大阪-98', '#97 东京-97'])
    expect(candidates[0].classes()).toContain('chosen')
    expect(candidates[2].text()).toContain('connect timeout')
    expect(rowFor(1).get('[data-testid="pas-toggle"]').attributes('aria-expanded')).toBe('true')
    await rowFor(2).get('[data-testid="pas-toggle"]').trigger('click')
    expect(wrapper.get('[data-event-id="2"] [data-testid="pas-detail"]').text()).toContain('这次没有可测的候选代理。')
    await rowFor(1).trigger('click')
    expect(wrapper.find('[data-event-id="1"] [data-testid="pas-detail"]').exists()).toBe(false)
  })

  it('asks for confirmation in the page before switching back, then refreshes that row', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm')
    api.listEvents.mockResolvedValue({ items: [event(1, 'kept'), event(2, 'observing')], next_cursor: 0 })
    api.revertEvent.mockResolvedValue(event(1, 'manual_revert', { reason: '已手动换回 #91' }))
    wrapper = mountView(); await flushPromises()
    await rowFor(1).get('[data-testid="pas-revert"]').trigger('click')
    expect(wrapper.find('[data-event-id="1"] [data-testid="pas-detail"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="pas-revert-message"]').text()).toBe('把 GPT-速刷号-1 的代理从 #96 东京-96 换回 #91 东京-91？')
    await wrapper.get('[data-testid="pas-revert-cancel"]').trigger('click')
    expect(wrapper.find('[data-testid="pas-revert-message"]').exists()).toBe(false)
    expect(api.revertEvent).not.toHaveBeenCalled()

    await rowFor(1).get('[data-testid="pas-revert"]').trigger('click')
    await wrapper.get('[data-testid="pas-revert-confirm"]').trigger('click')
    await flushPromises()
    expect(api.revertEvent).toHaveBeenCalledWith(1)
    expect(confirmSpy).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="pas-revert-message"]').exists()).toBe(false)
    expect(rowFor(1).get('[data-testid="pas-status"]').text()).toBe('已手动换回')
    expect(rowFor(1).find('[data-testid="pas-revert"]').exists()).toBe(false)
    expect(rowFor(1).text()).toContain('已手动换回 #91')
    expect(rowFor(2).find('[data-testid="pas-revert"]').exists()).toBe(true)
    expect(api.listEvents).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-testid="pas-notice"]').text()).toContain('已把 GPT-速刷号-1 换回原代理')
    confirmSpy.mockRestore()
  })

  it('keeps the dialog open with the backend reason when switching back is refused', async () => {
    api.listEvents.mockResolvedValue({ items: [event(3, 'kept_insufficient')], next_cursor: 0 })
    api.revertEvent.mockRejectedValue({ status: 400, code: 400, message: '旧代理已被别的号占用，不能换回' })
    wrapper = mountView(); await flushPromises()
    await rowFor(3).get('[data-testid="pas-revert"]').trigger('click')
    await wrapper.get('[data-testid="pas-revert-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="pas-revert-error"]').text()).toBe('旧代理已被别的号占用，不能换回')
    expect(wrapper.find('[data-testid="pas-revert-message"]').exists()).toBe(true)
    expect(rowFor(3).get('[data-testid="pas-status"]').text()).toBe(statusText.kept_insufficient)
  })

  it('loads more with the cursor until the backend reports no more', async () => {
    api.listEvents
      .mockResolvedValueOnce({ items: [event(30, 'kept'), event(29, 'no_candidate')], next_cursor: 29 })
      .mockResolvedValueOnce({ items: [event(29, 'no_candidate'), event(28, 'failed')], next_cursor: 0 })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('已加载 2 条')
    await wrapper.get('[data-testid="pas-load-more"]').trigger('click')
    await flushPromises()
    expect(api.listEvents).toHaveBeenLastCalledWith({ before_id: 29, limit: 50 })
    expect(wrapper.findAll('[data-testid="pas-event-row"]')).toHaveLength(3)
    expect(wrapper.find('[data-testid="pas-load-more"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('已加载 3 条')
    expect(wrapper.text()).toContain('没有更多了')
  })

  it('shows an empty state and a retryable error', async () => {
    api.listEvents.mockRejectedValueOnce({ status: 500, message: '记录读取失败' })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="pas-events-error"]').text()).toContain('记录读取失败')
    await wrapper.get('[data-testid="pas-events-error"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="pas-events-error"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="pas-events-empty"]').text()).toContain('还没有换代理记录')
  })
})
