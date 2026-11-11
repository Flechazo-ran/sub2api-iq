<template>
  <BaseDialog :show="show" :title="t('proxyAutoSwitch.picker.title')" width="extra-wide" @close="emit('close')">
    <div class="picker" data-testid="pas-picker">
      <div class="picker-filters">
        <input v-model="keyword" type="search" class="input picker-search" :placeholder="t('proxyAutoSwitch.picker.search')" :aria-label="t('proxyAutoSwitch.picker.search')" data-testid="pas-picker-search" />
        <select v-model="region" class="input picker-select" :aria-label="t('proxyAutoSwitch.picker.region')" data-testid="pas-picker-region">
          <option value="">{{ t('proxyAutoSwitch.picker.allRegions') }}</option>
          <option v-for="code in regions" :key="code" :value="code">{{ code }}</option>
        </select>
        <label class="picker-check"><input v-model="hideInPool" type="checkbox" data-testid="pas-picker-hide-in-pool" />{{ t('proxyAutoSwitch.picker.hideInPool') }}</label>
        <label class="picker-check"><input v-model="hideUnusable" type="checkbox" data-testid="pas-picker-hide-unusable" />{{ t('proxyAutoSwitch.picker.hideUnusable') }}</label>
      </div>

      <div class="picker-bar">
        <span class="muted" data-testid="pas-picker-summary">{{ t('proxyAutoSwitch.picker.summary', { shown: filtered.length, total: proxies.length }) }}</span>
        <div class="picker-bar-actions">
          <button type="button" class="link-button" :disabled="!selectableFiltered.length" data-testid="pas-picker-select-all" @click="selectAllFiltered">{{ t('proxyAutoSwitch.picker.selectAll', { count: selectableFiltered.length }) }}</button>
          <button v-if="selected.size" type="button" class="link-button" data-testid="pas-picker-clear" @click="selected = new Set()">{{ t('proxyAutoSwitch.picker.clear') }}</button>
        </div>
      </div>

      <div class="table-scroll picker-table-wrap">
        <table class="picker-table" data-testid="pas-picker-table">
          <thead><tr>
            <th class="check-cell"><input type="checkbox" :checked="pageAllSelected" :indeterminate.prop="pageSomeSelected" :disabled="!selectablePage.length" :aria-label="t('proxyAutoSwitch.picker.selectPage')" data-testid="pas-picker-select-page" @change="togglePage" /></th>
            <th>{{ t('proxyAutoSwitch.colProxy') }}</th><th>{{ t('proxyAutoSwitch.colRegion') }}</th><th>{{ t('proxyAutoSwitch.colExpires') }}</th><th class="num">{{ t('proxyAutoSwitch.picker.accounts') }}</th>
          </tr></thead>
          <tbody>
            <tr v-for="proxy in paged" :key="proxy.id" :data-picker-id="proxy.id" :class="{ muted: !selectable(proxy) }" @click="toggle(proxy)">
              <td class="check-cell" @click.stop><input type="checkbox" :checked="inPool(proxy) || selected.has(proxy.id)" :disabled="!selectable(proxy)" :aria-label="label(proxy)" :data-testid="`pas-picker-check-${proxy.id}`" @change="toggle(proxy)" /></td>
              <td class="name-cell">{{ label(proxy) }}<span v-if="inPool(proxy)" class="tag" data-testid="pas-picker-in-pool">{{ t('proxyAutoSwitch.picker.inPool') }}</span><span v-else-if="proxy.status !== 'active'" class="tag">{{ t('proxyAutoSwitch.poolInactive') }}</span><span v-else-if="expired(proxy)" class="tag">{{ t('proxyAutoSwitch.expired') }}</span></td>
              <td>{{ proxy.region || '—' }}</td>
              <td class="num-left">{{ monthDay(proxy.expires_at) }}</td>
              <td class="num">{{ proxy.account_count }} / {{ maxAccounts }}</td>
            </tr>
            <tr v-if="!paged.length"><td colspan="5" class="empty" data-testid="pas-picker-empty">{{ t('proxyAutoSwitch.picker.empty') }}</td></tr>
          </tbody>
        </table>
      </div>

      <div v-if="pages > 1" class="picker-pager">
        <button type="button" class="pager-btn" :disabled="page <= 1" data-testid="pas-picker-prev" @click="page--">{{ t('proxyAutoSwitch.prevPage') }}</button>
        <span class="muted" data-testid="pas-picker-page">{{ page }} / {{ pages }}</span>
        <button type="button" class="pager-btn" :disabled="page >= pages" data-testid="pas-picker-next" @click="page++">{{ t('proxyAutoSwitch.nextPage') }}</button>
      </div>
    </div>

    <template #footer>
      <div class="picker-footer">
        <span class="muted" data-testid="pas-picker-selected">{{ t('proxyAutoSwitch.picker.selected', { count: selected.size }) }}</span>
        <div class="picker-footer-actions">
          <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('proxyAutoSwitch.picker.cancel') }}</button>
          <button type="button" class="btn btn-primary" :disabled="!selected.size" data-testid="pas-picker-confirm" @click="confirm">{{ t('proxyAutoSwitch.picker.confirm', { count: selected.size }) }}</button>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { ProxyAutoSwitchProxy } from '@/api/admin/proxyAutoSwitch'

