import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProxyAutoSwitchView from '../ProxyAutoSwitchView.vue'
import zhProxyAutoSwitch from '@/i18n/locales/zh/proxyAutoSwitch'
import type {
  ProxyAutoSwitchAccount,
  ProxyAutoSwitchConfig,
  ProxyAutoSwitchEvent,
  ProxyAutoSwitchProxy,
  ProxyAutoSwitchStatus,
} from '@/api/admin/proxyAutoSwitch'

const { api, getAllGroups } = vi.hoisted(() => ({
  api: { getConfig: vi.fn(), updateConfig: vi.fn(), listEvents: vi.fn(), listAccounts: vi.fn(), listProxies: vi.fn(), listRecent: vi.fn() },
  getAllGroups: vi.fn(),
}))
vi.mock('@/api/admin/proxyAutoSwitch', () => ({ proxyAutoSwitchAPI: api, default: api }))
vi.mock('@/api/admin/groups', () => ({ getAll: getAllGroups, default: { getAll: getAllGroups } }))

function config(overrides: Partial<ProxyAutoSwitchConfig> = {}): ProxyAutoSwitchConfig {
  return {
    enabled: false,
    group_ids: [16],
    window_minutes: 5,
    min_samples: 20,
    p60_threshold_ms: 10000,
    mean_threshold_ms: 15000,
    max_accounts_per_proxy: 2,
    strategy: 'sequential',
    ...overrides,
  }
}

const proxyNames: Record<number, string> = {
  68: '新加坡-130.12.42.170',
  19: 'JP东京-103.101.222.116',
  42: 'JP东京-103.101.222.179',
  99: '越南河内-116.118.40.87',
  25: '德国法兰克福-145.79.120.174',
}
function proxy(id: number, overrides: Partial<ProxyAutoSwitchProxy> = {}): ProxyAutoSwitchProxy {
  return {
    id,
    name: proxyNames[id] ?? `代理-${id}`,
    status: 'active',
    region: id === 25 ? 'DE' : id === 99 ? 'VN' : id === 68 ? 'SG' : 'JP',
    expires_at: id === 25 ? '2026-10-01T00:00:00Z' : '2026-12-20T00:00:00Z',
    expired: id === 25,
    account_count: id === 68 ? 1 : 0,
    ...overrides,
  }
}
const defaultProxies = [proxy(68), proxy(19), proxy(42), proxy(99), proxy(25)]

function event(id: number, status: ProxyAutoSwitchStatus, overrides: Partial<ProxyAutoSwitchEvent> = {}): ProxyAutoSwitchEvent {
  const switched = status === 'done' || status === 'measuring'
  return {
    id,
    created_at: '2026-11-06T07:42:47Z',
    switched_at: '2026-11-06T07:42:47Z',
    status,
    account_id: 108,
    account_name: 'GPT-速刷号-108',
    group_id: 16,
    strategy: 'random',
    reason: '',
    from_proxy_id: 76,
    from_proxy_label: '新加坡-76',
    from_region: 'SG',
    to_proxy_id: switched ? 99 : null,
    to_proxy_label: switched ? '越南河内-99' : null,
    to_region: switched ? 'VN' : null,
    window_minutes: 5,
    before: { samples: 268, p60_ms: 13100, mean_ms: 18400, duration_mean_ms: 26100 },
    after: status === 'measuring'
      ? { samples: 88, p60_ms: 9000, mean_ms: 11200, duration_mean_ms: 18000 }
      : { samples: 512, p60_ms: 6000, mean_ms: 8100, duration_mean_ms: 12300 },
    from_probe_ms: 812,
    to_probe_ms: switched ? 354 : null,
    after_finished_at: status === 'done' ? '2026-11-06T07:52:47Z' : null,
    ...overrides,
  }
}

