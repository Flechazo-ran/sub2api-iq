<template>
  <AppLayout>
    <div class="proxy-workspace">
      <RoutingNav />
      <div class="workspace-heading">
        <div><p class="workspace-eyebrow">{{ t('nav.routing') }}</p><h2>{{ t('proxyAutoSwitch.title') }}</h2><p class="workspace-subtitle">{{ t('proxyAutoSwitch.intro') }}</p></div>
        <span v-if="saved" class="state-badge" :class="saved.enabled ? 'state-on' : 'state-off'" data-testid="pas-state"><span />{{ saved.enabled ? t('proxyAutoSwitch.stateOn') : t('proxyAutoSwitch.stateOff') }}</span>
      </div>
      <div v-if="notice" class="workspace-message message-success" role="status" data-testid="pas-notice">{{ notice }}<button :aria-label="t('proxyAutoSwitch.dismiss')" @click="notice = ''"><Icon name="x" size="sm" /></button></div>

      <section class="workspace-panel" aria-labelledby="pas-settings-heading">
        <header class="panel-heading"><div><h3 id="pas-settings-heading">{{ t('proxyAutoSwitch.settings') }}</h3><p>{{ t('proxyAutoSwitch.settingsHint') }}</p></div></header>
        <div v-if="configError" role="alert" class="panel-error" data-testid="pas-config-error">{{ configError }}<button :disabled="configLoading" @click="loadConfig">{{ t('proxyAutoSwitch.retry') }}</button></div>
        <div v-if="!saved && configLoading" class="settings-body skeleton-stack" :aria-label="t('proxyAutoSwitch.loading')"><span v-for="n in 3" :key="n" class="cell-skeleton" /></div>
        <form v-else-if="saved" class="settings-body" novalidate data-testid="pas-settings" @submit.prevent="save">
          <div class="settings-row">
            <div class="inline-field">
              <span class="field-label">{{ t('proxyAutoSwitch.enabled') }}</span>
              <button type="button" role="switch" class="switch-chip" :class="{ on: draft.enabled }" :aria-checked="draft.enabled" data-testid="pas-enabled" @click="draft.enabled = !draft.enabled"><span class="switch-dot" />{{ draft.enabled ? t('proxyAutoSwitch.on') : t('proxyAutoSwitch.off') }}</button>
            </div>
            <div class="inline-field groups-field">
              <span class="field-label">{{ t('proxyAutoSwitch.groups') }}</span>
              <div class="chips" data-testid="pas-groups">
                <label v-for="group in groupOptions" :key="group.id" class="chip" :class="{ on: draft.group_ids.includes(group.id) }" :data-group-id="group.id"><input v-model="draft.group_ids" type="checkbox" class="sr-only" :value="group.id" />{{ group.name }}</label>
                <span v-if="groupsLoaded && !groupOptions.length" class="field-hint">{{ t('proxyAutoSwitch.noGroups') }}</span>
              </div>
              <span v-if="groupsError" class="field-hint field-warn" data-testid="pas-groups-error">{{ t('proxyAutoSwitch.groupsLoadFailed') }}</span>
              <span v-if="!draft.group_ids.length" class="field-hint field-warn">{{ t('proxyAutoSwitch.noGroupSelected') }}</span>
              <span class="field-hint">{{ t('proxyAutoSwitch.groupsHint') }}</span>
            </div>
          </div>

          <div class="threshold-row">
            <label class="inline-field" for="pas-window_minutes"><span class="field-label">{{ t('proxyAutoSwitch.window') }}</span><input id="pas-window_minutes" v-model.number="draft.window_minutes" type="number" class="input num-input" min="1" max="60" step="1" :aria-invalid="fieldError('window_minutes') ? 'true' : undefined" data-testid="pas-window_minutes" /><span>{{ t('proxyAutoSwitch.unit.minutes') }}</span><span v-if="fieldError('window_minutes')" class="field-error">{{ fieldError('window_minutes') }}</span></label>
            <label class="inline-field" for="pas-min_samples"><span class="field-label">{{ t('proxyAutoSwitch.minSamples') }}</span><input id="pas-min_samples" v-model.number="draft.min_samples" type="number" class="input num-input" min="1" max="100000" step="1" :aria-invalid="fieldError('min_samples') ? 'true' : undefined" data-testid="pas-min_samples" /><span>{{ t('proxyAutoSwitch.unit.requests') }}</span><span v-if="fieldError('min_samples')" class="field-error">{{ fieldError('min_samples') }}</span></label>
            <label class="inline-field" for="pas-p60_threshold_s"><span class="field-label">{{ t('proxyAutoSwitch.p60Threshold') }}</span><input id="pas-p60_threshold_s" v-model.number="draft.p60_threshold_s" type="number" class="input num-input" min="0.001" max="600" step="any" :aria-invalid="fieldError('p60_threshold_s') ? 'true' : undefined" data-testid="pas-p60_threshold_s" /><span>{{ t('proxyAutoSwitch.unit.seconds') }}</span><span v-if="fieldError('p60_threshold_s')" class="field-error">{{ fieldError('p60_threshold_s') }}</span></label>
            <label class="inline-field" for="pas-mean_threshold_s"><span class="field-label">{{ t('proxyAutoSwitch.meanThreshold') }}</span><input id="pas-mean_threshold_s" v-model.number="draft.mean_threshold_s" type="number" class="input num-input" min="0.001" max="600" step="any" :aria-invalid="fieldError('mean_threshold_s') ? 'true' : undefined" data-testid="pas-mean_threshold_s" /><span>{{ t('proxyAutoSwitch.unit.seconds') }}</span><span v-if="fieldError('mean_threshold_s')" class="field-error">{{ fieldError('mean_threshold_s') }}</span></label>
          </div>

          <p class="rule" data-testid="pas-rule">{{ ruleText }}</p>

          <div class="threshold-row">
            <label class="inline-field" for="pas-max_accounts_per_proxy"><span class="field-label">{{ t('proxyAutoSwitch.maxAccountsPerProxy') }}</span><input id="pas-max_accounts_per_proxy" v-model.number="draft.max_accounts_per_proxy" type="number" class="input num-input" min="1" max="100" step="1" :aria-invalid="fieldError('max_accounts_per_proxy') ? 'true' : undefined" data-testid="pas-max_accounts_per_proxy" /><span>{{ t('proxyAutoSwitch.unit.accounts') }}</span><span v-if="fieldError('max_accounts_per_proxy')" class="field-error">{{ fieldError('max_accounts_per_proxy') }}</span></label>
          </div>

          <div class="subsection-heading">
            <div>
              <h4>{{ t('proxyAutoSwitch.proxyPool') }}<span class="count-label" data-testid="pas-pool-count">{{ draft.candidate_proxy_ids.length }}</span></h4>
              <p class="field-hint">{{ t('proxyAutoSwitch.proxyPoolHint') }}</p>
              <div class="pool-actions">
                <button type="button" class="btn btn-secondary btn-sm" :disabled="!proxiesLoaded" data-testid="pas-pool-add" @click="pickerOpen = true"><Icon name="plus" size="sm" />{{ t('proxyAutoSwitch.poolAdd') }}</button>
                <button v-if="unusablePoolCount" type="button" class="btn btn-secondary btn-sm" data-testid="pas-pool-remove-unusable" @click="removeUnusable">{{ t('proxyAutoSwitch.poolRemoveUnusable', { count: unusablePoolCount }) }}</button>
              </div>
            </div>
            <div class="pool-strategy" data-testid="pas-strategy">
              <span class="field-label">{{ t('proxyAutoSwitch.strategy') }}</span>
              <div class="strategy-group" role="radiogroup" :aria-label="t('proxyAutoSwitch.strategy')">
                <button v-for="option in strategies" :key="option" type="button" role="radio" :aria-checked="draft.strategy === option" :class="{ on: draft.strategy === option }" :data-testid="`pas-strategy-${option}`" @click="draft.strategy = option">{{ t(`proxyAutoSwitch.strategyLabel.${option}`) }}</button>
              </div>
            </div>
          </div>
          <div v-if="proxiesError" role="alert" class="inline-error" data-testid="pas-proxies-error">{{ proxiesError }}<button :disabled="proxiesLoading" @click="loadProxies">{{ t('proxyAutoSwitch.retry') }}</button></div>
          <div class="table-scroll">
            <table class="data-table proxy-table" data-testid="pas-proxies">
              <thead><tr><th class="cell-left rank-cell">{{ t('proxyAutoSwitch.colRank') }}</th><th class="cell-left">{{ t('proxyAutoSwitch.colProxy') }}</th><th class="cell-left">{{ t('proxyAutoSwitch.colRegion') }}</th><th class="cell-left">{{ t('proxyAutoSwitch.colExpires') }}</th><th>{{ t('proxyAutoSwitch.colAccountCount') }}</th><th class="cell-left action-cell" /></tr></thead>
              <tbody v-if="!proxiesLoaded && proxiesLoading"><tr v-for="n in 3" :key="`proxy-loading-${n}`" class="loading-row"><td v-for="c in 6" :key="c"><span class="cell-skeleton" /></td></tr></tbody>
              <tbody v-else>
                <tr v-for="proxy in poolRows" :key="proxy.id" :data-proxy-id="proxy.id" :class="{ 'row-expired': !isUsable(proxy) || isFull(proxy) }">
                  <td class="cell-left rank-cell" data-testid="pas-proxy-rank">{{ poolRank.get(proxy.id) ?? '—' }}</td>
                  <td class="cell-left proxy-name-cell">{{ proxyLabel(proxy) }}<span v-if="poolNote(proxy)" class="expired-tag" data-testid="pas-proxy-note">{{ poolNote(proxy) }}</span></td>
                  <td class="cell-left">{{ proxy.region || '—' }}</td>
                  <td class="cell-left tabular-nums">{{ monthDay(proxy.expires_at) }}</td>
                  <td class="tabular-nums" data-testid="pas-proxy-accounts">{{ proxy.account_count }} / {{ savedMax }}</td>
                  <td class="cell-left action-cell"><button type="button" class="link-button" :aria-label="t('proxyAutoSwitch.poolRemoveLabel', { name: proxyLabel(proxy) })" :data-testid="`pas-pool-remove-${proxy.id}`" @click="removeFromPool(proxy.id)">{{ t('proxyAutoSwitch.poolRemove') }}</button></td>
                </tr>
                <tr v-for="id in missingPoolIds" :key="`missing-${id}`" :data-proxy-id="id" class="row-expired">
                  <td class="cell-left rank-cell">—</td>
                  <td class="cell-left proxy-name-cell">#{{ id }}<span class="expired-tag">{{ t('proxyAutoSwitch.poolDeleted') }}</span></td>
                  <td class="cell-left">—</td><td class="cell-left">—</td><td class="tabular-nums">—</td>
                  <td class="cell-left action-cell"><button type="button" class="link-button" :data-testid="`pas-pool-remove-${id}`" @click="removeFromPool(id)">{{ t('proxyAutoSwitch.poolRemove') }}</button></td>
                </tr>
                <tr v-if="!poolRows.length && !missingPoolIds.length"><td colspan="6" class="table-empty" data-testid="pas-proxies-empty">{{ t('proxyAutoSwitch.poolEmpty') }}<span>{{ t('proxyAutoSwitch.poolEmptyHint') }}</span></td></tr>
              </tbody>
            </table>
          </div>

          <footer class="settings-footer">
            <span v-if="invalidCount" class="footer-state is-error" data-testid="pas-invalid">{{ t('proxyAutoSwitch.invalidCount', { count: invalidCount }) }}</span>
            <span v-else-if="dirty" class="footer-state" data-testid="pas-dirty">{{ t('proxyAutoSwitch.dirty') }}</span>
            <span v-else />
            <div class="footer-actions"><button type="button" class="btn btn-secondary" :disabled="!dirty || saving" data-testid="pas-discard" @click="discard">{{ t('proxyAutoSwitch.discard') }}</button><button type="submit" class="btn btn-primary" :disabled="saving || invalidCount > 0 || !dirty" data-testid="pas-save">{{ saving ? t('proxyAutoSwitch.saving') : t('proxyAutoSwitch.save') }}</button></div>
          </footer>
          <p v-if="saveError" role="alert" class="form-error" data-testid="pas-save-error">{{ saveError }}</p>
        </form>
      </section>

      <section class="workspace-panel" aria-labelledby="pas-accounts-heading">
        <header class="panel-heading"><div><h3 id="pas-accounts-heading">{{ t('proxyAutoSwitch.accountMonitor') }}</h3><p data-testid="pas-account-window">{{ t('proxyAutoSwitch.accountWindowHint', { minutes: monitorWindow }) }}</p></div></header>
        <div v-if="accountsError" role="alert" class="panel-error" data-testid="pas-accounts-error">{{ accountsError }}<button :disabled="accountsLoading" @click="loadAccounts">{{ t('proxyAutoSwitch.retry') }}</button></div>
        <div class="table-scroll">
          <table class="data-table accounts-table" data-testid="pas-accounts">
            <thead><tr><th class="cell-left">{{ t('proxyAutoSwitch.colAccount') }}</th><th class="cell-left">{{ t('proxyAutoSwitch.colCurrentProxy') }}</th><th>{{ t('proxyAutoSwitch.colSamples') }}</th><th>{{ t('proxyAutoSwitch.colP60') }}</th><th>{{ t('proxyAutoSwitch.colMean') }}</th><th>{{ t('proxyAutoSwitch.colDuration') }}</th><th class="cell-left">{{ t('proxyAutoSwitch.colState') }}</th><th class="cell-left">{{ t('proxyAutoSwitch.colLastSwitch') }}</th></tr></thead>
            <tbody v-if="!accountsLoaded && accountsLoading"><tr v-for="n in 3" :key="`account-loading-${n}`" class="loading-row"><td v-for="c in 8" :key="c"><span class="cell-skeleton" /></td></tr></tbody>
            <tbody v-else>
              <tr v-for="account in accounts" :key="account.account_id" :data-account-id="account.account_id">
                <td class="cell-left account-name"><strong>#{{ account.account_id }}</strong><span>{{ account.account_name }}</span></td>
                <td class="cell-left" data-testid="pas-account-proxy"><span>{{ account.proxy_id == null ? '—' : `#${account.proxy_id} ${account.proxy_label || ''}`.trim() }}</span><span v-if="account.region" class="region-tag">{{ account.region }}</span></td>
                <td class="tabular-nums" data-testid="pas-account-samples">{{ account.samples }}</td>
                <td class="tabular-nums" data-testid="pas-account-p60">{{ seconds(account.p60_ms) }}</td>
                <td class="tabular-nums" data-testid="pas-account-mean">{{ seconds(account.mean_ms) }}</td>
                <td class="tabular-nums" data-testid="pas-account-duration">{{ seconds(account.duration_mean_ms) }}</td>
                <td class="cell-left"><span class="state-pill" :class="accountStateTone(account.state)" :data-state="account.state" data-testid="pas-account-state">{{ accountStateLabel(account) }}</span><span v-if="account.note" class="cell-note">{{ account.note }}</span></td>
                <td class="cell-left tabular-nums" data-testid="pas-account-last-switch">{{ lastSwitch(account.last_switch_at) }}</td>
              </tr>
              <tr v-if="!accounts.length"><td colspan="8" class="table-empty" data-testid="pas-accounts-empty">{{ t('proxyAutoSwitch.accountsEmpty') }}</td></tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="workspace-panel" aria-labelledby="pas-events-heading">
        <header class="panel-heading"><div><h3 id="pas-events-heading">{{ t('proxyAutoSwitch.events') }}<span class="count-label">{{ events.length }}</span></h3><p>{{ t('proxyAutoSwitch.eventsHint') }}</p></div><button class="icon-button" :disabled="eventsLoading" :aria-label="t('proxyAutoSwitch.refresh')" data-testid="pas-refresh-events" @click="loadEvents"><Icon name="refresh" size="sm" :class="{ 'animate-spin': eventsLoading }" /></button></header>
        <div v-if="eventsError" role="alert" class="panel-error" data-testid="pas-events-error">{{ eventsError }}<button @click="retryEvents">{{ t('proxyAutoSwitch.retry') }}</button></div>
        <div class="table-scroll" :aria-busy="eventsLoading">
          <table class="data-table events-table" data-testid="pas-events">
            <thead>
              <tr class="group-row"><th class="cell-left" colspan="4" /><th class="cell-left" colspan="3">{{ t('proxyAutoSwitch.beforeWindow', { minutes: configWindow }) }}</th><th class="cell-left" colspan="3">{{ t('proxyAutoSwitch.afterWindow') }}</th><th class="cell-left" colspan="2">{{ t('proxyAutoSwitch.change') }}</th><th class="cell-left" colspan="2">{{ t('proxyAutoSwitch.proxyProbe') }}</th></tr>
              <tr><th class="cell-left">{{ t('proxyAutoSwitch.colTime') }}</th><th class="cell-left">{{ t('proxyAutoSwitch.colAccount') }}</th><th class="cell-left">{{ t('proxyAutoSwitch.colProxy') }}</th><th class="cell-left">{{ t('proxyAutoSwitch.colStrategy') }}</th><th>{{ t('proxyAutoSwitch.colSamplesShort') }}</th><th>{{ t('proxyAutoSwitch.p60Short') }}</th><th>{{ t('proxyAutoSwitch.meanShort') }}</th><th>{{ t('proxyAutoSwitch.colSamplesShort') }}</th><th>{{ t('proxyAutoSwitch.p60Short') }}</th><th>{{ t('proxyAutoSwitch.meanShort') }}</th><th>{{ t('proxyAutoSwitch.p60Short') }}</th><th>{{ t('proxyAutoSwitch.meanShort') }}</th><th>{{ t('proxyAutoSwitch.colProbeOld') }}</th><th>{{ t('proxyAutoSwitch.colProbeNew') }}</th></tr>
            </thead>
            <tbody v-if="!eventsLoaded && eventsLoading"><tr v-for="n in 4" :key="`loading-${n}`" class="loading-row"><td v-for="c in 14" :key="c"><span class="cell-skeleton" /></td></tr></tbody>
            <tbody v-else>
              <tr v-for="event in pagedEvents" :key="event.id" class="event-row" :data-event-id="event.id" data-testid="pas-event-row">
                <td class="cell-left time-cell"><strong :title="fullTime(eventAt(event))">{{ clock(eventAt(event)) }}</strong><span>{{ day(eventAt(event)) }}</span></td>
                <td class="cell-left account-cell"><strong>#{{ event.account_id }}</strong><span v-if="event.account_name">{{ event.account_name }}</span><span class="status-badge" :class="statusTone(event.status)" :data-status="event.status" data-testid="pas-status">{{ statusLabel(event.status) }}</span></td>
                <td class="cell-left proxy-cell" data-testid="pas-proxy">
                  <span class="proxy-line"><span class="proxy-name">{{ proxyText(event.from_proxy_id, event.from_proxy_label) }}</span><span v-if="event.from_region" class="region-tag">{{ event.from_region }}</span><template v-if="event.to_proxy_id != null"><span class="proxy-arrow" aria-hidden="true">→</span><span class="proxy-name">{{ proxyText(event.to_proxy_id, event.to_proxy_label) }}</span><span v-if="event.to_region" class="region-tag">{{ event.to_region }}</span></template></span>
                  <span v-if="event.reason" class="reason-text" :title="event.reason">{{ event.reason }}</span>
                </td>
                <td class="cell-left" data-testid="pas-strategy">{{ strategyLabel(event.strategy) }}</td>
                <td class="tabular-nums" data-testid="pas-before-samples">{{ event.before?.samples ?? '—' }}</td>
                <td class="tabular-nums" data-testid="pas-before-p60">{{ seconds(event.before?.p60_ms) }}</td>
                <td class="tabular-nums" data-testid="pas-before-mean">{{ seconds(event.before?.mean_ms) }}</td>
                <td class="tabular-nums" data-testid="pas-after-samples">{{ event.after?.samples ?? '—' }}</td>
                <td class="tabular-nums" data-testid="pas-after-p60">{{ seconds(event.after?.p60_ms) }}</td>
                <td class="tabular-nums" data-testid="pas-after-mean">{{ seconds(event.after?.mean_ms) }}</td>
                <template v-if="event.status === 'measuring'">
                  <td class="cell-left measuring-cell" colspan="2" data-testid="pas-measuring">{{ t('proxyAutoSwitch.measuringNote') }}</td>
                </template>
                <template v-else>
                  <td class="tabular-nums" :class="changeFor(event, 'p60_ms').tone" data-testid="pas-change-p60">{{ changeFor(event, 'p60_ms').text }}</td>
                  <td class="tabular-nums" :class="changeFor(event, 'mean_ms').tone" data-testid="pas-change-mean">{{ changeFor(event, 'mean_ms').text }}</td>
                </template>
                <td class="tabular-nums" data-testid="pas-probe-old">{{ probeText(event.from_probe_ms, event) }}</td>
                <td class="tabular-nums" data-testid="pas-probe-new">{{ probeText(event.to_probe_ms, event) }}</td>
              </tr>
              <tr v-if="!events.length"><td colspan="14" class="table-empty" data-testid="pas-events-empty">{{ t('proxyAutoSwitch.empty') }}<span>{{ t('proxyAutoSwitch.emptyHint') }}</span></td></tr>
            </tbody>
          </table>
        </div>
        <footer v-if="events.length" class="operations-footer" data-testid="pas-events-pager">
          <span data-testid="pas-events-range">{{ t('proxyAutoSwitch.pageInfo', { from: eventPageFrom, to: eventPageTo }) }}<template v-if="!cursor"> · {{ t('proxyAutoSwitch.pageTotal', { total: events.length }) }}</template></span>
          <div class="pager">
            <label class="pager-size"><span>{{ t('proxyAutoSwitch.pageSize') }}</span>
              <select v-model.number="eventPageSize" class="pager-select" data-testid="pas-events-page-size"><option v-for="size in eventPageSizes" :key="size" :value="size">{{ size }}</option></select>
            </label>
            <button type="button" class="pager-btn" :disabled="eventPage <= 1 || eventsLoading" data-testid="pas-events-prev" @click="eventPage--">{{ t('proxyAutoSwitch.prevPage') }}</button>
            <span class="pager-num" data-testid="pas-events-page">{{ cursor ? t('proxyAutoSwitch.pageOnly', { page: eventPage }) : `${eventPage} / ${eventPages}` }}</span>
            <button type="button" class="pager-btn" :disabled="!hasNextEventPage || moreLoading || eventsLoading" data-testid="pas-events-next" @click="nextEventPage">{{ moreLoading ? t('proxyAutoSwitch.loading') : t('proxyAutoSwitch.nextPage') }}</button>
          </div>
        </footer>
      </section>
    </div>
    <ProxyPoolPicker :show="pickerOpen" :proxies="proxies" :pool-ids="draft.candidate_proxy_ids" :max-accounts="savedMax" @close="pickerOpen = false" @add="addToPool" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import RoutingNav from '@/components/admin/operations/RoutingNav.vue'