// 从 IP 管理里挑代理加进代理池。已在池子里的打勾、不能再选；停用、过期的默认隐藏，显示时也不能选。
const props = defineProps<{ show: boolean; proxies: ProxyAutoSwitchProxy[]; poolIds: number[]; maxAccounts: number }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'add', ids: number[]): void }>()
const { t } = useI18n()

const PAGE_SIZE = 20
const keyword = ref('')
const region = ref('')
const hideInPool = ref(true)
const hideUnusable = ref(true)
const page = ref(1)
const selected = ref(new Set<number>())

// 每次打开都从干净的状态开始。
watch(() => props.show, open => {
  if (!open) return
  keyword.value = ''; region.value = ''; hideInPool.value = true; hideUnusable.value = true
  page.value = 1; selected.value = new Set()
})
watch([keyword, region, hideInPool, hideUnusable], () => { page.value = 1 })

const pool = computed(() => new Set(props.poolIds))
function inPool(proxy: ProxyAutoSwitchProxy) { return pool.value.has(proxy.id) }
function expired(proxy: ProxyAutoSwitchProxy) {
  if (proxy.expired) return true
  if (!proxy.expires_at) return false
  const at = new Date(proxy.expires_at).getTime()
  return Number.isFinite(at) && at < Date.now()
}
function usable(proxy: ProxyAutoSwitchProxy) { return proxy.status === 'active' && !expired(proxy) }
function selectable(proxy: ProxyAutoSwitchProxy) { return usable(proxy) && !inPool(proxy) }
function label(proxy: ProxyAutoSwitchProxy) { return proxy.name ? `#${proxy.id} ${proxy.name}` : `#${proxy.id}` }
function monthDay(value: string | null) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

const regions = computed(() => [...new Set(props.proxies.map(proxy => proxy.region).filter(Boolean))].sort())
const collator = new Intl.Collator('zh-Hans-CN', { numeric: true, sensitivity: 'base' })
// 按名称排序（IP 管理里的名字通常带地区和 IP，排在一起好找）。
const filtered = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return props.proxies
    .filter(proxy => !hideInPool.value || !inPool(proxy))
    .filter(proxy => !hideUnusable.value || usable(proxy))
    .filter(proxy => !region.value || proxy.region === region.value)
    .filter(proxy => !kw || label(proxy).toLowerCase().includes(kw))
    .sort((a, b) => collator.compare(a.name || '', b.name || '') || a.id - b.id)
})
const pages = computed(() => Math.max(1, Math.ceil(filtered.value.length / PAGE_SIZE)))
const paged = computed(() => filtered.value.slice((page.value - 1) * PAGE_SIZE, page.value * PAGE_SIZE))
const selectableFiltered = computed(() => filtered.value.filter(selectable))
const selectablePage = computed(() => paged.value.filter(selectable))
const pageAllSelected = computed(() => selectablePage.value.length > 0 && selectablePage.value.every(proxy => selected.value.has(proxy.id)))
const pageSomeSelected = computed(() => !pageAllSelected.value && selectablePage.value.some(proxy => selected.value.has(proxy.id)))

