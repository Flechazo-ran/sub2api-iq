import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpsDashboardHeader from '../OpsDashboardHeader.vue'
import type { OpsDashboardOverview, OpsSystemMetricsSnapshot } from '@/api/admin/ops'

vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: { getRealtimeTrafficSummary: vi.fn().mockResolvedValue({ enabled: true, summary: null }) } }))
vi.mock('@/stores', () => ({ useAdminSettingsStore: () => ({ opsRealtimeMonitoringEnabled: true, setOpsRealtimeMonitoringEnabledLocal: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key })
}))

function mountHeader(systemMetrics: Partial<OpsSystemMetricsSnapshot> | null) {
  const overview = { tps: { avg: 1 }, system_metrics: systemMetrics } as unknown as OpsDashboardOverview
  return mount(OpsDashboardHeader, {
    props: { overview, platform: '', groupId: null, timeRange: '1h', queryMode: 'auto', loading: false, lastUpdated: null },
    global: { stubs: { Select: true, HelpTooltip: true, BaseDialog: true, Icon: true } }
  })
}

describe('OpsDashboardHeader request body budget', () => {
  it('shows peak / limit under memory and highlights rejections', async () => {
    const wrapper = mountHeader({ memory_used_mb: 2048, memory_total_mb: 8192, request_body_budget_limit_mb: 1500, request_body_budget_peak_mb: 321, request_body_budget_rejected: 7 })
    await flushPromises()
    const line = wrapper.get('[data-testid="ops-request-body-budget"]')
    expect(line.text()).toContain('admin.ops.requestBodyBudget')
    expect(line.text()).toContain('admin.ops.requestBodyBudgetRejected')
    expect(line.text()).toContain('7')
    expect(line.classes()).toContain('text-rose-600')
    wrapper.unmount()
  })

  it('stays quiet without rejections', async () => {
    const wrapper = mountHeader({ request_body_budget_limit_mb: 1500, request_body_budget_peak_mb: 10, request_body_budget_rejected: 0 })
    await flushPromises()
    const line = wrapper.get('[data-testid="ops-request-body-budget"]')
    expect(line.text()).not.toContain('admin.ops.requestBodyBudgetRejected')
    expect(line.classes()).toContain('text-gray-500')
    wrapper.unmount()
  })

  it.each([null, undefined, 0])('is hidden when the budget is disabled or unknown: %s', async (limit) => {
    const wrapper = mountHeader({ memory_used_mb: 1, memory_total_mb: 2, request_body_budget_limit_mb: limit })
    await flushPromises()
    expect(wrapper.find('[data-testid="ops-request-body-budget"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
