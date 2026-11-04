<template>
  <AppLayout>
    <div class="proxy-workspace">
      <SmartOpsNav />
      <div class="workspace-heading">
        <div><p class="workspace-eyebrow">{{ t('accountOps.smartTitle') }}</p><h2>{{ t('proxyAutoSwitch.title') }}</h2><p class="workspace-subtitle">{{ t('proxyAutoSwitch.intro') }}</p></div>
        <span v-if="saved" class="state-badge" :class="saved.enabled ? 'state-on' : 'state-off'" data-testid="pas-state"><span />{{ saved.enabled ? t('proxyAutoSwitch.stateOn') : t('proxyAutoSwitch.stateOff') }}</span>
      </div>
      <div v-if="notice" class="workspace-message message-success" role="status" data-testid="pas-notice">{{ notice }}<button :aria-label="t('proxyAutoSwitch.dismiss')" @click="notice = ''"><Icon name="x" size="sm" /></button></div>

      <section class="workspace-panel" aria-labelledby="pas-settings-heading">
        <header class="panel-heading"><div><h3 id="pas-settings-heading">{{ t('proxyAutoSwitch.settings') }}</h3><p>{{ t('proxyAutoSwitch.settingsHint') }}</p></div></header>
        <div v-if="configError" role="alert" class="panel-error" data-testid="pas-config-error">{{ configError }}<button :disabled="configLoading" @click="loadConfig">{{ t('proxyAutoSwitch.retry') }}</button></div>
        <div v-if="!saved && configLoading" class="settings-body skeleton-stack" :aria-label="t('proxyAutoSwitch.loading')"><span v-for="n in 3" :key="n" class="cell-skeleton" /></div>
        <form v-else-if="saved" class="settings-body" novalidate data-testid="pas-settings" @submit.prevent="save">
          <div class="mode-row">
            <span class="field-label">{{ t('proxyAutoSwitch.mode') }}</span>
            <div class="mode-switch" role="radiogroup" :aria-label="t('proxyAutoSwitch.mode')">
              <button v-for="option in modes" :key="option.id" type="button" role="radio" :aria-checked="draft.enabled === option.value" :class="{ on: draft.enabled === option.value }" :data-testid="`pas-mode-${option.id}`" @click="draft.enabled = option.value">{{ t(option.label) }}</button>
            </div>
          </div>
          <div class="field-grid">
            <div class="field"><span class="field-label">{{ t('proxyAutoSwitch.slowMetric') }}</span><strong class="field-fixed" data-testid="pas-slow-metric">{{ t('proxyAutoSwitch.slowMetricValue') }}</strong><span class="field-hint">{{ t('proxyAutoSwitch.slowMetricHint') }}</span></div>
            <div v-for="card in cards" :key="card.id" class="field">
              <label class="field-label" :for="`pas-${card.primary.key}`">{{ t(card.label) }}</label>
              <span class="field-input"><span v-if="card.prefix">{{ t(card.prefix) }}</span><input :id="`pas-${card.primary.key}`" v-model.number="draft[card.primary.key]" type="number" class="input num-input" :min="ranges[card.primary.key].min" :max="ranges[card.primary.key].max" :step="card.primary.step ?? 1" :aria-invalid="fieldError(card.primary.key) ? 'true' : undefined" :data-testid="`pas-${card.primary.key}`" /><span>{{ t(card.primary.unit) }}</span></span>
              <span v-if="fieldError(card.primary.key)" class="field-error">{{ fieldError(card.primary.key) }}</span>
              <template v-if="card.sub">
                <label class="sub-field" :for="`pas-${card.sub.key}`"><span>{{ t(card.sub.label) }}</span><span class="field-input"><input :id="`pas-${card.sub.key}`" v-model.number="draft[card.sub.key]" type="number" class="input num-input" :min="ranges[card.sub.key].min" :max="ranges[card.sub.key].max" step="1" :aria-invalid="fieldError(card.sub.key) ? 'true' : undefined" :data-testid="`pas-${card.sub.key}`" /><span>{{ t(card.sub.unit) }}</span></span></label>
                <span v-if="fieldError(card.sub.key)" class="field-error">{{ fieldError(card.sub.key) }}</span>
              </template>
              <span v-if="card.hint" class="field-hint">{{ t(card.hint) }}</span>
            </div>
          </div>
          <div class="field-grid field-grid-wide">
            <fieldset class="field">
              <legend class="field-label">{{ t('proxyAutoSwitch.groups') }}</legend>
              <div class="chips" data-testid="pas-groups">
                <label v-for="group in groupOptions" :key="group.id" class="chip" :class="{ on: draft.group_ids.includes(group.id) }" :data-group-id="group.id"><input v-model="draft.group_ids" type="checkbox" class="sr-only" :value="group.id" />{{ group.name }}</label>
                <span v-if="groupsLoaded && !groupOptions.length" class="field-hint">{{ t('proxyAutoSwitch.noGroups') }}</span>
              </div>
              <span v-if="groupsError" class="field-hint text-amber-600 dark:text-amber-400" data-testid="pas-groups-error">{{ t('proxyAutoSwitch.groupsLoadFailed') }}</span>
              <span v-if="!draft.group_ids.length" class="field-hint text-amber-600 dark:text-amber-400">{{ t('proxyAutoSwitch.noGroupSelected') }}</span>
              <span class="field-hint">{{ t('proxyAutoSwitch.groupsHint') }}</span>
            </fieldset>
            <div class="field">
              <span class="field-label">{{ t('proxyAutoSwitch.candidates') }}</span>
              <div class="chips"><span class="chip on is-fixed">{{ t('proxyAutoSwitch.idleOnly') }}</span></div>
              <label class="sub-field" for="pas-min_days_to_expiry"><span>{{ t('proxyAutoSwitch.expiryDays') }}</span><span class="field-input"><input id="pas-min_days_to_expiry" v-model.number="draft.min_days_to_expiry" type="number" class="input num-input" :min="ranges.min_days_to_expiry.min" :max="ranges.min_days_to_expiry.max" step="1" :aria-invalid="fieldError('min_days_to_expiry') ? 'true' : undefined" data-testid="pas-min_days_to_expiry" /><span>{{ t('proxyAutoSwitch.unit.days') }}</span></span></label>
              <span v-if="fieldError('min_days_to_expiry')" class="field-error">{{ fieldError('min_days_to_expiry') }}</span>
              <label class="sub-field" for="pas-region"><span>{{ t('proxyAutoSwitch.regionPolicy') }}</span><select id="pas-region" v-model="draft.region_policy" class="input region-select" data-testid="pas-region"><option v-for="policy in regionPolicies" :key="policy" :value="policy">{{ t(`proxyAutoSwitch.region.${policy}`) }}</option></select></label>
            </div>
          </div>
          <footer class="settings-footer">
            <span v-if="invalidCount" class="footer-state is-error" data-testid="pas-invalid">{{ t('proxyAutoSwitch.invalidCount', { count: invalidCount }) }}</span>
            <span v-else-if="dirty" class="footer-state" data-testid="pas-dirty">{{ t('proxyAutoSwitch.dirty') }}</span>
            <span v-else />
            <div class="footer-actions"><button type="button" class="btn btn-secondary" :disabled="!dirty || saving" data-testid="pas-discard" @click="discard">{{ t('proxyAutoSwitch.discard') }}</button><button type="submit" class="btn btn-primary" :disabled="!dirty || saving || invalidCount > 0" data-testid="pas-save">{{ saving ? t('proxyAutoSwitch.saving') : t('proxyAutoSwitch.save') }}</button></div>
          </footer>
          <p v-if="saveError" role="alert" class="form-error" data-testid="pas-save-error">{{ saveError }}</p>
        </form>
      </section>

      <section class="workspace-panel" aria-labelledby="pas-events-heading">
        <header class="panel-heading"><div><h3 id="pas-events-heading">{{ t('proxyAutoSwitch.events') }}<span class="count-label">{{ events.length }}</span></h3><p>{{ t('proxyAutoSwitch.eventsHint') }}</p></div><button class="icon-button" :disabled="eventsLoading" :aria-label="t('proxyAutoSwitch.refresh')" data-testid="pas-refresh-events" @click="loadEvents"><Icon name="refresh" size="sm" :class="{ 'animate-spin': eventsLoading }" /></button></header>
        <div v-if="eventsError" role="alert" class="panel-error" data-testid="pas-events-error">{{ eventsError }}<button @click="retryEvents">{{ t('proxyAutoSwitch.retry') }}</button></div>
        <div class="events-scroll" :aria-busy="eventsLoading">
          <table class="events-table">
            <thead><tr><th>{{ t('proxyAutoSwitch.colTime') }}</th><th>{{ t('proxyAutoSwitch.colAccount') }}</th><th>{{ t('proxyAutoSwitch.colProxy') }}</th><th>{{ t('proxyAutoSwitch.colLatency') }}</th><th>{{ t('proxyAutoSwitch.colResult') }}</th><th><span class="sr-only">{{ t('proxyAutoSwitch.colActions') }}</span></th></tr></thead>
            <tbody v-if="!eventsLoaded && eventsLoading"><tr v-for="n in 4" :key="`loading-${n}`" class="loading-row"><td v-for="c in 6" :key="c"><span class="cell-skeleton" /></td></tr></tbody>
            <tbody v-for="event in events" :key="event.id" class="event-group" :data-event-id="event.id">
              <tr class="event-row" :class="{ expanded: isExpanded(event.id) }" data-testid="pas-event-row" @click="toggle(event.id)">
                <td class="time-cell" :data-label="t('proxyAutoSwitch.colTime')"><strong :title="fullTime(event.created_at)">{{ clock(event.created_at) }}</strong><span>{{ day(event.created_at) }}</span></td>
                <td class="account-cell"><strong :title="event.account_name">{{ event.account_name || `#${event.account_id}` }}</strong><span>#{{ event.account_id }}</span></td>
                <td class="proxy-cell" :data-label="t('proxyAutoSwitch.colProxy')" data-testid="pas-proxy">
                  <span class="proxy-name">{{ proxyText(event.from_proxy_id, event.from_proxy_label) }}<span v-if="event.from_region" class="region-tag">{{ event.from_region }}</span></span>
                  <template v-if="event.to_proxy_id != null"><span class="proxy-arrow" aria-hidden="true">→</span><span class="proxy-name">{{ proxyText(event.to_proxy_id, event.to_proxy_label) }}<span v-if="event.to_region" class="region-tag">{{ event.to_region }}</span></span></template>
                </td>
                <td class="latency-cell" :data-label="t('proxyAutoSwitch.colLatency')" data-testid="pas-latency">
                  <span class="latency">{{ seconds(event.before_p50_ms) }}<template v-if="event.candidate_probe_ms != null"> → <strong>{{ seconds(event.candidate_probe_ms) }}</strong></template></span>
                  <span v-if="event.current_probe_ms != null" class="cell-secondary">{{ t('proxyAutoSwitch.currentProbe', { value: seconds(event.current_probe_ms) }) }}</span>
                  <span v-if="event.before_samples != null" class="cell-secondary">{{ t('proxyAutoSwitch.beforeSamples', { count: event.before_samples }) }}</span>
                </td>
                <td class="result-cell" :data-label="t('proxyAutoSwitch.colResult')">
                  <span class="outcome-badge" :class="tone(event.status)" :data-status="event.status" data-testid="pas-status"><span />{{ statusLabel(event.status) }}</span>
                  <span v-if="event.after_p50_ms != null" class="cell-secondary">{{ t('proxyAutoSwitch.after', { value: seconds(event.after_p50_ms), count: event.after_samples ?? 0 }) }}</span>
                  <span v-if="event.reason" class="cell-secondary reason-text" :title="event.reason">{{ event.reason }}</span>
                </td>
                <td class="action-cell">
                  <div class="row-actions">
                    <button v-if="canRevert(event)" type="button" class="btn btn-secondary btn-sm" data-testid="pas-revert" @click.stop="askRevert(event)">{{ t('proxyAutoSwitch.revert') }}</button>
                    <button type="button" class="detail-button" :aria-expanded="isExpanded(event.id) ? 'true' : 'false'" :aria-controls="`pas-detail-${event.id}`" data-testid="pas-toggle" @click.stop="toggle(event.id)">{{ isExpanded(event.id) ? t('proxyAutoSwitch.hideDetails') : t('proxyAutoSwitch.details') }}<Icon name="chevronDown" size="xs" class="transition-transform" :class="{ 'rotate-180': isExpanded(event.id) }" /></button>
                  </div>
                </td>
              </tr>
              <tr v-if="isExpanded(event.id)" :id="`pas-detail-${event.id}`" class="detail-row" data-testid="pas-detail">
                <td colspan="6">
                  <div class="detail-panel">
                    <div class="detail-head"><h4>{{ t('proxyAutoSwitch.candidateTitle') }}</h4><span>{{ t('proxyAutoSwitch.currentProxy') }} {{ proxyText(event.from_proxy_id, event.from_proxy_label) }} · {{ seconds(event.current_probe_ms) }}</span></div>
                    <p v-if="event.reason" class="detail-reason"><span>{{ t('proxyAutoSwitch.reason') }}</span>{{ event.reason }}</p>
                    <table v-if="event.candidates?.length" class="candidate-table">
                      <thead><tr><th>{{ t('proxyAutoSwitch.candidateProxy') }}</th><th>{{ t('proxyAutoSwitch.candidateRegion') }}</th><th>{{ t('proxyAutoSwitch.candidateProbe') }}</th><th>{{ t('proxyAutoSwitch.candidateNote') }}</th></tr></thead>
                      <tbody><tr v-for="candidate in sortedCandidates(event)" :key="candidate.proxy_id" :class="{ chosen: candidate.proxy_id === event.to_proxy_id }" data-testid="pas-candidate"><td>{{ proxyText(candidate.proxy_id, candidate.label) }}<span v-if="candidate.proxy_id === event.to_proxy_id" class="chosen-tag">{{ t('proxyAutoSwitch.chosen') }}</span></td><td>{{ candidate.region || '—' }}</td><td class="tabular-nums">{{ seconds(candidate.probe_ms) }}</td><td class="candidate-note">{{ candidate.error || '' }}</td></tr></tbody>
                    </table>
                    <p v-else class="detail-empty">{{ t('proxyAutoSwitch.noCandidates') }}</p>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="eventsLoaded && !events.length" class="panel-empty" data-testid="pas-events-empty"><Icon name="document" size="lg" /><h4>{{ t('proxyAutoSwitch.empty') }}</h4><p>{{ t('proxyAutoSwitch.emptyHint') }}</p></div>
        </div>
        <footer class="operations-footer"><span>{{ t('proxyAutoSwitch.loadedCount', { count: events.length }) }}</span><button v-if="cursor" type="button" :disabled="moreLoading || eventsLoading" data-testid="pas-load-more" @click="loadMore">{{ moreLoading ? t('proxyAutoSwitch.loading') : t('proxyAutoSwitch.loadMore') }}<Icon name="arrowDown" size="xs" /></button><span v-else-if="eventsLoaded && events.length">{{ t('proxyAutoSwitch.loadedEnd') }}</span></footer>
      </section>
    </div>

    <BaseDialog :show="!!revertTarget" :title="t('proxyAutoSwitch.revertTitle')" width="narrow" :close-on-escape="!reverting" :show-close-button="!reverting" @close="closeRevert">
      <template v-if="revertTarget">
        <p class="text-sm leading-6 text-gray-700 dark:text-gray-200" data-testid="pas-revert-message">{{ t('proxyAutoSwitch.revertConfirm', { account: revertTarget.account_name || `#${revertTarget.account_id}`, to: proxyText(revertTarget.to_proxy_id, revertTarget.to_proxy_label), from: proxyText(revertTarget.from_proxy_id, revertTarget.from_proxy_label) }) }}</p>
        <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('proxyAutoSwitch.revertHint') }}</p>
        <p v-if="revertError" role="alert" class="form-error mt-3" data-testid="pas-revert-error">{{ revertError }}</p>
      </template>
      <template #footer><div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" :disabled="reverting" data-testid="pas-revert-cancel" @click="closeRevert">{{ t('proxyAutoSwitch.cancel') }}</button><button type="button" class="btn btn-primary" :disabled="reverting" data-testid="pas-revert-confirm" @click="confirmRevert">{{ reverting ? t('proxyAutoSwitch.reverting') : t('proxyAutoSwitch.confirmRevert') }}</button></div></template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { getAll as getAllGroups } from '@/api/admin/groups'
