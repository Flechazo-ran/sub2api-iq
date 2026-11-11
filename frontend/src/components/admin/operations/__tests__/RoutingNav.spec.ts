import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it } from 'vitest'
import RoutingNav from '../RoutingNav.vue'

const routingPaths = ['/admin/first-token-trend', '/admin/proxy-autoswitch', '/admin/proxy-score']

async function mountAt(path: string) {
  const router = createRouter({ history: createMemoryHistory(), routes: routingPaths.map(route => ({ path: route, component: { template: '<div />' } })) })
  await router.push(path)
  await router.isReady()
  const i18n = createI18n({ legacy: false, locale: 'zh', missingWarn: false, fallbackWarn: false, messages: { zh: {} } })
  return mount(RoutingNav, { global: { plugins: [router, i18n] } })
}

describe('RoutingNav', () => {
  it('lists 首字趋势 then 代理优选 and marks the current page', async () => {
    const wrapper = await mountAt('/admin/proxy-autoswitch')
    expect(wrapper.findAll('a').map(link => link.attributes('href'))).toEqual(routingPaths)
    expect(wrapper.find('[aria-current="page"]').text()).toBe('proxyAutoSwitch.title')
  })
})