function account(id: number, state: ProxyAutoSwitchAccount['state'], overrides: Partial<ProxyAutoSwitchAccount> = {}): ProxyAutoSwitchAccount {
  return {
    account_id: id,
    account_name: `GPT-速刷号-${id}`,
    group_ids: [16],
    proxy_id: state === 'no_proxy' ? null : 68,
    proxy_label: state === 'no_proxy' ? null : '新加坡-68',
    region: state === 'no_proxy' ? null : 'SG',
    samples: state === 'unavailable' ? 0 : 231,
    p60_ms: state === 'unavailable' ? null : 6100,
    mean_ms: state === 'unavailable' ? null : 7900,
    duration_mean_ms: state === 'unavailable' ? null : 13200,
    state,
    last_switch_at: null,
    note: '',
    ...overrides,
  }
}

function mountView() {
  const i18n = createI18n({ legacy: false, locale: 'zh', missingWarn: false, fallbackWarn: false, messages: { zh: { proxyAutoSwitch: zhProxyAutoSwitch, nav: { routing: '线路优化' } } } })
  return mount(ProxyAutoSwitchView, { global: { plugins: [i18n], stubs: { AppLayout: { template: '<main><slot /></main>' }, RoutingNav: true, Teleport: true } } })
}
let wrapper: ReturnType<typeof mountView>
const rowFor = (id: number) => wrapper.get(`[data-event-id="${id}"]`)
const poolOrder = () => wrapper.findAll('[data-testid="pas-pool-order"] [data-pool-proxy-id]').map(item => Number(item.attributes('data-pool-proxy-id')))

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  getAllGroups.mockReset().mockResolvedValue([
    { id: 16, name: 'GPT-速刷-AUTO', platform: 'openai' },
    { id: 20, name: '1028GPT-PRO', platform: 'openai' },
    { id: 31, name: 'Claude 分组', platform: 'anthropic' },
  ])
  api.getConfig.mockResolvedValue(config())
  api.listEvents.mockResolvedValue({ items: [], next_cursor: 0 })
  api.listAccounts.mockResolvedValue({ items: [], window_minutes: 5 })
  api.listProxies.mockResolvedValue(defaultProxies)
})
afterEach(() => wrapper?.unmount())