import ProxyPoolPicker from '@/components/admin/proxyAutoSwitch/ProxyPoolPicker.vue'
import Icon from '@/components/icons/Icon.vue'
import { getAll as getAllGroups } from '@/api/admin/groups'
import {
  proxyAutoSwitchAPI,
  type ProxyAutoSwitchAccount,
  type ProxyAutoSwitchConfig,
  type ProxyAutoSwitchEvent,
  type ProxyAutoSwitchProxy,
  type ProxyAutoSwitchStatus,
  type ProxyAutoSwitchStrategy,
  type ProxyAutoSwitchUpdate,
} from '@/api/admin/proxyAutoSwitch'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t, te } = useI18n()
const PAGE_SIZE = 50
const strategies: ProxyAutoSwitchStrategy[] = ['random', 'sequential', 'region']

type NumericKey = 'window_minutes' | 'min_samples' | 'p60_threshold_s' | 'mean_threshold_s' | 'max_accounts_per_proxy'
interface Draft {
  enabled: boolean
  group_ids: number[]
  window_minutes: number | string
  min_samples: number | string
  p60_threshold_s: number | string
  mean_threshold_s: number | string
  max_accounts_per_proxy: number | string
  candidate_proxy_ids: number[]
  strategy: ProxyAutoSwitchStrategy
}

