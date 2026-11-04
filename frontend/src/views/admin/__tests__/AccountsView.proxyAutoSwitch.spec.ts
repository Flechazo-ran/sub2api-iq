import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, listRecent } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listRecent: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getManagementCapabilities: vi.fn().mockResolvedValue({ web_search_enabled: false, account_quota_notify_enabled: false }),
      list: listAccounts,
      listWithEtag: vi.fn().mockResolvedValue({ notModified: true, etag: null, data: null }),
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: vi.fn().mockResolvedValue([]) },
    scheduledTests: { listResults: vi.fn().mockResolvedValue([]) }
  }
}))

vi.mock('@/api/admin/accountQuality', () => ({ listQualityPlans: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/admin/proxyAutoSwitch', () => ({ listRecent }))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token' })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, values?: Record<string, unknown>) => (values ? `${key} ${JSON.stringify(values)}` : key)
    })
  }
})

// Render the proxy cell slot for every row so the 自动换过 tag is observable.
const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div data-test="data-table">
      <div v-for="row in data" :key="row.id" :data-test="'proxy-' + row.id">
        <slot name="cell-proxy" :row="row" />
      </div>
    </div>
  `
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        HelpTooltip: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
        AccountTableFilters: { template: '<div></div>' },
        AccountBulkActionsBar: true,
        AccountActionMenu: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        Icon: true
      }
    }
  })
}

const baseAccount = {
  platform: 'openai',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  concurrency: 1,
  priority: 0,
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z'
}

function showProxyColumn() {
  localStorage.setItem('account-hidden-columns', JSON.stringify(['today_stats']))
  localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')
}

describe('admin AccountsView 自动换过 proxy tag', () => {
  beforeEach(() => {
    localStorage.clear()
    listRecent.mockReset()
    listAccounts.mockReset().mockResolvedValue({
      items: [
        { ...baseAccount, id: 65, name: 'switched', proxy: { id: 96, name: '东京-96', country_code: 'JP' } },
        { ...baseAccount, id: 66, name: 'untouched', proxy: { id: 95, name: '东京-95', country_code: 'JP' } }
      ],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1
    })
  })

  it('tags accounts switched in the last 7 days and explains the switch on hover', async () => {
    showProxyColumn()
    listRecent.mockResolvedValue([
      { account_id: 65, event_id: 12, at: '2026-11-03T12:14:00Z', status: 'kept', from_proxy_label: '东京-91', to_proxy_label: '东京-96', reason: '换后 1.30 s' }
    ])
    const wrapper = mountView()
    await flushPromises()

    expect(listRecent).toHaveBeenCalledWith(7)
    const tag = wrapper.get('[data-test="proxy-65"] [data-testid="proxy-autoswitch-tag"]')
    expect(tag.text()).toBe('proxyAutoSwitch.recentTag')
    const title = tag.attributes('title') ?? ''
    expect(title).toContain('proxyAutoSwitch.recentTitle')
    expect(title).toContain('"from":"东京-91"')
    expect(title).toContain('"to":"东京-96"')
    expect(title).toContain('"result":"proxyAutoSwitch.status.kept"')
    expect(title).toMatch(/"time":"[^"]+"/)
    expect(title).toContain('换后 1.30 s')
    expect(wrapper.find('[data-test="proxy-66"] [data-testid="proxy-autoswitch-tag"]').exists()).toBe(false)
  })

  it('tags a not_faster switch too, since the account stayed on the new proxy', async () => {
    showProxyColumn()
    listRecent.mockResolvedValue([
      { account_id: 65, event_id: 13, at: '2026-11-04T12:14:00Z', status: 'not_faster', from_proxy_label: '东京-91', to_proxy_label: '东京-96', reason: '不换回，还慢就接着换' }
    ])
    const wrapper = mountView()
    await flushPromises()

    const tag = wrapper.get('[data-test="proxy-65"] [data-testid="proxy-autoswitch-tag"]')
    expect(tag.attributes('title')).toContain('"result":"proxyAutoSwitch.status.not_faster"')
    expect(tag.attributes('title')).toContain('不换回，还慢就接着换')
  })

  it('stays silent when the endpoint is missing or fails', async () => {
    showProxyColumn()
    listRecent.mockRejectedValue({ status: 404, message: 'Not Found' })
    const consoleError = vi.spyOn(console, 'error')
    const wrapper = mountView()
    await flushPromises()

    expect(listRecent).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="proxy-autoswitch-tag"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="proxy-65"]').text()).toContain('东京-96')
    expect(consoleError.mock.calls.some(args => args.some(arg => String(arg).includes('Not Found')))).toBe(false)
    consoleError.mockRestore()
  })

  it('does not ask for recent switches while the proxy column is hidden', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(listRecent).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="proxy-autoswitch-tag"]').exists()).toBe(false)
  })
})