describe('ProxyAutoSwitchView settings', () => {
  it('shows the contract defaults and saves exactly the contract fields, converting seconds to milliseconds', async () => {
    api.updateConfig.mockImplementation(async (body: ProxyAutoSwitchConfig) => body)
    wrapper = mountView(); await flushPromises()

    expect(api.getConfig).toHaveBeenCalledTimes(1)
    expect(getAllGroups).toHaveBeenCalledWith('openai')
    const chips = wrapper.findAll('[data-testid="pas-groups"] label')
    expect(chips.map(chip => chip.text())).toEqual(['GPT-速刷-AUTO', '1028GPT-PRO'])
    expect(chips[0].classes()).toContain('on')
    expect((chips[1].get('input').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.get('[data-testid="pas-save"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(false)
    expect((wrapper.get('[data-testid="pas-window_minutes"]').element as HTMLInputElement).value).toBe('5')
    expect((wrapper.get('[data-testid="pas-min_samples"]').element as HTMLInputElement).value).toBe('20')
    expect((wrapper.get('[data-testid="pas-p60_threshold_s"]').element as HTMLInputElement).value).toBe('10')
    expect((wrapper.get('[data-testid="pas-mean_threshold_s"]').element as HTMLInputElement).value).toBe('15')
    expect((wrapper.get('[data-testid="pas-max_accounts_per_proxy"]').element as HTMLInputElement).value).toBe('2')
    expect(wrapper.get('[data-testid="pas-rule"]').text()).toBe('规则：统计过去 5 分钟，窗口里不少于 20 个请求，首字 p60 大于 10 秒且首字均值大于 15 秒，就自动更换代理。首字和耗时都取自使用记录的每个请求。')

    await wrapper.get('[data-testid="pas-enabled"]').trigger('click')
    await wrapper.get('[data-testid="pas-window_minutes"]').setValue('7')
    await wrapper.get('[data-testid="pas-min_samples"]').setValue('30')
    await wrapper.get('[data-testid="pas-p60_threshold_s"]').setValue('12.5')
    await wrapper.get('[data-testid="pas-mean_threshold_s"]').setValue('16.5')
    await wrapper.get('[data-testid="pas-max_accounts_per_proxy"]').setValue('3')
    await wrapper.get('[data-testid="pas-strategy-random"]').trigger('click')
    expect(wrapper.get('[data-testid="pas-rule"]').text()).toContain('统计过去 7 分钟')
    expect(wrapper.get('[data-testid="pas-rule"]').text()).toContain('不少于 30 个请求')
    expect(wrapper.get('[data-testid="pas-rule"]').text()).toContain('首字 p60 大于 12.5 秒')
    expect(wrapper.get('[data-testid="pas-rule"]').text()).toContain('首字均值大于 16.5 秒')
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()

    const payload = api.updateConfig.mock.calls[0][0] as Record<string, unknown>
    expect(payload).toEqual({
      enabled: true,
      group_ids: [16],
      window_minutes: 7,
      min_samples: 30,
      p60_threshold_ms: 12500,
      mean_threshold_ms: 16500,
      max_accounts_per_proxy: 3,
      strategy: 'random',
    })
    expect(Object.keys(payload).sort()).toEqual(['enabled', 'group_ids', 'max_accounts_per_proxy', 'mean_threshold_ms', 'min_samples', 'p60_threshold_ms', 'strategy', 'window_minutes'])
    expect(payload).not.toHaveProperty('candidate_proxy_ids')
    expect(payload).not.toHaveProperty('metric')
    expect(payload).not.toHaveProperty('threshold_ms')
    expect(payload).not.toHaveProperty('consecutive_checks')
    expect(payload).not.toHaveProperty('account_cooldown_minutes')
    expect(wrapper.get('[data-testid="pas-state"]').text()).toBe('运行中 · 自动换代理')
    expect(wrapper.get('[data-testid="pas-notice"]').text()).toContain('设置已保存')
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="pas-save"]').attributes('disabled')).toBeDefined()
  })

  it('keeps configured group ids that are not in the OpenAI list so saving does not drop them', async () => {
    api.getConfig.mockResolvedValue(config({ group_ids: [16, 44] }))
    api.updateConfig.mockImplementation(async (body: ProxyAutoSwitchConfig) => body)
    wrapper = mountView(); await flushPromises()
    const chip = wrapper.get('[data-group-id="44"]')
    expect(chip.text()).toBe('#44')
    expect(chip.classes()).toContain('on')

    await wrapper.get('[data-testid="pas-enabled"]').trigger('click')
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect((api.updateConfig.mock.calls[0][0] as ProxyAutoSwitchConfig).group_ids).toEqual([16, 44])
  })

  it('reloads the account monitor and proxy list after a successful save', async () => {
    api.updateConfig.mockImplementation(async (body: ProxyAutoSwitchConfig) => body)
    api.listAccounts
      .mockResolvedValueOnce({ items: [], window_minutes: 5 })
      .mockResolvedValueOnce({ items: [], window_minutes: 7 })
    wrapper = mountView(); await flushPromises()
    expect(api.listAccounts).toHaveBeenCalledTimes(1)
    expect(api.listProxies).toHaveBeenCalledTimes(1)

    await wrapper.get('[data-testid="pas-enabled"]').trigger('click')
    await wrapper.get('[data-testid="pas-window_minutes"]').setValue('10')
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(api.listAccounts).toHaveBeenCalledTimes(2)
    expect(api.listProxies).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-testid="pas-account-window"]').text()).toBe('过去 7 分钟 · 每个号单独看')
  })

  it('shows proxies sorted by latency rank (server order) and marks full ones', async () => {
    // 后端已按延迟排好序返回，前端照序展示，账号数 >= 配置上限的标「已满」。
    api.listProxies.mockResolvedValue([proxy(19), proxy(68, { account_count: 2 }), proxy(42)])
    wrapper = mountView(); await flushPromises()

    const rows = wrapper.findAll('[data-testid="pas-proxies"] [data-proxy-id]')
    expect(rows.map(r => Number(r.attributes('data-proxy-id')))).toEqual([19, 68, 42])
    expect(wrapper.find('[data-proxy-id="68"] [data-testid="pas-proxy-full"]').exists()).toBe(true)
    expect(wrapper.find('[data-proxy-id="19"] [data-testid="pas-proxy-full"]').exists()).toBe(false)
  })

  it('switches strategy and shows the right aria-checked', async () => {
    wrapper = mountView(); await flushPromises()
    // 默认 sequential
    expect(wrapper.get('[data-testid="pas-strategy-sequential"]').attributes('aria-checked')).toBe('true')
    await wrapper.get('[data-testid="pas-strategy-region"]').trigger('click')
    expect(wrapper.get('[data-testid="pas-strategy-region"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[data-testid="pas-strategy-sequential"]').attributes('aria-checked')).toBe('false')
  })

  it('blocks out-of-range values before calling the backend', async () => {
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="pas-window_minutes"]').setValue('61')
    expect(wrapper.get('[data-testid="pas-window_minutes"]').attributes('aria-invalid')).toBe('true')
    expect(wrapper.get('[data-testid="pas-invalid"]').text()).toContain('1')
    expect(wrapper.get('[data-testid="pas-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(api.updateConfig).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="pas-discard"]').trigger('click')
    expect((wrapper.get('[data-testid="pas-window_minutes"]').element as HTMLInputElement).value).toBe('5')
    expect(wrapper.find('[data-testid="pas-invalid"]').exists()).toBe(false)
  })

  it('validates max_accounts_per_proxy range', async () => {
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="pas-max_accounts_per_proxy"]').setValue('101')
    expect(wrapper.get('[data-testid="pas-max_accounts_per_proxy"]').attributes('aria-invalid')).toBe('true')
    expect(wrapper.get('[data-testid="pas-invalid"]').text()).toContain('1')
    expect(wrapper.get('[data-testid="pas-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(api.updateConfig).not.toHaveBeenCalled()
  })

  it('shows a config load error and retries', async () => {
    api.getConfig.mockRejectedValueOnce({ status: 500, message: '读取配置失败' })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="pas-config-error"]').text()).toContain('读取配置失败')
    expect(wrapper.find('[data-testid="pas-settings"]').exists()).toBe(false)
    await wrapper.get('[data-testid="pas-config-error"] button').trigger('click')
    await flushPromises()
    expect(api.getConfig).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="pas-settings"]').exists()).toBe(true)
  })

  it('shows the backend message when saving fails and keeps the edits', async () => {
    api.updateConfig.mockRejectedValue({ status: 400, code: 400, message: '配置无效' })
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="pas-min_samples"]').setValue('30')
    await wrapper.get('[data-testid="pas-settings"]').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[data-testid="pas-save-error"]').text()).toBe('配置无效')
    expect((wrapper.get('[data-testid="pas-min_samples"]').element as HTMLInputElement).value).toBe('30')
    expect(wrapper.find('[data-testid="pas-dirty"]').exists()).toBe(true)
    expect(api.listAccounts).toHaveBeenCalledTimes(1)
  })
})