// 与后端 PUT /config 的校验范围一致：窗口 1–60 分钟、样本 1–100000、两个阈值 1–600000 毫秒。
const ranges: Record<NumericKey, { min: number; max: number; integer: boolean }> = {
  window_minutes: { min: 1, max: 60, integer: true },
  min_samples: { min: 1, max: 100000, integer: true },
  p60_threshold_s: { min: 0.001, max: 600, integer: false },
  mean_threshold_s: { min: 0.001, max: 600, integer: false },
  max_accounts_per_proxy: { min: 1, max: 100, integer: true },
}
const defaultConfig: ProxyAutoSwitchConfig = { enabled: false, group_ids: [], window_minutes: 5, min_samples: 20, p60_threshold_ms: 10000, mean_threshold_ms: 15000, max_accounts_per_proxy: 2, candidate_proxy_ids: [], strategy: 'sequential' }

function finiteNumber(value: unknown, fallback: number): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}
function normalizeStrategy(value: unknown): ProxyAutoSwitchStrategy {
  return strategies.includes(value as ProxyAutoSwitchStrategy) ? (value as ProxyAutoSwitchStrategy) : 'sequential'
}
// 兼容老配置：缺的新字段用默认值，老字段忽略。
function normalizeConfig(raw: Partial<ProxyAutoSwitchConfig> | null | undefined): ProxyAutoSwitchConfig {
  return {
    enabled: !!raw?.enabled,
    group_ids: Array.isArray(raw?.group_ids) ? raw.group_ids.map(Number).filter(Number.isFinite) : [],
    window_minutes: finiteNumber(raw?.window_minutes, defaultConfig.window_minutes),
    min_samples: finiteNumber(raw?.min_samples, defaultConfig.min_samples),
    p60_threshold_ms: finiteNumber(raw?.p60_threshold_ms, defaultConfig.p60_threshold_ms),
    mean_threshold_ms: finiteNumber(raw?.mean_threshold_ms, defaultConfig.mean_threshold_ms),
    max_accounts_per_proxy: finiteNumber(raw?.max_accounts_per_proxy, defaultConfig.max_accounts_per_proxy),
    candidate_proxy_ids: Array.isArray(raw?.candidate_proxy_ids) ? [...new Set(raw.candidate_proxy_ids.map(Number).filter(Number.isFinite))] : [],
    strategy: normalizeStrategy(raw?.strategy),
  }
}
function toDraft(config: ProxyAutoSwitchConfig): Draft {
  return {
    enabled: config.enabled,
    group_ids: [...config.group_ids],
    window_minutes: config.window_minutes,
    min_samples: config.min_samples,
    p60_threshold_s: config.p60_threshold_ms / 1000,
    mean_threshold_s: config.mean_threshold_ms / 1000,
    max_accounts_per_proxy: config.max_accounts_per_proxy,
    candidate_proxy_ids: [...config.candidate_proxy_ids],
    strategy: config.strategy,
  }
}
// 固定键顺序、分组和代理池都去重排序，方便比对是否改过（池内顺序由延迟排名决定，加入顺序不重要）。
function canonical(config: ProxyAutoSwitchConfig): ProxyAutoSwitchUpdate {
  return {
    enabled: config.enabled,
    group_ids: [...new Set(config.group_ids)].sort((a, b) => a - b),
    window_minutes: config.window_minutes,
    min_samples: config.min_samples,
    p60_threshold_ms: config.p60_threshold_ms,
    mean_threshold_ms: config.mean_threshold_ms,
    max_accounts_per_proxy: config.max_accounts_per_proxy,
    candidate_proxy_ids: [...new Set(config.candidate_proxy_ids)].sort((a, b) => a - b),
    strategy: config.strategy,
  }
}
function toPayload(): ProxyAutoSwitchUpdate {
  return canonical({
    enabled: draft.enabled,
    group_ids: draft.group_ids,
    window_minutes: Number(draft.window_minutes),
    min_samples: Number(draft.min_samples),
    p60_threshold_ms: Math.round(Number(draft.p60_threshold_s) * 1000),
    mean_threshold_ms: Math.round(Number(draft.mean_threshold_s) * 1000),
    max_accounts_per_proxy: Number(draft.max_accounts_per_proxy),
    candidate_proxy_ids: [...draft.candidate_proxy_ids],
    strategy: draft.strategy,
  })
}