import { proxyAutoSwitchAPI, type ProxyAutoSwitchConfig, type ProxyAutoSwitchEvent, type ProxyAutoSwitchRegionPolicy, type ProxyAutoSwitchStatus } from '@/api/admin/proxyAutoSwitch'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t, te } = useI18n()
const PAGE_SIZE = 50

// 表单里阈值按秒填，提交时换回 threshold_ms；其余字段与后端同名。
type NumericKey = 'window_minutes' | 'min_samples' | 'threshold_s' | 'consecutive_checks' | 'min_improvement_pct' | 'account_cooldown_hours' | 'max_switches_per_hour' | 'observe_minutes' | 'min_days_to_expiry'
type Draft = Record<NumericKey, number | string> & { enabled: boolean; region_policy: ProxyAutoSwitchRegionPolicy; group_ids: number[] }
interface NumberSpec { key: NumericKey; unit: string; step?: number }
interface FieldCard { id: string; label: string; prefix?: string; primary: NumberSpec; sub?: NumberSpec & { label: string }; hint?: string }

// 与后端 PUT /config 的校验范围一致。
const ranges: Record<NumericKey, { min: number; max: number; integer: boolean }> = {
  window_minutes: { min: 5, max: 60, integer: true },
  min_samples: { min: 1, max: 1000, integer: true },
  threshold_s: { min: 0.2, max: 30, integer: false },
  consecutive_checks: { min: 1, max: 10, integer: true },
  min_improvement_pct: { min: 5, max: 90, integer: true },
  account_cooldown_hours: { min: 1, max: 72, integer: true },
  max_switches_per_hour: { min: 1, max: 50, integer: true },
  observe_minutes: { min: 5, max: 60, integer: true },
  min_days_to_expiry: { min: 0, max: 90, integer: true },
}
const cards: FieldCard[] = [
  { id: 'window', label: 'proxyAutoSwitch.window', prefix: 'proxyAutoSwitch.windowPrefix', primary: { key: 'window_minutes', unit: 'proxyAutoSwitch.unit.minutes' }, sub: { key: 'min_samples', label: 'proxyAutoSwitch.minSamples', unit: 'proxyAutoSwitch.unit.requests' } },
  { id: 'threshold', label: 'proxyAutoSwitch.threshold', primary: { key: 'threshold_s', unit: 'proxyAutoSwitch.unit.seconds', step: 0.1 }, sub: { key: 'consecutive_checks', label: 'proxyAutoSwitch.consecutive', unit: 'proxyAutoSwitch.unit.times' }, hint: 'proxyAutoSwitch.thresholdHint' },
  { id: 'improvement', label: 'proxyAutoSwitch.improvement', primary: { key: 'min_improvement_pct', unit: 'proxyAutoSwitch.unit.percent' }, hint: 'proxyAutoSwitch.improvementHint' },
  { id: 'cooldown', label: 'proxyAutoSwitch.cooldown', primary: { key: 'account_cooldown_hours', unit: 'proxyAutoSwitch.unit.hours' }, sub: { key: 'max_switches_per_hour', label: 'proxyAutoSwitch.maxPerHour', unit: 'proxyAutoSwitch.unit.accounts' } },
  { id: 'observe', label: 'proxyAutoSwitch.observe', primary: { key: 'observe_minutes', unit: 'proxyAutoSwitch.unit.minutes' }, hint: 'proxyAutoSwitch.observeHint' },
]
const modes = [{ id: 'off', value: false, label: 'proxyAutoSwitch.modeOff' }, { id: 'auto', value: true, label: 'proxyAutoSwitch.modeAuto' }]
const regionPolicies: ProxyAutoSwitchRegionPolicy[] = ['prefer_same', 'same_only', 'any']
const revertableStatuses: ProxyAutoSwitchStatus[] = ['observing', 'kept', 'kept_insufficient']
const tones: Record<ProxyAutoSwitchStatus, string> = { observing: 'tone-info', kept: 'tone-success', kept_insufficient: 'tone-success', reverted: 'tone-warning', revert_skipped: 'tone-warning', no_candidate: 'tone-neutral', failed: 'tone-danger', manual_revert: 'tone-muted' }

