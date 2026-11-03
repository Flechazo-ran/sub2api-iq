import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { useAuthStore } from './auth'
import { listQualityPlans, listQualityOperations, listQualityTemplates, listQualityPresets, listQualityFieldWatch, type QualityFieldWatchStatus, type QualityOperation, type QualityRuleTemplate, type QualityRulePreset } from '@/api/admin/accountQuality'
import { getAllIncludingInactive } from '@/api/admin/groups'
import { FIELD_WATCH_QUESTION } from '@/utils/intelligenceTest'
import type { AdminGroup, ScheduledTestPlan } from '@/types'

// Session-only summaries survive route navigation. Answer bodies stay in the detail drawer.
export const useAccountQualityStore = defineStore('accountQuality', () => {
  const auth = useAuthStore()
  const plans = ref<ScheduledTestPlan[]>([]), groups = ref<AdminGroup[]>([]), templates = ref<QualityRuleTemplate[]>([])
  const presets = ref<QualityRulePreset[]>([]), presetsLoading = ref(false), presetsError = ref('')
  const operations = ref<QualityOperation[]>([]), cursor = ref(0)
  // 字段看护实时状态，按账号 id；fieldWatchLoaded=false（接口没上线、失败）时页面不显示状态行。
  const fieldWatch = ref<Record<number, QualityFieldWatchStatus>>({}), fieldWatchLoaded = ref(false)
  const rulesLoaded = ref(false), operationsLoaded = ref(false)
  const rulesLoading = ref(false), operationsLoading = ref(false), moreLoading = ref(false)
  const rulesError = ref(''), operationsError = ref(''), groupsError = ref('')
  const updatedAt = ref(0)
  const search = ref(''), selectedPlanId = ref<number | null>(null), operationFilter = ref('all')
  let ruleVersion = 0, operationVersion = 0, groupVersion = 0, presetVersion = 0, fieldWatchVersion = 0
  let rulesFlight: Promise<void> | null = null, groupsFlight: Promise<void> | null = null
  const errorText = (e: unknown) => e instanceof Error ? e.message : 'qualityOps.error'

  function reset() {
    ruleVersion++; operationVersion++; groupVersion++; presetVersion++; fieldWatchVersion++
    presets.value = []; presetsLoading.value = false; presetsError.value = ''
    plans.value = []; groups.value = []; templates.value = []; operations.value = []; cursor.value = 0
    fieldWatch.value = {}; fieldWatchLoaded.value = false
    rulesLoaded.value = operationsLoaded.value = false
    rulesLoading.value = operationsLoading.value = moreLoading.value = false
    rulesError.value = operationsError.value = groupsError.value = ''
    search.value = ''; selectedPlanId.value = null; operationFilter.value = 'all'; updatedAt.value = 0
    rulesFlight = groupsFlight = null
  }
  async function refreshPresets() {
    const version = ++presetVersion
    presetsLoading.value = true; presetsError.value = ''
    try {
      const data = await listQualityPresets()
      if (version === presetVersion) presets.value = data
    } catch (e) { if (version === presetVersion) presetsError.value = errorText(e) }
    finally { if (version === presetVersion) presetsLoading.value = false }
  }
  watch(() => auth.user ? `${auth.user.id}:${auth.user.role}` : '', reset, { flush: 'sync' })

  function refreshRules(force = false): Promise<void> {
    if (rulesFlight && !force) return rulesFlight
    const version = ++ruleVersion
    rulesLoading.value = true; rulesError.value = ''
    const flight = (async () => {
      try {
        // 分组规则和它建出的账号规则一起刷新，卡片上的覆盖数和规则列表不会对不上。
        const [data, groupRules] = await Promise.all([listQualityPlans(), listQualityTemplates()])
        if (version !== ruleVersion) return
        plans.value = data; templates.value = groupRules; rulesLoaded.value = true
        if (selectedPlanId.value && !data.some(p => p.id === selectedPlanId.value)) selectedPlanId.value = null
        updatedAt.value = Date.now()
        // 规则到手后再拉字段看护状态；不等它，失败也不影响规则列表。
        void refreshFieldWatch()
      } catch (e) { if (version === ruleVersion) rulesError.value = errorText(e) }
      finally { if (version === ruleVersion) { rulesLoading.value = false; rulesFlight = null } }
    })()
    rulesFlight = flight
    return flight
  }
  // 只查选了字段看护的账号；没有这类规则就不发请求。接口未上线（404）或失败时静默降级：
  // 保留上次拿到的状态，从没拿到过就不显示状态行。
  async function refreshFieldWatch() {
    const version = ++fieldWatchVersion
    const ids = [...new Set(plans.value.filter(plan => plan.pelican_config?.question_kind === FIELD_WATCH_QUESTION).map(plan => plan.account_id))]
    if (!ids.length) { fieldWatch.value = {}; return }
    try {
      const items = await listQualityFieldWatch(ids)
      if (version !== fieldWatchVersion) return
      fieldWatch.value = Object.fromEntries(items.filter(item => item && typeof item.account_id === 'number').map(item => [item.account_id, item]))
      fieldWatchLoaded.value = true
    } catch { /* 静默降级，见上 */ }
  }
  function refreshGroups(): Promise<void> {
    if (groupsFlight) return groupsFlight
    const version = ++groupVersion
    groupsError.value = ''
    const flight = (async () => {
      try { const data = await getAllIncludingInactive(); if (version === groupVersion) groups.value = data }
      catch (e) { if (version === groupVersion) groupsError.value = errorText(e) }
      finally { if (version === groupVersion) groupsFlight = null }
    })()
    groupsFlight = flight
    return flight
  }
  async function refreshOperations(append = false) {
    if (append && (moreLoading.value || operationsLoading.value || !cursor.value)) return
    const version = ++operationVersion
    if (append) moreLoading.value = true
    else { operationsLoading.value = true; moreLoading.value = false }
    operationsError.value = ''
    try {
      const page = await listQualityOperations(append ? cursor.value : 0)
      if (version !== operationVersion) return
      const unique = new Map<number, QualityOperation>()
      for (const item of [...(append ? operations.value : []), ...page.items]) unique.set(item.id, item)
      operations.value = [...unique.values()]; cursor.value = page.next_cursor
      operationsLoaded.value = true; updatedAt.value = Date.now()
    } catch (e) { if (version === operationVersion) operationsError.value = errorText(e) }
    finally { if (version === operationVersion) operationsLoading.value = moreLoading.value = false }
  }
  async function refresh() {
    await Promise.all([refreshRules(), refreshGroups(), refreshOperations(), refreshPresets()])
  }
  return { plans, groups, templates, operations, cursor, rulesLoaded, operationsLoaded, rulesLoading, operationsLoading, moreLoading,
    rulesError, operationsError, groupsError, updatedAt, search, selectedPlanId, operationFilter,
    presets, presetsLoading, presetsError, refreshPresets, fieldWatch, fieldWatchLoaded, refreshFieldWatch,
    refresh, refreshRules, refreshGroups, refreshOperations, reset }
})