describe('ProxyAutoSwitchView account monitor', () => {
  it('renders all five monitor states with the configured request threshold', async () => {
    api.listAccounts.mockResolvedValue({
      window_minutes: 5,
      items: [
        account(107, 'ok'),
        account(109, 'insufficient'),
        account(108, 'slow'),
        account(106, 'unavailable'),
        account(105, 'no_proxy'),
      ],
    })
    wrapper = mountView(); await flushPromises()

    expect(api.listAccounts).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-testid="pas-account-window"]').text()).toBe('过去 5 分钟 · 每个号单独看')
    expect(wrapper.get('[data-account-id="107"] [data-testid="pas-account-state"]').text()).toBe('正常')
    expect(wrapper.get('[data-account-id="109"] [data-testid="pas-account-state"]').text()).toBe('不到 20 个请求')
    expect(wrapper.get('[data-account-id="108"] [data-testid="pas-account-state"]').text()).toBe('两项都超，换代理')
    expect(wrapper.get('[data-account-id="106"] [data-testid="pas-account-state"]').text()).toBe('账号不可用')
    expect(wrapper.get('[data-account-id="105"] [data-testid="pas-account-state"]').text()).toBe('没有代理')
    expect(wrapper.get('[data-account-id="107"] [data-testid="pas-account-proxy"]').text()).toContain('#68 新加坡-68')
    expect(wrapper.get('[data-account-id="107"] [data-testid="pas-account-proxy"]').text()).toContain('SG')
    expect(wrapper.get('[data-account-id="107"] [data-testid="pas-account-samples"]').text()).toBe('231')
    expect(wrapper.get('[data-account-id="107"] [data-testid="pas-account-p60"]').text()).toBe('6.1s')
    expect(wrapper.get('[data-account-id="107"] [data-testid="pas-account-mean"]').text()).toBe('7.9s')
    expect(wrapper.get('[data-account-id="107"] [data-testid="pas-account-duration"]').text()).toBe('13.2s')
  })
})