function toDraft(config: ProxyAutoSwitchConfig): Draft {
  return {
    enabled: config.enabled, window_minutes: config.window_minutes, min_samples: config.min_samples, threshold_s: config.threshold_ms / 1000,
    consecutive_checks: config.consecutive_checks, min_improvement_pct: config.min_improvement_pct, account_cooldown_hours: config.account_cooldown_hours,
    max_switches_per_hour: config.max_switches_per_hour, observe_minutes: config.observe_minutes, min_days_to_expiry: config.min_days_to_expiry,
    region_policy: config.region_policy, group_ids: [...(config.group_ids ?? [])],
  }
}
// 固定键顺序、分组去重排序，方便比对是否改过。
function canonical(config: ProxyAutoSwitchConfig): ProxyAutoSwitchConfig {
  return {
    enabled: config.enabled, window_minutes: config.window_minutes, min_samples: config.min_samples, threshold_ms: config.threshold_ms,
    consecutive_checks: config.consecutive_checks, min_improvement_pct: config.min_improvement_pct, account_cooldown_hours: config.account_cooldown_hours,
    max_switches_per_hour: config.max_switches_per_hour, observe_minutes: config.observe_minutes, min_days_to_expiry: config.min_days_to_expiry,
    region_policy: config.region_policy, group_ids: [...new Set(config.group_ids ?? [])].sort((a, b) => a - b),
  }
}
function toPayload(): ProxyAutoSwitchConfig {
  return canonical({
    enabled: draft.enabled, window_minutes: Number(draft.window_minutes), min_samples: Number(draft.min_samples), threshold_ms: Math.round(Number(draft.threshold_s) * 1000),
    consecutive_checks: Number(draft.consecutive_checks), min_improvement_pct: Number(draft.min_improvement_pct), account_cooldown_hours: Number(draft.account_cooldown_hours),
    max_switches_per_hour: Number(draft.max_switches_per_hour), observe_minutes: Number(draft.observe_minutes), min_days_to_expiry: Number(draft.min_days_to_expiry),
    region_policy: draft.region_policy, group_ids: draft.group_ids,
  })
}