const saved = ref<ProxyAutoSwitchConfig | null>(null)
const draft = reactive<Draft>(toDraft(defaultConfig))
const dirty = computed(() => !!saved.value && JSON.stringify(toPayload()) !== JSON.stringify(canonical(saved.value)))
const configLoading = ref(false), configError = ref(''), saving = ref(false), saveError = ref(''), notice = ref('')
const groups = ref<{ id: number; name: string }[]>([]), groupsLoaded = ref(false), groupsError = ref(false)
let disposed = false

function fieldError(key: NumericKey): string {
  const raw = draft[key]
  const value = Number(raw)
  const range = ranges[key]
  const empty = typeof raw === 'string' && raw.trim() === ''
  if (empty || !Number.isFinite(value)) return t(range.integer ? 'proxyAutoSwitch.integerError' : 'proxyAutoSwitch.rangeError', range)
  if (range.integer) {
    const valid = Number.isInteger(value) && value >= range.min && value <= range.max
    return valid ? '' : t('proxyAutoSwitch.integerError', range)
  }
  const ms = Math.round(value * 1000)
  return ms >= 1 && ms <= 600000 ? '' : t('proxyAutoSwitch.rangeError', range)
}
const numericKeys = Object.keys(ranges) as NumericKey[]
const invalidCount = computed(() => numericKeys.filter(key => fieldError(key)).length)
const ruleText = computed(() => t('proxyAutoSwitch.rule', {
  window: displayNumber(draft.window_minutes),
  samples: displayNumber(draft.min_samples),
  p60: displayNumber(draft.p60_threshold_s),
  mean: displayNumber(draft.mean_threshold_s),
}))
function displayNumber(value: number | string): string {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? String(parsed) : '—'
}
const configWindow = computed(() => saved.value?.window_minutes ?? defaultConfig.window_minutes)
const groupOptions = computed(() => {
  const known = new Set(groups.value.map(group => group.id))
  const extra = [...new Set([...(saved.value?.group_ids ?? []), ...draft.group_ids])].filter(id => !known.has(id)).map(id => ({ id, name: `#${id}` }))
  return [...groups.value, ...extra]
})