function toggle(proxy: ProxyAutoSwitchProxy) {
  if (!selectable(proxy)) return
  const next = new Set(selected.value)
  if (next.has(proxy.id)) next.delete(proxy.id)
  else next.add(proxy.id)
  selected.value = next
}
function togglePage() {
  const next = new Set(selected.value)
  if (pageAllSelected.value) selectablePage.value.forEach(proxy => next.delete(proxy.id))
  else selectablePage.value.forEach(proxy => next.add(proxy.id))
  selected.value = next
}
function selectAllFiltered() {
  const next = new Set(selected.value)
  selectableFiltered.value.forEach(proxy => next.add(proxy.id))
  selected.value = next
}
function confirm() {
  // 按列表里的顺序加，方便和页面上对照
  const order = new Map(props.proxies.map((proxy, i) => [proxy.id, i]))
  emit('add', [...selected.value].sort((a, b) => (order.get(a) ?? 0) - (order.get(b) ?? 0)))
}
</script>

<style scoped>
.picker { @apply flex flex-col gap-3; }
.picker-filters { @apply flex flex-wrap items-center gap-2; }
.picker-search { @apply min-w-0 flex-1 rounded-lg px-3 py-1.5 text-sm; min-width: 12rem; }
.picker-select { @apply w-auto rounded-lg px-2 py-1.5 text-sm; }
.picker-check { @apply inline-flex items-center gap-1.5 text-xs text-gray-600 dark:text-gray-300; }
.picker-bar { @apply flex flex-wrap items-center justify-between gap-2 text-xs; }
.picker-bar-actions { @apply flex items-center gap-3; }
.muted { @apply text-gray-400 dark:text-gray-500; }
.link-button { @apply text-xs text-primary-600 hover:underline disabled:cursor-not-allowed disabled:opacity-40 dark:text-primary-400; }
.picker-table-wrap { @apply max-h-[55vh] overflow-auto rounded-lg border border-gray-100 dark:border-dark-700; }
.picker-table { @apply w-full border-collapse text-left text-xs; }
.picker-table th { @apply sticky top-0 z-[1] whitespace-nowrap border-b border-gray-100 bg-white px-3 py-2 font-medium text-gray-400 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-500; }
.picker-table td { @apply whitespace-nowrap border-b border-gray-50 px-3 py-2 dark:border-dark-800; }
.picker-table tbody tr { @apply cursor-pointer hover:bg-gray-50 dark:hover:bg-dark-800/60; }
.picker-table tbody tr.muted { @apply cursor-default hover:bg-transparent; }
.picker-table .num { @apply text-right; font-variant-numeric: tabular-nums; }
.picker-table .num-left { font-variant-numeric: tabular-nums; }
.check-cell { width: 2.5rem; }
.check-cell input { @apply h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500; }
.name-cell { @apply font-mono text-[11px]; }
.tag { @apply ml-2 rounded bg-gray-100 px-1.5 py-0.5 font-sans text-[10px] text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.empty { @apply py-8 text-center text-gray-400; }
.picker-pager { @apply flex items-center justify-end gap-2 text-xs; }
.pager-btn { @apply rounded-md border border-gray-200 px-2.5 py-1 text-gray-600 hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-40 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-800; }
.picker-footer { @apply flex w-full flex-wrap items-center justify-between gap-3; }
.picker-footer-actions { @apply flex items-center gap-2; }
</style>