const saved = ref<ProxyAutoSwitchConfig | null>(null)
// 「没找到快 N% 的代理」跟着已保存的「至少快」走，没加载到配置时按默认 30。
const savedImprovementPct = computed(() => saved.value?.min_improvement_pct ?? 30)
const draft = reactive<Draft>(toDraft({ enabled: false, window_minutes: 10, min_samples: 10, threshold_ms: 2500, consecutive_checks: 2, min_improvement_pct: 30, account_cooldown_hours: 6, max_switches_per_hour: 5, observe_minutes: 15, min_days_to_expiry: 7, region_policy: 'prefer_same', group_ids: [] }))
const configLoading = ref(false), configError = ref(''), saving = ref(false), saveError = ref(''), notice = ref('')
const groups = ref<{ id: number; name: string }[]>([]), groupsLoaded = ref(false), groupsError = ref(false)
let disposed = false

function fieldError(key: NumericKey): string {
  const value = draft[key], range = ranges[key]
  const valid = typeof value === 'number' && Number.isFinite(value) && value >= range.min && value <= range.max && (!range.integer || Number.isInteger(value))
  return valid ? '' : t(range.integer ? 'proxyAutoSwitch.integerError' : 'proxyAutoSwitch.rangeError', { min: range.min, max: range.max })
}
const invalidCount = computed(() => (Object.keys(ranges) as NumericKey[]).filter(key => fieldError(key)).length)
const dirty = computed(() => !!saved.value && JSON.stringify(toPayload()) !== JSON.stringify(canonical(saved.value)))
// 已选但不在 OpenAI 分组列表里的编号（停用的分组或列表没拉到）也留着显示，免得保存时被悄悄丢掉。
const groupOptions = computed(() => {
  const known = new Set(groups.value.map(group => group.id))
  const extra = [...new Set([...(saved.value?.group_ids ?? []), ...draft.group_ids])].filter(id => !known.has(id)).map(id => ({ id, name: `#${id}` }))
  return [...groups.value, ...extra]
})