function applyConfig(raw: ProxyAutoSwitchConfig) {
  saved.value = canonical(normalizeConfig(raw))
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
  const payload = payloadForSave()
  try {
    const config = await proxyAutoSwitchAPI.updateConfig(payload)
    if (disposed) return
    applyConfig(config ?? payload)
    notice.value = t('proxyAutoSwitch.saved')
    // 窗口和监控状态都以服务端为准：保存成功后重拉监控和代理列表，失败时不重拉。
    await Promise.all([loadAccounts(), loadProxies()])
  } catch (err) {
    saveError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.saveFailed'))
  } finally { saving.value = false }
}
function discard() {
  if (saved.value) Object.assign(draft, toDraft(saved.value))
  saveError.value = ''
}

// 代理池：人工从 IP 管理里选进来的代理（draft.candidate_proxy_ids）。
// 表里按后端返回的顺序排（池内按延迟排名），本地新加、还没保存的排最后，显示「待保存」。
const proxies = ref<ProxyAutoSwitchProxy[]>([]), proxiesLoading = ref(false), proxiesLoaded = ref(false), proxiesError = ref('')
function isExpired(proxy: ProxyAutoSwitchProxy) {
  if (proxy.expired) return true
  const date = validDate(proxy.expires_at)
  return !!date && date.getTime() < Date.now()
}
function isUsable(proxy: ProxyAutoSwitchProxy) { return proxy.status === 'active' && !isExpired(proxy) }
const proxyById = computed(() => new Map(proxies.value.map(proxy => [proxy.id, proxy])))
const poolIds = computed(() => new Set(draft.candidate_proxy_ids))
const savedPoolIds = computed(() => new Set(saved.value?.candidate_proxy_ids ?? []))
// 池里的代理：已保存的在前（后端顺序 = 延迟排名），新加的在后；IP 管理里已删除的单独列在最后。
const poolRows = computed(() => {
  const listed = proxies.value.filter(proxy => poolIds.value.has(proxy.id))
  const savedRows = listed.filter(proxy => savedPoolIds.value.has(proxy.id))
  const added = draft.candidate_proxy_ids.map(id => proxyById.value.get(id)).filter((proxy): proxy is ProxyAutoSwitchProxy => !!proxy && !savedPoolIds.value.has(proxy.id))
  return [...savedRows, ...added]
})
const missingPoolIds = computed(() => (proxiesLoaded.value ? draft.candidate_proxy_ids.filter(id => !proxyById.value.has(id)) : []))
// 排名只给已保存、可用的代理编号；停用 / 过期 / 新加没保存的显示「—」。
const poolRank = computed(() => {
  const rank = new Map<number, number>()
  let n = 0
  for (const proxy of poolRows.value) {
    if (savedPoolIds.value.has(proxy.id) && isUsable(proxy)) rank.set(proxy.id, ++n)
  }
  return rank
})
function poolNote(proxy: ProxyAutoSwitchProxy): string {
  if (proxy.status !== 'active') return t('proxyAutoSwitch.poolInactive')
  if (isExpired(proxy)) return t('proxyAutoSwitch.expired')
  if (!savedPoolIds.value.has(proxy.id)) return t('proxyAutoSwitch.poolUnsaved')
  if (isFull(proxy)) return t('proxyAutoSwitch.full')
  return ''
}
// 「已满」按已保存的上限算，和后端换代理时用的一致。
const savedMax = computed(() => saved.value?.max_accounts_per_proxy ?? defaultConfig.max_accounts_per_proxy)
function isFull(proxy: ProxyAutoSwitchProxy) { return proxy.account_count >= savedMax.value }
function proxyLabel(proxy: ProxyAutoSwitchProxy) { return proxy.name ? `#${proxy.id} ${proxy.name}` : `#${proxy.id}` }
function removeFromPool(id: number) { draft.candidate_proxy_ids = draft.candidate_proxy_ids.filter(candidate => candidate !== id) }
function addToPool(ids: number[]) {
  const next = [...draft.candidate_proxy_ids]
  for (const id of ids) if (!next.includes(id)) next.push(id)
  draft.candidate_proxy_ids = next
  pickerOpen.value = false
}
// 停用、过期的代理一键移出（只改草稿，点保存才生效）。
const unusablePoolCount = computed(() => poolRows.value.filter(proxy => !isUsable(proxy)).length + missingPoolIds.value.length)
function removeUnusable() {
  const drop = new Set([...poolRows.value.filter(proxy => !isUsable(proxy)).map(proxy => proxy.id), ...missingPoolIds.value])
  draft.candidate_proxy_ids = draft.candidate_proxy_ids.filter(id => !drop.has(id))
}
const pickerOpen = ref(false)
async function loadProxies() {
  proxiesLoading.value = true; proxiesError.value = ''
  try {
    const items = await proxyAutoSwitchAPI.listProxies()
    if (!disposed) {
      proxies.value = items
      proxiesLoaded.value = true
    }
  } catch (err) {
    if (!disposed) proxiesError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.proxiesLoadFailed'))
  } finally { proxiesLoading.value = false }
}