describe('ProxyAutoSwitchView switch log', () => {
  it('renders before, after, change and probes for a done record', async () => {
    api.listEvents.mockResolvedValue({ items: [event(1, 'done')], next_cursor: 0 })
    wrapper = mountView(); await flushPromises()

    const row = rowFor(1)
    expect(row.get('[data-testid="pas-before-samples"]').text()).toBe('268')
    expect(row.get('[data-testid="pas-before-p60"]').text()).toBe('13.1s')
    expect(row.get('[data-testid="pas-before-mean"]').text()).toBe('18.4s')
    expect(row.get('[data-testid="pas-after-samples"]').text()).toBe('512')
    expect(row.get('[data-testid="pas-after-p60"]').text()).toBe('6.0s')
    expect(row.get('[data-testid="pas-after-mean"]').text()).toBe('8.1s')
    expect(row.get('[data-testid="pas-change-p60"]').text()).toBe('↓ 7.1s')
    expect(row.get('[data-testid="pas-change-p60"]').classes()).toContain('change-good')
    expect(row.get('[data-testid="pas-change-mean"]').text()).toBe('↓ 10.3s')
    expect(row.get('[data-testid="pas-change-mean"]').classes()).toContain('change-good')
    expect(row.get('[data-testid="pas-probe-old"]').text()).toBe('812ms')
    expect(row.get('[data-testid="pas-probe-new"]').text()).toBe('354ms')
    expect(row.get('[data-testid="pas-proxy"]').text()).toContain('#76 新加坡-76')
    expect(row.get('[data-testid="pas-proxy"]').text()).toContain('SG')
    expect(row.get('[data-testid="pas-proxy"]').text()).toContain('→')
    expect(row.get('[data-testid="pas-proxy"]').text()).toContain('#99 越南河内-99')
    expect(row.get('[data-testid="pas-proxy"]').text()).toContain('VN')
    expect(row.get('[data-testid="pas-strategy"]').text()).toBe('随机')
    expect(row.get('[data-testid="pas-status"]').text()).toBe('已完成')
  })

  it('shows the measuring note instead of the two change cells while the after window is still open', async () => {
    api.listEvents.mockResolvedValue({ items: [event(2, 'measuring')], next_cursor: 0 })
    wrapper = mountView(); await flushPromises()

    const row = rowFor(2)
    expect(row.get('[data-testid="pas-measuring"]').text()).toBe('换后不满 10 分钟，统计中')
    expect(row.get('[data-testid="pas-measuring"]').attributes('colspan')).toBe('2')
    expect(row.find('[data-testid="pas-change-p60"]').exists()).toBe(false)
    expect(row.find('[data-testid="pas-change-mean"]').exists()).toBe(false)
    expect(row.get('[data-testid="pas-after-samples"]').text()).toBe('88')
    expect(row.get('[data-testid="pas-after-p60"]').text()).toBe('9.0s')
  })

  it('renders unreachable probes as 测不通 and shows a slower change in red', async () => {
    api.listEvents.mockResolvedValue({
      items: [
        event(3, 'done', { from_probe_ms: null, to_probe_ms: null, after: { samples: 10, p60_ms: 15000, mean_ms: 20000, duration_mean_ms: 30000 } }),
        event(6, 'failed', { from_probe_ms: 500, to_probe_ms: null }),
      ],
      next_cursor: 0,
    })
    wrapper = mountView(); await flushPromises()

    const row = rowFor(3)
    expect(row.get('[data-testid="pas-probe-old"]').text()).toBe('测不通')
    expect(row.get('[data-testid="pas-probe-new"]').text()).toBe('测不通')
    expect(rowFor(6).get('[data-testid="pas-probe-new"]').text()).toBe('测不通')
    expect(row.get('[data-testid="pas-change-p60"]').text()).toBe('↑ 1.9s')
    expect(row.get('[data-testid="pas-change-p60"]').classes()).toContain('change-bad')
    expect(row.get('[data-testid="pas-change-mean"]').text()).toBe('↑ 1.6s')
    expect(row.get('[data-testid="pas-change-mean"]').classes()).toContain('change-bad')
  })

  it('shows old records with empty new fields as dashes and renders no_candidate reasons', async () => {
    const legacy = event(4, 'kept', {
      strategy: null,
      before: null,
      after: null,
      from_probe_ms: null,
      to_probe_ms: null,
      after_finished_at: null,
    })
    const noCandidate = event(5, 'no_candidate', { from_probe_ms: null, to_probe_ms: null, reason: '池子里的代理都被别的号占了' })
    api.listEvents.mockResolvedValue({ items: [legacy, noCandidate], next_cursor: 0 })
    wrapper = mountView(); await flushPromises()

    const oldRow = rowFor(4)
    expect(oldRow.get('[data-testid="pas-before-samples"]').text()).toBe('—')
    expect(oldRow.get('[data-testid="pas-before-p60"]').text()).toBe('—')
    expect(oldRow.get('[data-testid="pas-after-mean"]').text()).toBe('—')
    expect(oldRow.get('[data-testid="pas-change-p60"]').text()).toBe('—')
    expect(oldRow.get('[data-testid="pas-probe-old"]').text()).toBe('—')
    expect(oldRow.get('[data-testid="pas-probe-new"]').text()).toBe('—')
    expect(oldRow.get('[data-testid="pas-strategy"]').text()).toBe('—')

    const noCandidateRow = rowFor(5)
    expect(noCandidateRow.get('[data-testid="pas-status"]').text()).toBe('没有候选代理')
    expect(noCandidateRow.text()).toContain('池子里的代理都被别的号占了')
    expect(noCandidateRow.get('[data-testid="pas-probe-old"]').text()).toBe('—')
    expect(noCandidateRow.get('[data-testid="pas-probe-new"]').text()).toBe('—')
  })

  it('has no switch-back button', async () => {
    api.listEvents.mockResolvedValue({ items: [event(1, 'done')], next_cursor: 0 })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.find('[data-testid="pas-revert"]').exists()).toBe(false)
    expect(wrapper.findAll('button').some(button => button.text() === '换回')).toBe(false)
  })

  it('loads more with the cursor until the backend reports no more', async () => {
    api.listEvents
      .mockResolvedValueOnce({ items: [event(30, 'done'), event(29, 'no_candidate')], next_cursor: 29 })
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

  it('shows a retryable events error and an empty state', async () => {
    api.listEvents.mockRejectedValueOnce({ status: 500, message: '记录读取失败' })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="pas-events-error"]').text()).toContain('记录读取失败')
    await wrapper.get('[data-testid="pas-events-error"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="pas-events-error"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="pas-events-empty"]').text()).toContain('还没有换代理记录')
  })
})