function applyConfig(config: ProxyAutoSwitchConfig) {
  saved.value = canonical(config)
  Object.assign(draft, toDraft(saved.value))
}
async function loadConfig() {
  configLoading.value = true; configError.value = ''
  try {
    const config = await proxyAutoSwitchAPI.getConfig()
    if (!disposed) applyConfig(config)
  } catch (err) {
    if (!disposed) configError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.loadFailed'))
  } finally { configLoading.value = false }
}
async function loadGroups() {
  try {
    const data = await getAllGroups('openai')
    groups.value = (data ?? []).filter(group => group.platform === 'openai').map(({ id, name }) => ({ id, name }))
    groupsError.value = false
  } catch {
    groupsError.value = true
  } finally { groupsLoaded.value = true }
}
async function save() {
  if (!saved.value || saving.value || invalidCount.value || !dirty.value) return
  saving.value = true; saveError.value = ''; notice.value = ''
  const payload = toPayload()
  try {
    const config = await proxyAutoSwitchAPI.updateConfig(payload)
    applyConfig(config ?? payload)
    notice.value = t('proxyAutoSwitch.saved')
  } catch (err) {
    saveError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.saveFailed'))
  } finally { saving.value = false }
}
function discard() {
  if (saved.value) Object.assign(draft, toDraft(saved.value))
  saveError.value = ''
}

const events = ref<ProxyAutoSwitchEvent[]>([]), cursor = ref(0)
const eventsLoading = ref(false), eventsLoaded = ref(false), moreLoading = ref(false), eventsError = ref(''), lastEventsFailure = ref<'list' | 'more'>('list')
const expanded = ref<number[]>([])
let eventsSeq = 0