// 保存时去掉 IP 管理里已经删掉的代理（否则后端校验会拒绝）；代理列表没加载成功就原样带上，别把池子清空。
function payloadForSave(): ProxyAutoSwitchUpdate {
  const payload = toPayload()
  if (proxiesLoaded.value) payload.candidate_proxy_ids = payload.candidate_proxy_ids.filter(id => proxyById.value.has(id))
  return payload
}

// 账号监控：窗口以接口返回为准，没返回时退回已保存配置。
const accounts = ref<ProxyAutoSwitchAccount[]>([]), accountsWindow = ref(0)
const accountsLoading = ref(false), accountsLoaded = ref(false), accountsError = ref('')
const monitorWindow = computed(() => accountsWindow.value || configWindow.value)
const monitorMinSamples = computed(() => saved.value?.min_samples ?? defaultConfig.min_samples)
const accountTones: Record<string, string> = { ok: 'state-good', insufficient: 'state-muted', slow: 'state-bad', unavailable: 'state-muted', no_proxy: 'state-warn' }
function accountStateTone(state: string) { return accountTones[state] ?? 'state-muted' }
function accountStateLabel(account: ProxyAutoSwitchAccount) {
  const key = `proxyAutoSwitch.monitorState.${account.state}`
  return te(key) ? t(key, { samples: monitorMinSamples.value }) : account.state
}
async function loadAccounts() {
  accountsLoading.value = true; accountsError.value = ''
  try {
    const page = await proxyAutoSwitchAPI.listAccounts()
    if (disposed) return
    accounts.value = page.items ?? []
    accountsWindow.value = page.window_minutes || 0
    accountsLoaded.value = true
  } catch (err) {
    if (!disposed) accountsError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.accountsLoadFailed'))
  } finally { accountsLoading.value = false }
}

// 换代理记录：分页和刷新沿用原来的 cursor 做法。
const events = ref<ProxyAutoSwitchEvent[]>([]), cursor = ref(0)
const eventsLoading = ref(false), eventsLoaded = ref(false), moreLoading = ref(false), eventsError = ref(''), lastEventsFailure = ref<'list' | 'more'>('list')
let eventsSeq = 0

async function loadEvents() {
  const seq = ++eventsSeq
  eventsLoading.value = true; moreLoading.value = false; eventsError.value = ''
  try {
    const page = await proxyAutoSwitchAPI.listEvents({ before_id: 0, limit: PAGE_SIZE })
    if (seq !== eventsSeq || disposed) return
    events.value = page.items; cursor.value = page.next_cursor; eventsLoaded.value = true; eventPage.value = 1
  } catch (err) {
    if (seq === eventsSeq && !disposed) { eventsError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.eventsLoadFailed')); lastEventsFailure.value = 'list' }
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
    if (seq === eventsSeq && !disposed) { eventsError.value = extractApiErrorMessage(err, t('proxyAutoSwitch.eventsLoadFailed')); lastEventsFailure.value = 'more' }
  } finally { if (seq === eventsSeq) moreLoading.value = false }
}
function retryEvents() { return lastEventsFailure.value === 'more' && events.value.length ? loadMore() : loadEvents() }

// 换代理记录前端分页：接口按 cursor 每次拉 PAGE_SIZE 条，翻到已加载的末尾且后端还有就再拉一批。
// 后端不返回总数，没拉完之前只显示页码，拉完了才显示「共 N 条」和总页数。
const eventPageSizes = [10, 20, 50]
const EVENT_PAGE_SIZE_KEY = 'pas.eventPageSize'
function readEventPageSize() {
  try { const v = Number(localStorage.getItem(EVENT_PAGE_SIZE_KEY)); return eventPageSizes.includes(v) ? v : 10 } catch { return 10 }
}
const eventPageSize = ref(readEventPageSize())
const eventPage = ref(1)
const eventPages = computed(() => Math.max(1, Math.ceil(events.value.length / eventPageSize.value)))
const pagedEvents = computed(() => events.value.slice((eventPage.value - 1) * eventPageSize.value, eventPage.value * eventPageSize.value))
const eventPageFrom = computed(() => (events.value.length ? (eventPage.value - 1) * eventPageSize.value + 1 : 0))
const eventPageTo = computed(() => Math.min(eventPage.value * eventPageSize.value, events.value.length))
const hasNextEventPage = computed(() => eventPage.value * eventPageSize.value < events.value.length || !!cursor.value)
async function nextEventPage() {
  if (eventPage.value * eventPageSize.value >= events.value.length && cursor.value) await loadMore()
  if (eventPage.value * eventPageSize.value < events.value.length) eventPage.value++
}
watch(eventPageSize, size => {
  eventPage.value = 1
  try { localStorage.setItem(EVENT_PAGE_SIZE_KEY, String(size)) } catch { /* ignore */ }
})

const legacyStatuses: ProxyAutoSwitchStatus[] = ['observing', 'kept', 'kept_insufficient', 'not_faster', 'reverted', 'revert_skipped', 'manual_revert']
const statusTones: Record<string, string> = { measuring: 'tone-info', done: 'tone-success', no_candidate: 'tone-neutral', failed: 'tone-danger', observing: 'tone-info', kept: 'tone-success', kept_insufficient: 'tone-success', not_faster: 'tone-warning', reverted: 'tone-warning', revert_skipped: 'tone-warning', manual_revert: 'tone-muted' }
function isLegacy(event: ProxyAutoSwitchEvent) { return legacyStatuses.includes(event.status) }
function statusTone(status: ProxyAutoSwitchStatus) { return statusTones[status] ?? 'tone-neutral' }
function statusLabel(status: ProxyAutoSwitchStatus) {
  const key = `proxyAutoSwitch.status.${status}`
  return te(key) ? t(key) : status
}
function strategyLabel(strategy: ProxyAutoSwitchStrategy | null) {
  if (!strategy) return '—'
  const key = `proxyAutoSwitch.strategyLabel.${strategy}`
  return te(key) ? t(key) : '—'
}
function eventAt(event: ProxyAutoSwitchEvent) { return event.switched_at || event.created_at }
function changeFor(event: ProxyAutoSwitchEvent, key: 'p60_ms' | 'mean_ms') {
  const before = event.before?.[key] ?? null
  const after = event.after?.[key] ?? null
  if (before == null || after == null) return { text: '—', tone: '' }
  const diff = before - after
  if (diff === 0) return { text: '—', tone: '' }
  return { text: `${diff > 0 ? '↓' : '↑'} ${seconds(Math.abs(diff))}`, tone: diff > 0 ? 'change-good' : 'change-bad' }
}
function probeText(ms: number | null, event: ProxyAutoSwitchEvent) {
  // no_candidate 从来没换、也没测过速，null 不代表测不通。
  if (ms == null) return isLegacy(event) || event.status === 'no_candidate' ? '—' : t('proxyAutoSwitch.probeUnreachable')
  return `${Math.round(ms)}ms`
}
function proxyText(id: number | null, label: string | null) {
  if (id == null) return label || '—'
  return label ? `#${id} ${label}` : `#${id}`
}
function seconds(ms: number | null | undefined) { return ms == null || !Number.isFinite(ms) ? '—' : `${(ms / 1000).toFixed(1)}s` }
function validDate(value: string | null | undefined) { const date = value ? new Date(value) : null; return date && !Number.isNaN(date.getTime()) ? date : null }
function pad(value: number) { return String(value).padStart(2, '0') }
function clock(value: string) { const date = validDate(value); return date ? date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : '—' }
function day(value: string) { const date = validDate(value); return date ? `${pad(date.getMonth() + 1)}-${pad(date.getDate())}` : '' }
function monthDay(value: string | null | undefined) { const date = validDate(value); return date ? `${pad(date.getMonth() + 1)}-${pad(date.getDate())}` : '—' }
function lastSwitch(value: string | null | undefined) { const date = validDate(value); return date ? `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}` : '—' }
function fullTime(value: string) { return validDate(value)?.toLocaleString() ?? '' }

onMounted(() => { void loadConfig(); void loadGroups(); void loadProxies(); void loadAccounts(); void loadEvents() })
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
.inline-error { @apply flex items-center justify-between gap-3 rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-950/40 dark:text-red-300; overflow-wrap: anywhere; }
.inline-error button { @apply shrink-0 underline; }
.skeleton-stack { @apply space-y-3; }
.cell-skeleton { @apply block h-3 rounded bg-gray-100 dark:bg-dark-700; animation: proxy-pulse 1.7s ease-in-out infinite; }

.settings-body { @apply space-y-4 border-t border-gray-100 px-5 py-5 dark:border-dark-700; }
.settings-row { @apply flex flex-wrap items-start gap-x-6 gap-y-3; }
.inline-field { @apply flex flex-wrap items-center gap-2 text-sm text-gray-600 dark:text-gray-300; }
.inline-field > .field-error { @apply w-full; }
.groups-field { @apply min-w-0 flex-1 flex-col items-start; }
.threshold-row { @apply flex flex-wrap items-center gap-x-5 gap-y-3; }
.field-label { @apply text-xs font-medium text-gray-500 dark:text-gray-400; }
.field-hint { @apply text-xs leading-5 text-gray-400 dark:text-gray-500; }
.field-warn { @apply text-amber-600 dark:text-amber-400; }
.field-error { @apply text-xs text-red-600 dark:text-red-400; }
.num-input { width: 6.5rem; @apply rounded-lg px-3 py-1.5 text-base font-semibold tabular-nums; }
.num-input[aria-invalid='true'] { @apply border-red-400 focus:border-red-500 focus:ring-red-500/30 dark:border-red-700; }
.switch-chip { @apply inline-flex items-center gap-1.5 rounded-full border border-gray-200 bg-white px-3 py-1 text-xs font-medium text-gray-600 transition-colors dark:border-dark-600 dark:bg-dark-900 dark:text-gray-300; }
.switch-chip.on { @apply border-primary-300 bg-primary-50 text-primary-700 dark:border-primary-700 dark:bg-primary-950/40 dark:text-primary-300; }
.switch-dot { @apply h-1.5 w-1.5 rounded-full bg-current; }
.chips { @apply flex flex-wrap gap-2; }
.chip { @apply inline-flex max-w-full cursor-pointer items-center rounded-full border border-gray-200 bg-white px-3 py-1 text-xs text-gray-600 transition-colors hover:border-gray-300 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-300 dark:hover:border-dark-500; overflow-wrap: anywhere; }
.chip.on { @apply border-primary-300 bg-primary-50 font-medium text-primary-700 dark:border-primary-700 dark:bg-primary-950/40 dark:text-primary-300; }
.chip:focus-within { @apply ring-2 ring-primary-400 ring-offset-2 dark:ring-offset-dark-900; }
.rule { @apply rounded-lg border border-dashed border-gray-200 bg-gray-50/70 px-3 py-2 text-[13px] leading-6 text-gray-600 dark:border-dark-600 dark:bg-dark-800/50 dark:text-gray-300; }
.subsection-heading { @apply flex items-start justify-between gap-3; }
.subsection-heading h4 { @apply text-sm font-semibold; }
.subsection-heading { @apply flex-wrap; }
.pool-strategy { @apply ml-auto flex shrink-0 items-center gap-2; }
.pool-actions { @apply mt-2 flex flex-wrap items-center gap-2; }
.pool-actions .btn { @apply inline-flex items-center gap-1; }
.proxy-table .action-cell { width: 4.5rem; }
.link-button { @apply text-xs text-gray-500 underline-offset-2 hover:text-rose-600 hover:underline dark:text-gray-400 dark:hover:text-rose-400; }
.proxy-table .rank-cell { width: 5.5rem; font-variant-numeric: tabular-nums; }
.pager { @apply flex flex-wrap items-center gap-2; }
.pager-size { @apply inline-flex items-center gap-1.5; }
.pager-select { @apply rounded-md border border-gray-200 bg-white px-1.5 py-0.5 text-[11px] text-gray-600 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-300; }
.operations-footer .pager-btn { @apply rounded-md border border-gray-200 px-2.5 py-1 text-gray-600 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-800; }
.pager-num { @apply tabular-nums text-gray-500 dark:text-gray-400; }

.table-scroll { @apply min-w-0 overflow-x-auto border-t border-gray-100 dark:border-dark-700; }
.data-table { @apply w-full border-collapse text-left text-xs; }
.data-table th { @apply whitespace-nowrap border-b border-gray-100 px-3 py-2.5 font-medium text-gray-400 dark:border-dark-800 dark:text-gray-500; }
.data-table td { @apply border-b border-gray-100 px-3 py-3 align-middle dark:border-dark-800; }
.data-table .tabular-nums { @apply text-right; font-variant-numeric: tabular-nums; }
.data-table th:not(.cell-left) { @apply text-right; }
.data-table .cell-left { @apply text-left; }
.data-table .group-row th { @apply border-b-0 pb-1 text-left text-[11px] text-gray-500 dark:text-gray-400; }
.proxy-table td, .accounts-table td, .events-table td { @apply whitespace-nowrap; }
.proxy-name-cell { @apply font-mono text-[11px]; }
.proxy-checkbox { @apply h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500; }
.row-expired td { @apply bg-gray-50/60 text-gray-400 dark:bg-dark-800/30 dark:text-gray-500; }
.expired-tag { @apply ml-2 rounded bg-gray-100 px-1.5 py-0.5 font-sans text-[10px] text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.table-empty { @apply py-8 text-center text-gray-400 dark:text-gray-500; }
.table-empty span { @apply mt-1 block text-[11px]; }
.pool-order { @apply rounded-xl border border-gray-200 bg-gray-50/60 p-3 dark:border-dark-700 dark:bg-dark-800/50; }
.pool-order-head { @apply flex flex-wrap items-baseline gap-2; }
.pool-order-list { @apply mt-2 flex flex-wrap gap-2; }
.pool-order-item { @apply inline-flex items-center gap-2 rounded-lg border border-gray-200 bg-white px-2.5 py-1.5 text-xs dark:border-dark-600 dark:bg-dark-900; }
.pool-order-name { @apply font-mono text-[11px]; }
.order-actions { @apply flex items-center gap-1; }
.order-button { @apply flex h-6 w-6 items-center justify-center rounded border border-gray-200 text-gray-500 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-400 dark:hover:bg-dark-800; }
.strategy-group { @apply inline-flex flex-wrap gap-1 rounded-xl border border-gray-200 bg-gray-50 p-1 dark:border-dark-700 dark:bg-dark-800; }
.strategy-group button { @apply rounded-lg px-3 py-1.5 text-xs text-gray-500 transition-colors hover:bg-white dark:text-gray-400 dark:hover:bg-dark-700; }
.strategy-group button.on { @apply bg-gray-900 font-medium text-white hover:bg-gray-900 dark:bg-gray-100 dark:text-gray-900 dark:hover:bg-gray-100; }
.settings-footer { @apply flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 pt-4 dark:border-dark-700; }
.footer-actions { @apply ml-auto flex flex-wrap items-center gap-2; }
.footer-state { @apply text-xs text-amber-600 dark:text-amber-400; }
.footer-state.is-error { @apply text-red-600 dark:text-red-400; }
.form-error { @apply rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300; overflow-wrap: anywhere; }

.accounts-table, .events-table { min-width: 980px; }
.events-table { min-width: 1280px; }
.account-name strong { @apply block text-xs font-medium; }
.account-name span { @apply mt-1 block max-w-[220px] truncate text-[10px] text-gray-400; }
.region-tag { @apply ml-1 rounded bg-sky-50 px-1 py-0.5 font-sans text-[10px] font-medium text-sky-700 dark:bg-sky-950/40 dark:text-sky-300; }
.state-pill { @apply inline-flex items-center rounded-full border border-current px-2 py-0.5 text-[11px] font-medium; }
.state-good { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300; }
.state-bad { @apply bg-rose-50 text-rose-600 dark:bg-rose-950/40 dark:text-rose-300; }
.state-warn { @apply bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300; }
.state-muted { @apply bg-gray-100 text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.cell-note { @apply mt-1 block max-w-[220px] whitespace-normal text-[10px] text-gray-400; }
.time-cell strong { @apply block text-xs font-medium; }
.time-cell > span { @apply mt-1 block text-[10px] text-gray-400; }
.account-cell { @apply max-w-[220px]; }
.account-cell strong { @apply block text-xs font-medium; }
.account-cell > span:not(.status-badge) { @apply mt-1 block max-w-[200px] truncate text-[10px] text-gray-400; }
.proxy-cell { @apply whitespace-normal font-mono text-[11px]; }
.proxy-line { @apply inline-flex flex-wrap items-center gap-1.5; }
.proxy-arrow { @apply mx-1.5 text-gray-400; }
.reason-text { @apply mt-1 block max-w-[420px] whitespace-normal text-[10px] text-gray-400; }
.status-badge { @apply mt-1 inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium; }
.tone-success { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300; }
.tone-warning { @apply bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300; }
.tone-info { @apply bg-sky-50 text-sky-700 dark:bg-sky-950/40 dark:text-sky-300; }
.tone-muted, .tone-neutral { @apply bg-gray-100 text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.tone-danger { @apply bg-rose-50 text-rose-600 dark:bg-rose-950/40 dark:text-rose-300; }
.change-good { @apply font-medium text-emerald-600 dark:text-emerald-400; }
.change-bad { @apply font-medium text-rose-600 dark:text-rose-400; }
.measuring-cell { @apply text-left text-gray-500 dark:text-gray-400; }
.operations-footer { @apply flex min-h-14 shrink-0 flex-wrap items-center justify-between gap-3 border-t border-gray-100 px-5 py-3 text-[11px] text-gray-400 dark:border-dark-700; }
.operations-footer button { @apply inline-flex items-center gap-1.5 text-primary-600 dark:text-primary-400; }
button:disabled { @apply cursor-not-allowed opacity-40; }
button:focus-visible, input:focus-visible, select:focus-visible { @apply outline-none ring-2 ring-primary-400 ring-offset-2 dark:ring-offset-dark-900; }
@keyframes proxy-pulse { 50% { opacity: .45; } }

@media (max-width: 767px) {
  .panel-heading, .settings-body, .operations-footer { @apply px-4; }
  .panel-error { @apply mx-4; }
}
@media (prefers-reduced-motion: reduce) { .cell-skeleton { animation: none; } }
</style>