async function loadEvents() {
  const seq = ++eventsSeq
  eventsLoading.value = true; moreLoading.value = false; eventsError.value = ''
  try {
    const page = await proxyAutoSwitchAPI.listEvents({ before_id: 0, limit: PAGE_SIZE })
    if (seq !== eventsSeq || disposed) return
    events.value = page.items; cursor.value = page.next_cursor; eventsLoaded.value = true
  } catch (err) {
    if (seq === eventsSeq && !disposed) { eventsError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.loadFailed')); lastEventsFailure.value = 'list' }
  } finally { if (seq === eventsSeq) eventsLoading.value = false }
}
async function loadMore() {
  if (!cursor.value || moreLoading.value || eventsLoading.value) return
  const seq = eventsSeq
  moreLoading.value = true; eventsError.value = ''
  try {
    const page = await proxyAutoSwitchAPI.listEvents({ before_id: cursor.value, limit: PAGE_SIZE })
    if (seq !== eventsSeq || disposed) return
    const seen = new Set(events.value.map(event => event.id))
    events.value.push(...page.items.filter(event => !seen.has(event.id)))
    cursor.value = page.next_cursor
  } catch (err) {
    if (seq === eventsSeq && !disposed) { eventsError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.loadFailed')); lastEventsFailure.value = 'more' }
  } finally { if (seq === eventsSeq) moreLoading.value = false }
}
function retryEvents() { return lastEventsFailure.value === 'more' && events.value.length ? loadMore() : loadEvents() }
function isExpanded(id: number) { return expanded.value.includes(id) }
function toggle(id: number) { expanded.value = isExpanded(id) ? expanded.value.filter(item => item !== id) : [...expanded.value, id] }
function canRevert(event: ProxyAutoSwitchEvent) { return revertableStatuses.includes(event.status) }
function statusLabel(status: string) { const key = `proxyAutoSwitch.status.${status}`; return te(key) ? t(key, { pct: savedImprovementPct.value }) : status }
function tone(status: ProxyAutoSwitchStatus) { return tones[status] ?? 'tone-neutral' }
function sortedCandidates(event: ProxyAutoSwitchEvent) {
  const rank = (ms: number | null) => (ms == null || !Number.isFinite(ms) ? Number.POSITIVE_INFINITY : ms)
  return [...(event.candidates ?? [])].sort((a, b) => rank(a.probe_ms) - rank(b.probe_ms))
}
function proxyText(id: number | null | undefined, label: string | null | undefined) {
  if (id == null) return label || '—'
  return label ? `#${id} ${label}` : `#${id}`
}
function seconds(ms: number | null | undefined) { return ms == null || !Number.isFinite(ms) ? '—' : `${(ms / 1000).toFixed(2)} s` }
function validDate(value: string | null | undefined) { const date = value ? new Date(value) : null; return date && !Number.isNaN(date.getTime()) ? date : null }
function clock(value: string) { return validDate(value)?.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false }) ?? '—' }
function day(value: string) { return validDate(value)?.toLocaleDateString(undefined, { month: '2-digit', day: '2-digit' }) ?? '' }
function fullTime(value: string) { return validDate(value)?.toLocaleString() ?? '' }

const revertTarget = ref<ProxyAutoSwitchEvent | null>(null), reverting = ref(false), revertError = ref('')
function askRevert(event: ProxyAutoSwitchEvent) { revertTarget.value = event; revertError.value = '' }
function closeRevert() { if (reverting.value) return; revertTarget.value = null; revertError.value = '' }
async function confirmRevert() {
  const target = revertTarget.value
  if (!target || reverting.value) return
  reverting.value = true; revertError.value = ''
  try {
    const updated = await proxyAutoSwitchAPI.revertEvent(target.id)
    if (disposed) return
    const index = events.value.findIndex(event => event.id === target.id)
    if (updated && updated.id === target.id && index >= 0) events.value.splice(index, 1, updated)
    else void loadEvents()
    revertTarget.value = null
    notice.value = t('proxyAutoSwitch.revertDone', { account: target.account_name || `#${target.account_id}` })
  } catch (err) {
    if (!disposed) revertError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.revertFailed'))
  } finally { reverting.value = false }
}

onMounted(() => { void loadConfig(); void loadGroups(); void loadEvents() })
onUnmounted(() => { disposed = true })
</script>

<style scoped>
/* 沿用质量运维（AccountQualityView）的版式与配色。 */
.proxy-workspace { @apply flex w-full min-w-0 flex-col gap-4 text-gray-900 dark:text-gray-100; }
.proxy-workspace > nav { margin-bottom: 0; max-width: 100%; }
.workspace-heading { @apply flex flex-wrap items-start justify-between gap-4; }
.workspace-heading > div { @apply min-w-0; max-width: 56rem; }
.workspace-eyebrow { @apply mb-1 text-[11px] font-semibold tracking-widest text-primary-600 dark:text-primary-400; }
.workspace-heading h2 { @apply text-2xl font-semibold tracking-tight; }
.workspace-subtitle { @apply mt-1.5 text-sm leading-6 text-gray-500 dark:text-gray-400; }
.state-badge { @apply inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full px-3 py-1.5 text-xs font-medium; }
.state-badge > span { @apply h-1.5 w-1.5 rounded-full bg-current; }
.state-on { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300; }
.state-off { @apply bg-gray-100 text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.workspace-message { @apply flex items-center justify-between gap-4 rounded-xl border px-4 py-3 text-sm; }
.message-success { @apply border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300; }
.workspace-panel { @apply flex min-w-0 flex-col overflow-hidden rounded-2xl border border-gray-200/80 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.panel-heading { @apply flex shrink-0 items-center justify-between gap-3 px-5 pb-4 pt-5; }
.panel-heading h3 { @apply flex items-center gap-2 text-base font-semibold; }
.panel-heading p { @apply mt-1 text-xs leading-relaxed text-gray-500 dark:text-gray-400; }
.count-label { @apply rounded-md bg-gray-100 px-1.5 py-0.5 text-xs tabular-nums text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.icon-button { @apply flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-gray-200 text-gray-500 hover:bg-gray-50 dark:border-dark-700 dark:text-gray-400 dark:hover:bg-dark-800; }
.panel-error { @apply mx-5 mb-4 flex items-center justify-between gap-3 rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-950/40 dark:text-red-300; overflow-wrap: anywhere; }
.panel-error button { @apply shrink-0 underline; }
.panel-empty { @apply flex min-h-44 flex-col items-center justify-center gap-3 p-6 text-center text-sm text-gray-400; }
.panel-empty p { @apply max-w-sm text-xs leading-relaxed; }
.skeleton-stack { @apply space-y-3; }
.cell-skeleton { @apply block h-3 rounded bg-gray-100 dark:bg-dark-700; animation: proxy-pulse 1.7s ease-in-out infinite; }

.settings-body { @apply space-y-4 border-t border-gray-100 px-5 py-5 dark:border-dark-700; }
.mode-row { @apply flex flex-wrap items-center gap-3; }
.mode-switch { @apply inline-flex max-w-full flex-wrap gap-1 rounded-xl border border-gray-200 bg-gray-50 p-1 dark:border-dark-700 dark:bg-dark-800; }
.mode-switch button { @apply rounded-lg px-4 py-2 text-sm text-gray-500 transition-colors hover:bg-white dark:text-gray-400 dark:hover:bg-dark-700; }
.mode-switch button.on { @apply bg-gray-900 font-medium text-white hover:bg-gray-900 dark:bg-gray-100 dark:text-gray-900 dark:hover:bg-gray-100; }
.field-grid { @apply grid gap-3 sm:grid-cols-2 xl:grid-cols-3; }
.field-grid-wide { @apply xl:grid-cols-2; }
.field { @apply flex min-w-0 flex-col gap-2 rounded-xl border border-gray-200 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-800/50; }
fieldset.field { @apply m-0; }
.field-label { @apply text-xs font-medium text-gray-500 dark:text-gray-400; }
fieldset.field > legend { @apply float-left mb-2 w-full p-0; }
.field-fixed { @apply text-lg font-semibold; }
.field-input { @apply inline-flex flex-wrap items-center gap-2 text-sm text-gray-600 dark:text-gray-300; }
.num-input { width: 6.5rem; @apply rounded-lg px-3 py-1.5 text-base font-semibold tabular-nums; }
.num-input[aria-invalid='true'] { @apply border-red-400 focus:border-red-500 focus:ring-red-500/30 dark:border-red-700; }
.region-select { @apply w-auto max-w-full rounded-lg px-3 py-1.5; }
.sub-field { @apply flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-gray-500 dark:text-gray-400; }
.sub-field .num-input { width: 5.5rem; @apply text-sm; }
.field-hint { @apply text-xs leading-5 text-gray-400 dark:text-gray-500; }
.field-error { @apply text-xs text-red-600 dark:text-red-400; }
.chips { @apply flex flex-wrap gap-2; }
.chip { @apply inline-flex max-w-full cursor-pointer items-center rounded-full border border-gray-200 bg-white px-3 py-1 text-xs text-gray-600 transition-colors hover:border-gray-300 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-300 dark:hover:border-dark-500; overflow-wrap: anywhere; }
.chip.on { @apply border-primary-300 bg-primary-50 font-medium text-primary-700 dark:border-primary-700 dark:bg-primary-950/40 dark:text-primary-300; }
.chip.is-fixed { @apply cursor-default; }
.chip:focus-within { @apply ring-2 ring-primary-400 ring-offset-2 dark:ring-offset-dark-900; }
.settings-footer { @apply flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 pt-4 dark:border-dark-700; }
.footer-state { @apply text-xs text-amber-600 dark:text-amber-400; }
.footer-state.is-error { @apply text-red-600 dark:text-red-400; }
.footer-actions { @apply ml-auto flex flex-wrap items-center gap-2; }
.form-error { @apply rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300; overflow-wrap: anywhere; }

.events-scroll { @apply min-w-0 overflow-x-auto border-t border-gray-100 dark:border-dark-700; }
.events-table { @apply w-full text-left text-xs; min-width: 760px; }
.events-table thead { @apply bg-white text-gray-400 dark:bg-dark-900; box-shadow: 0 1px 0 rgb(148 163 184 / .15); }
.events-table th { @apply whitespace-nowrap px-4 py-3 font-medium; }
.event-row > td { @apply border-b border-gray-100 px-4 py-4 align-middle dark:border-dark-800; }
.event-row { @apply cursor-pointer; }
.event-row:hover, .event-row.expanded { @apply bg-slate-50/80 dark:bg-dark-800/70; }
.time-cell { @apply whitespace-nowrap tabular-nums; }
.time-cell strong { @apply block text-xs font-medium; }
.time-cell > span, .account-cell > span { @apply mt-1 block text-[10px] text-gray-400; }
.account-cell { max-width: 220px; }
.account-cell strong { @apply block truncate font-medium; max-width: 200px; }
.proxy-cell { @apply font-mono text-[11px]; }
.proxy-name { @apply inline-flex items-center gap-1.5; overflow-wrap: anywhere; }
.proxy-arrow { @apply mx-1.5 text-gray-400; }
.region-tag { @apply rounded bg-sky-50 px-1 py-0.5 font-sans text-[10px] font-medium text-sky-700 dark:bg-sky-950/40 dark:text-sky-300; }
.latency { @apply whitespace-nowrap font-mono tabular-nums; }
.latency strong { @apply font-semibold text-emerald-600 dark:text-emerald-400; }
.cell-secondary { @apply mt-1.5 block text-[10px] text-gray-400; }
.reason-text { @apply truncate; max-width: 220px; }
.outcome-badge { @apply inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-[11px] font-medium; }
.outcome-badge > span { @apply h-1.5 w-1.5 shrink-0 rounded-full bg-current; }
.tone-success { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300; }
.tone-warning { @apply bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300; }
.tone-info { @apply bg-sky-50 text-sky-700 dark:bg-sky-950/40 dark:text-sky-300; }
.tone-muted { @apply bg-slate-100 text-slate-600 dark:bg-dark-700 dark:text-gray-300; }
.tone-neutral { @apply bg-gray-100 text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.tone-danger { @apply bg-rose-50 text-rose-600 dark:bg-rose-950/40 dark:text-rose-300; }
.row-actions { @apply flex items-center justify-end gap-2; }
.detail-button { @apply inline-flex min-h-8 items-center gap-1 whitespace-nowrap rounded-md px-1 text-xs text-gray-400 hover:text-primary-600 dark:hover:text-primary-400; }
.detail-row > td { @apply border-b border-gray-100 bg-slate-50/60 px-4 pb-4 pt-0 dark:border-dark-800 dark:bg-dark-800/40; }
.detail-panel { @apply rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900; }
.detail-head { @apply mb-3 flex flex-wrap items-baseline justify-between gap-2; }
.detail-head h4 { @apply text-sm font-semibold; }
.detail-head span { @apply font-mono text-[11px] text-gray-500 dark:text-gray-400; overflow-wrap: anywhere; }
.detail-reason { @apply mb-3 text-xs leading-5 text-gray-600 dark:text-gray-300; overflow-wrap: anywhere; }
.detail-reason span { @apply mr-2 text-gray-400; }
.candidate-table { @apply w-full table-fixed text-left text-xs; }
.candidate-table th { @apply py-2 pr-3 font-normal text-gray-500 dark:text-gray-400; }
.candidate-table td { @apply border-t border-gray-100 py-2 pr-3 align-top dark:border-dark-700; overflow-wrap: anywhere; }
.candidate-table th:first-child { width: 40%; }
.candidate-table tr.chosen td { @apply font-medium text-emerald-700 dark:text-emerald-300; }
.chosen-tag { @apply ml-2 rounded bg-emerald-50 px-1.5 py-0.5 text-[10px] text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300; }
.candidate-note { @apply text-rose-600 dark:text-rose-400; }
.detail-empty { @apply text-xs text-gray-400; }
.operations-footer { @apply flex min-h-14 shrink-0 flex-wrap items-center justify-between gap-3 border-t border-gray-100 px-5 py-3 text-[11px] text-gray-400 dark:border-dark-700; }
.operations-footer button { @apply inline-flex items-center gap-1.5 text-primary-600 dark:text-primary-400; }
button:disabled { @apply cursor-not-allowed opacity-40; }
button:focus-visible, input:focus-visible, select:focus-visible { @apply outline-none ring-2 ring-primary-400 ring-offset-2 dark:ring-offset-dark-900; }
@keyframes proxy-pulse { 50% { opacity: .45; } }

/* 手机宽度：表格改成卡片，不横向滚动。 */
@media (max-width: 767px) {
  .events-scroll { overflow-x: visible; }
  .events-table { display: block; min-width: 0; }
  .events-table thead { @apply sr-only; }
  .events-table tbody { display: block; }
  .event-row { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; padding: 16px; @apply border-b border-gray-100 dark:border-dark-800; }
  .event-row > td { min-width: 0; max-width: none; padding: 0; border: 0; }
  .event-row > td[data-label]::before { content: attr(data-label); @apply mb-1.5 block font-sans text-[10px] text-gray-400; }
  .event-row .account-cell { grid-column: 1 / -1; grid-row: 1; }
  .event-row .account-cell strong { max-width: none; white-space: normal; overflow-wrap: anywhere; }
  .event-row .proxy-cell, .event-row .result-cell, .event-row .action-cell { grid-column: 1 / -1; }
  .event-row .reason-text { max-width: 100%; white-space: normal; }
  .detail-row { display: block; }
  .detail-row > td { display: block; padding: 0 16px 16px; }
  .panel-heading, .settings-body, .operations-footer { @apply px-4; }
  .panel-error { @apply mx-4; }
}
@media (prefers-reduced-motion: reduce) { .cell-skeleton { animation: none; } }
</style>
