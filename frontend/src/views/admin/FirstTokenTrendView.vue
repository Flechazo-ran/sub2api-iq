<template>
  <AppLayout>
    <div class="ftt-workspace">
      <RoutingNav />
      <div class="workspace-heading">
        <div><p class="workspace-eyebrow">{{ t('nav.routing') }}</p><h2>{{ t('firstTokenTrend.title') }}</h2><p class="workspace-subtitle">{{ t('firstTokenTrend.intro') }}</p></div>
        <div class="heading-actions">
          <span v-if="loadedAt" class="updated-at">{{ t('firstTokenTrend.updatedAt', { time: loadedAt }) }}</span>
          <button type="button" class="icon-button" :disabled="loading" :aria-label="t('firstTokenTrend.refresh')" data-testid="ftt-refresh" @click="manualRefresh"><Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" /></button>
        </div>
      </div>

      <section class="workspace-panel filter-panel" data-testid="ftt-filters">
        <div class="fgroup">
          <label class="flabel" for="ftt-group">{{ t('firstTokenTrend.filters.group') }}</label>
          <select id="ftt-group" v-model.number="groupId" class="input group-select" data-testid="ftt-group">
            <option v-for="group in groupOptions" :key="group.id" :value="group.id">{{ group.name }} #{{ group.id }}</option>
          </select>
          <span v-if="groupsError" class="field-hint text-amber-600 dark:text-amber-400" data-testid="ftt-groups-error">{{ t('firstTokenTrend.filters.groupsLoadFailed') }}</span>
        </div>
        <div class="fgroup">
          <span id="ftt-range-label" class="flabel">{{ t('firstTokenTrend.filters.range') }}</span>
          <div class="seg" role="radiogroup" aria-labelledby="ftt-range-label">
            <button v-for="option in ranges" :key="option" type="button" role="radio" :aria-checked="range === option" :class="{ on: range === option }" :data-testid="`ftt-range-${option}`" @click="range = option">{{ t(`firstTokenTrend.range.${option}`) }}</button>
          </div>
          <span v-if="stepText" class="flabel" data-testid="ftt-step">{{ stepText }}</span>
        </div>
        <div class="fgroup">
          <span id="ftt-metric-label" class="flabel">{{ t('firstTokenTrend.filters.metric') }}</span>
          <div class="seg" role="radiogroup" aria-labelledby="ftt-metric-label">
            <button v-for="option in metrics" :key="option" type="button" role="radio" :aria-checked="metric === option" :class="{ on: metric === option }" :data-testid="`ftt-metric-${option}`" @click="metric = option">{{ t(`firstTokenTrend.metric.${option}`) }}</button>
          </div>
        </div>
        <div class="fgroup">
          <span class="flabel">{{ t('firstTokenTrend.filters.series') }}</span>
          <button v-for="key in seriesKeys" :key="key" type="button" class="chip" :aria-pressed="shown[key]" :data-testid="`ftt-series-${key}`" @click="toggleSeries(key)"><i :class="seriesSwatch[key]" />{{ t(`firstTokenTrend.series.${key}`) }}</button>
        </div>
        <div class="fgroup">
          <button type="button" role="switch" class="refresh-toggle" :aria-checked="autoRefresh.enabled.value" data-testid="ftt-auto-refresh" @click="autoRefresh.setEnabled(!autoRefresh.enabled.value)"><span class="toggle-track"><span class="toggle-thumb" /></span>{{ t('firstTokenTrend.filters.autoRefresh') }}</button>
          <span class="flabel">{{ t('firstTokenTrend.filters.autoRefreshHint') }}</span>
        </div>
      </section>

      <div v-if="error" role="alert" class="panel-error" data-testid="ftt-error">{{ error }}<button type="button" :disabled="loading" data-testid="ftt-retry" @click="manualRefresh">{{ t('firstTokenTrend.retry') }}</button></div>

      <div v-if="!data && loading" class="skeleton-stack" :aria-label="t('firstTokenTrend.loading')" data-testid="ftt-loading">
        <div class="kpis"><span v-for="n in 6" :key="n" class="kpi-skeleton" /></div>
        <span class="chart-skeleton" />
        <span class="chart-skeleton short" />
      </div>

      <template v-else-if="data">
        <div v-if="isEmpty" class="workspace-panel panel-empty" data-testid="ftt-empty"><Icon name="chartBar" size="lg" /><h4>{{ t('firstTokenTrend.empty') }}</h4><p>{{ t('firstTokenTrend.emptyHint') }}</p></div>

        <template v-else>
          <p class="note" data-testid="ftt-kpi-note">{{ kpiNote }}</p>
          <div class="kpis" data-testid="ftt-kpis">
            <div v-for="kpi in kpis" :key="kpi.key" class="kpi" :data-testid="`ftt-kpi-${kpi.key}`">
              <span class="k">{{ kpi.label }}</span>
              <span class="v num">{{ kpi.value }}</span>
              <span class="d num" :class="kpi.tone">{{ kpi.delta }}</span>
            </div>
          </div>
          <section class="workspace-panel card" aria-labelledby="ftt-trend-heading">
            <header class="card-h"><h3 id="ftt-trend-heading">{{ t('firstTokenTrend.trend.title') }}</h3><span class="sub" data-testid="ftt-metric-hint">{{ t(`firstTokenTrend.metricHint.${metric}`) }}</span></header>
            <div class="legend">
              <span v-for="type in eventTypes" :key="type"><i class="dot" :class="eventSwatch[type]" />{{ t(`firstTokenTrend.trend.${type}`) }}</span>
              <span><i class="dash border-red-600 dark:border-red-400" />{{ t('firstTokenTrend.trend.threshold') }}</span>
            </div>
            <div class="chart-box trend-box" data-testid="ftt-trend-chart">
              <Line :data="trendData" :options="trendOptions" :plugins="trendPlugins" />
              <div v-if="hoveredEvent" class="event-tip" :style="{ left: `${hoveredEvent.x}px` }" role="tooltip" data-testid="ftt-event-tip">
                <strong>{{ clock(hoveredEvent.event.at, true) }} · {{ t(`firstTokenTrend.events.type.${hoveredEvent.event.type}`) }}</strong>
                <span>#{{ hoveredEvent.event.account_id }} {{ hoveredEvent.event.account_name }}</span>
                <span>{{ hoveredEvent.event.detail }}</span>
                <span v-if="hoveredEvent.event.actor">{{ t('firstTokenTrend.events.by', { actor: hoveredEvent.event.actor }) }}</span>
              </div>
            </div>
          </section>

          <section class="workspace-panel card" aria-labelledby="ftt-conc-heading">
            <header class="card-h"><h3 id="ftt-conc-heading">{{ t('firstTokenTrend.concurrency.title') }}</h3><span class="sub">{{ t('firstTokenTrend.concurrency.sub') }}</span></header>
            <div class="legend">
              <span><i class="line bg-sky-500 dark:bg-sky-400" />{{ t('firstTokenTrend.concurrency.inflight') }}</span>
              <span><i class="dash border-violet-500 dark:border-violet-400" />{{ t('firstTokenTrend.concurrency.limit') }}</span>
              <span v-if="data.limit_is_current" class="limit-note" data-testid="ftt-limit-current">{{ t('firstTokenTrend.concurrency.limitIsCurrent') }}</span>
            </div>
            <div class="chart-box conc-box" data-testid="ftt-conc-chart"><Line :data="concData" :options="concOptions" :plugins="concPlugins" /></div>
          </section>

          <section class="workspace-panel card" aria-labelledby="ftt-split-heading">
            <header class="card-h"><h3 id="ftt-split-heading">{{ t('firstTokenTrend.split.title') }}</h3><span class="sub">{{ t('firstTokenTrend.split.sub') }}</span></header>
            <div class="legend">
              <span v-for="seg in splitSegments" :key="seg.key"><i :class="seg.swatch" />{{ t(`firstTokenTrend.split.${seg.key}`) }}</span>
            </div>
            <div class="chart-box split-box" data-testid="ftt-split-chart"><Bar :data="splitData" :options="splitOptions" /></div>
          </section>
        </template>
        <section v-if="!isEmpty || sortedEvents.length" class="workspace-panel card" aria-labelledby="ftt-events-heading">
          <header class="card-h"><h3 id="ftt-events-heading">{{ t('firstTokenTrend.events.title') }}</h3><span class="sub">{{ t('firstTokenTrend.events.sub') }}</span></header>
          <div class="table-scroll">
            <table class="data-table" data-testid="ftt-events">
              <thead><tr><th class="l">{{ t('firstTokenTrend.events.colTime') }}</th><th class="l">{{ t('firstTokenTrend.events.colAccount') }}</th><th class="l">{{ t('firstTokenTrend.events.colType') }}</th><th class="l">{{ t('firstTokenTrend.events.colChange') }}</th><th class="l">{{ t('firstTokenTrend.events.colActor') }}</th><th>{{ t('firstTokenTrend.events.colBefore') }}</th><th>{{ t('firstTokenTrend.events.colAfter') }}</th><th class="l">{{ t('firstTokenTrend.events.colEffect') }}</th></tr></thead>
              <tbody>
                <tr v-for="(event, index) in sortedEvents" :key="`${event.at}-${event.type}-${event.account_id}-${index}`" data-testid="ftt-event-row">
                  <td class="l num">{{ clock(event.at, true) }}</td>
                  <td class="l"><span class="acct">#{{ event.account_id }}</span><small v-if="event.account_name" class="muted">{{ event.account_name }}</small></td>
                  <td class="l"><span class="tag"><i class="dot" :class="eventSwatch[event.type]" />{{ t(`firstTokenTrend.events.type.${event.type}`) }}</span></td>
                  <td class="l wrap">{{ event.detail }}</td>
                  <td class="l">{{ event.actor || '—' }}</td>
                  <td class="num">{{ sec(event.p50_before_ms) }}</td>
                  <td class="num">{{ sec(event.p50_after_ms) }}</td>
                  <td class="l"><span class="effect" :class="effectOf(event).tone" data-testid="ftt-effect">{{ effectOf(event).text }}</span></td>
                </tr>
                <tr v-if="!sortedEvents.length"><td colspan="8" class="l muted" data-testid="ftt-events-empty">{{ t('firstTokenTrend.events.empty') }}</td></tr>
              </tbody>
            </table>
          </div>
        </section>

        <section v-if="!isEmpty" class="workspace-panel card" aria-labelledby="ftt-dims-heading">
          <header class="card-h">
            <h3 id="ftt-dims-heading">{{ t('firstTokenTrend.dims.title') }}</h3>
            <div class="seg" role="radiogroup" aria-labelledby="ftt-dims-heading">
              <button v-for="option in dimensions" :key="option" type="button" role="radio" :aria-checked="dimension === option" :class="{ on: dimension === option }" :data-testid="`ftt-dim-${option}`" @click="dimension = option">{{ t(`firstTokenTrend.dims.${option}`) }}</button>
            </div>
          </header>
          <div class="table-scroll" :aria-busy="loading">
            <table class="data-table" data-testid="ftt-rows">
              <thead><tr><th class="l">{{ t('firstTokenTrend.dims.colName') }}</th><th>{{ t('firstTokenTrend.dims.colSamples') }}</th><th>{{ t('firstTokenTrend.dims.colAvg') }}</th><th>{{ t('firstTokenTrend.dims.colP50') }}</th><th>{{ t('firstTokenTrend.dims.colP80') }}</th><th>{{ t('firstTokenTrend.dims.colOver10') }}</th><th>{{ t('firstTokenTrend.dims.colInflight') }}</th><th class="l">{{ t('firstTokenTrend.dims.colSpark') }}</th><th class="l">{{ t('firstTokenTrend.dims.colStatus') }}</th></tr></thead>
              <tbody>
                <tr v-for="row in data.rows" :key="row.key" data-testid="ftt-row">
                  <td class="l name"><span>{{ row.label }}</span><small v-if="row.sub" class="muted">{{ row.sub }}</small></td>
                  <td class="num">{{ row.samples.toLocaleString() }}</td>
                  <template v-if="row.samples > 0">
                    <td class="num">{{ sec(row.avg_ms) }}</td>
                    <td class="num">{{ sec(row.p50_ms) }}</td>
                    <td class="num">{{ sec(row.p80_ms) }}</td>
                    <td class="num">{{ pct(row.over10_pct) }}</td>
                    <td class="num">{{ fixed1(row.inflight_avg) }}</td>
                    <td class="l"><svg v-if="sparks[row.key]" class="spark" width="120" height="28" viewBox="0 0 120 28" aria-hidden="true" data-testid="ftt-spark"><line x1="0" x2="120" :y1="sparks[row.key]!.thrY" :y2="sparks[row.key]!.thrY" class="spark-thr" /><polyline v-for="(points, i) in sparks[row.key]!.segments" :key="i" :points="points" class="spark-line" /><circle :cx="sparks[row.key]!.last[0]" :cy="sparks[row.key]!.last[1]" r="2.5" class="spark-dot" /></svg><span v-else class="muted">—</span></td>
                  </template>
                  <td v-else colspan="6" class="l muted">{{ t('firstTokenTrend.dims.noRowData') }}</td>
                  <td class="l"><span class="pill" :class="statusOf(row).tone" data-testid="ftt-row-status">{{ statusOf(row).text }}</span></td>
                </tr>
                <tr v-if="!data.rows.length"><td colspan="9" class="l muted" data-testid="ftt-rows-empty">{{ t('firstTokenTrend.dims.empty') }}</td></tr>
              </tbody>
            </table>
          </div>
          <p class="note">{{ t('firstTokenTrend.dims.note') }}</p>
        </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { BarElement, CategoryScale, Chart as ChartJS, Filler, LinearScale, LineElement, PointElement, Tooltip, type ChartData, type ChartOptions, type Plugin, type TooltipItem } from 'chart.js'
import { Bar, Line } from 'vue-chartjs'
import AppLayout from '@/components/layout/AppLayout.vue'
import RoutingNav from '@/components/admin/operations/RoutingNav.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAutoRefresh } from '@/composables/useAutoRefresh'
import { firstTokenTrendAPI, type FirstTokenTrendBucket, type FirstTokenTrendData, type FirstTokenTrendDimension, type FirstTokenTrendEvent, type FirstTokenTrendEventType, type FirstTokenTrendGroup, type FirstTokenTrendMetric, type FirstTokenTrendRange, type FirstTokenTrendRow } from '@/api/admin/firstTokenTrend'
import { extractApiErrorMessage } from '@/utils/apiError'

ChartJS.register(CategoryScale, LinearScale, LineElement, PointElement, BarElement, Tooltip, Filler)

const { t, te } = useI18n()

type SeriesKey = 'avg' | 'p50' | 'p80'
const DEFAULT_GROUP_ID = 16
const SLOW_MS = 10000
const ranges: FirstTokenTrendRange[] = ['30m', '1h', '2h']
const metrics: FirstTokenTrendMetric[] = ['usage', 'switch']
const dimensions: FirstTokenTrendDimension[] = ['account', 'proxy', 'model']
const seriesKeys: SeriesKey[] = ['avg', 'p50', 'p80']
const eventTypes: FirstTokenTrendEventType[] = ['proxy', 'concurrency', 'status']
// 色块用 Tailwind 色板（与 palette 里的十六进制一一对应），深浅色各一套。
const seriesSwatch: Record<SeriesKey, string> = { avg: 'bg-blue-600 dark:bg-blue-400', p50: 'bg-yellow-600 dark:bg-yellow-400', p80: 'bg-orange-600 dark:bg-orange-400' }
const eventSwatch: Record<FirstTokenTrendEventType, string> = { proxy: 'bg-primary-600 dark:bg-primary-400', concurrency: 'bg-violet-500 dark:bg-violet-400', status: 'bg-red-600 dark:bg-red-400' }
const splitSegments = [
  { key: 'head', field: 'head_p50_ms', swatch: 'bg-slate-400 dark:bg-slate-500' },
  { key: 'upload', field: 'upload_p50_ms', swatch: 'bg-violet-500 dark:bg-violet-400' },
  { key: 'after', field: 'after_p50_ms', swatch: 'bg-sky-500 dark:bg-sky-400' },
] as const

// 深浅色跟着 <html class="dark"> 走，切换主题时图表跟着重画。
const isDark = ref(document.documentElement.classList.contains('dark'))
let themeObserver: MutationObserver | null = null
const palette = computed(() => isDark.value
  ? { avg: '#60a5fa', p50: '#facc15', p80: '#fb923c', bad: '#f87171', proxy: '#2dd4bf', concurrency: '#a78bfa', status: '#f87171', head: '#64748b', upload: '#a78bfa', after: '#38bdf8', inflight: '#38bdf8', inflightFill: 'rgba(56, 189, 248, 0.14)', grid: '#374151', text: '#9ca3af', tipBg: '#1f2937', tipTitle: '#f3f4f6', tipBody: '#d1d5db' }
  : { avg: '#2563eb', p50: '#ca8a04', p80: '#ea580c', bad: '#dc2626', proxy: '#0d9488', concurrency: '#8b5cf6', status: '#dc2626', head: '#94a3b8', upload: '#8b5cf6', after: '#0ea5e9', inflight: '#0ea5e9', inflightFill: 'rgba(14, 165, 233, 0.12)', grid: '#f3f4f6', text: '#6b7280', tipBg: '#ffffff', tipTitle: '#111827', tipBody: '#4b5563' })

// ---- 筛选 ----
const groupId = ref(DEFAULT_GROUP_ID)
const range = ref<FirstTokenTrendRange>('2h')
const metric = ref<FirstTokenTrendMetric>('usage')
const dimension = ref<FirstTokenTrendDimension>('account')
const shown = reactive<Record<SeriesKey, boolean>>({ avg: true, p50: true, p80: true })
function toggleSeries(key: SeriesKey) {
  // 至少留一条线
  if (shown[key] && seriesKeys.filter(k => shown[k]).length === 1) return
  shown[key] = !shown[key]
}

const groups = ref<FirstTokenTrendGroup[]>([]), groupsError = ref(false)
// 当前选中的分组不在列表里（列表没拉到）也留着显示。
const groupOptions = computed(() => groups.value.some(group => group.id === groupId.value) ? groups.value : [...groups.value, { id: groupId.value, name: data.value?.group.id === groupId.value ? data.value.group.name : '' }])
async function loadGroups() {
  try {
    groups.value = await firstTokenTrendAPI.listGroups()
    groupsError.value = false
  } catch {
    groupsError.value = true
  }
}

// ---- 数据 ----
const data = ref<FirstTokenTrendData | null>(null)
const loading = ref(false), error = ref(''), loadedAt = ref('')
let seq = 0, disposed = false
async function load() {
  const current = ++seq
  loading.value = true; error.value = ''
  try {
    const result = await firstTokenTrendAPI.getTrend({ group_id: groupId.value, range: range.value, metric: metric.value, dimension: dimension.value })
    if (current !== seq || disposed) return
    data.value = result
    loadedAt.value = clock(new Date(), true)
    hoveredEvent.value = null
  } catch (err) {
    if (current === seq && !disposed) error.value = extractApiErrorMessage(err, t('firstTokenTrend.loadFailed'))
  } finally {
    if (current === seq) loading.value = false
  }
}
const autoRefresh = useAutoRefresh({ storageKey: 'admin.firstTokenTrend.autoRefresh', intervals: [60], defaultInterval: 60, defaultEnabled: true, onRefresh: load, shouldPause: () => document.hidden || loading.value })
function manualRefresh() { autoRefresh.resetCountdown(); void load() }
watch([groupId, range, metric, dimension], manualRefresh)

const buckets = computed<FirstTokenTrendBucket[]>(() => data.value?.buckets ?? [])
const stepMs = computed(() => (data.value?.step_seconds ?? 300) * 1000)
const isEmpty = computed(() => !!data.value && !buckets.value.some(bucket => bucket.samples > 0) && !(data.value.rows ?? []).some(row => row.samples > 0))
const stepText = computed(() => {
  const seconds = data.value?.step_seconds
  if (!seconds) return ''
  if (seconds % 3600 === 0) return t('firstTokenTrend.step.hours', { n: seconds / 3600 })
  if (seconds % 60 === 0) return t('firstTokenTrend.step.minutes', { n: seconds / 60 })
  return t('firstTokenTrend.step.seconds', { n: seconds })
})
// ---- 格式 ----
// 后端按北京时间对齐整格，页面上的时间也按北京时间显示。
const bjFormatter = new Intl.DateTimeFormat('en-US', { timeZone: 'Asia/Shanghai', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' })
function bjParts(date: Date) {
  const parts: Record<string, string> = {}
  for (const part of bjFormatter.formatToParts(date)) parts[part.type] = part.value
  return parts
}
function toDate(value: string | Date | null | undefined) {
  const date = value instanceof Date ? value : value ? new Date(value) : null
  return date && !Number.isNaN(date.getTime()) ? date : null
}
function clock(value: string | Date, withSeconds = false) {
  const date = toDate(value)
  if (!date) return '—'
  const p = bjParts(date)
  return withSeconds ? `${p.hour}:${p.minute}:${p.second}` : `${p.hour}:${p.minute}`
}
// x 轴大约每 10 分钟标一个（2 小时 60 格），30 分钟 / 1 小时每 5 分钟标一个；只标落在整格上的时间。
const tickEveryMinutes = computed(() => (range.value === '2h' ? 10 : 5))
function showTick(ts: string) {
  const date = toDate(ts)
  return !!date && Number(bjParts(date).minute) % tickEveryMinutes.value === 0
}
function bucketSpan(index: number) {
  const bucket = buckets.value[index]
  const start = toDate(bucket?.ts)
  if (!start) return ''
  const end = new Date(start.getTime() + stepMs.value)
  return `${clock(start)}–${clock(end)}`
}
const isNum = (value: number | null | undefined): value is number => typeof value === 'number' && Number.isFinite(value)
function sec(ms: number | null | undefined) { return isNum(ms) ? `${(ms / 1000).toFixed(1)}s` : '—' }
function pct(value: number | null | undefined) { return isNum(value) ? `${Math.round(value)}%` : '—' }
function fixed1(value: number | null | undefined) { return isNum(value) ? value.toFixed(1) : '—' }
const toSeconds = (ms: number | null | undefined) => (isNum(ms) ? Math.round(ms / 100) / 10 : null)

// ---- 6 个数：最近一个完整的格 ----
const completeIndex = computed(() => {
  const to = toDate(data.value?.to)?.getTime() ?? Date.now()
  for (let i = buckets.value.length - 1; i >= 0; i--) {
    const start = toDate(buckets.value[i].ts)?.getTime()
    if (start != null && start + stepMs.value <= to) return i
  }
  return -1
})
const kpiNote = computed(() => {
  const index = completeIndex.value
  if (index < 0) return t('firstTokenTrend.kpi.noComplete')
  const start = toDate(buckets.value[index].ts)!
  const end = new Date(start.getTime() + stepMs.value)
  return t('firstTokenTrend.kpi.note', { from: clock(start), to: clock(end) })
})
interface KpiSpec { key: string; label: string; pick: (bucket: FirstTokenTrendBucket) => number | null; format: (value: number) => string; scale: number; digits: number; unit: string; slowerIsWorse: boolean }
const kpiSpecs = computed<KpiSpec[]>(() => [
  { key: 'avg', label: t('firstTokenTrend.kpi.avg'), pick: b => b.avg_ms, format: v => sec(v), scale: 1000, digits: 1, unit: 's', slowerIsWorse: true },
  { key: 'p50', label: t('firstTokenTrend.kpi.p50'), pick: b => b.p50_ms, format: v => sec(v), scale: 1000, digits: 1, unit: 's', slowerIsWorse: true },
  { key: 'p80', label: t('firstTokenTrend.kpi.p80'), pick: b => b.p80_ms, format: v => sec(v), scale: 1000, digits: 1, unit: 's', slowerIsWorse: true },
  { key: 'over10', label: t('firstTokenTrend.kpi.over10'), pick: b => b.over10_pct, format: v => pct(v), scale: 1, digits: 0, unit: '%', slowerIsWorse: true },
  { key: 'inflight', label: t('firstTokenTrend.kpi.inflight'), pick: b => b.inflight_avg, format: v => t('firstTokenTrend.kpi.countUnit', { n: v.toFixed(1) }), scale: 1, digits: 1, unit: '', slowerIsWorse: false },
  { key: 'samples', label: t('firstTokenTrend.kpi.samples'), pick: b => b.samples, format: v => v.toLocaleString(), scale: 1, digits: 0, unit: '', slowerIsWorse: false },
])
const kpis = computed(() => {
  const index = completeIndex.value
  const cur = index >= 0 ? buckets.value[index] : null
  const prev = index >= 1 ? buckets.value[index - 1] : null
  return kpiSpecs.value.map(spec => {
    const value = cur ? spec.pick(cur) : null
    const before = prev ? spec.pick(prev) : null
    let delta = '—', tone = ''
    if (isNum(value) && isNum(before)) {
      const diff = Number(((value - before) / spec.scale).toFixed(spec.digits))
      const abs = Math.abs(diff).toFixed(spec.digits)
      if (diff === 0) delta = `→ ${abs}${spec.unit}`
      else {
        delta = `${diff > 0 ? '↑' : '↓'} ${abs}${spec.unit}`
        // 首字类变大是变慢，标红；变小标绿。并发和样本只显示方向。
        if (spec.slowerIsWorse) tone = diff > 0 ? 'up' : 'down'
      }
    }
    return { key: spec.key, label: spec.label, value: isNum(value) ? spec.format(value) : '—', delta, tone }
  })
})

// ---- 变动 ----
const sortedEvents = computed(() => [...(data.value?.events ?? [])].sort((a, b) => (toDate(b.at)?.getTime() ?? 0) - (toDate(a.at)?.getTime() ?? 0)))
function statusValue(value: string) { const key = `firstTokenTrend.events.statusValue.${value}`; return value && te(key) ? t(key) : value }
function eventShort(event: FirstTokenTrendEvent) {
  if (event.type === 'proxy') return t('firstTokenTrend.events.short.proxy', { from: event.from || '—', to: event.to || '—' })
  if (event.type === 'concurrency') return t('firstTokenTrend.events.short.concurrency', { to: event.to || '—' })
  return t('firstTokenTrend.events.short.status', { id: event.account_id, to: statusValue(event.to) })
}
function effectOf(event: FirstTokenTrendEvent) {
  if (!isNum(event.p50_after_ms)) return { text: t('firstTokenTrend.events.waiting'), tone: 'eff-none' }
  if (!isNum(event.p50_before_ms)) return { text: t('firstTokenTrend.events.noBaseline'), tone: 'eff-none' }
  const diff = Number(((event.p50_after_ms - event.p50_before_ms) / 1000).toFixed(1))
  if (diff === 0) return { text: t('firstTokenTrend.events.same'), tone: 'eff-none' }
  const value = `${Math.abs(diff).toFixed(1)}s`
  return diff > 0 ? { text: t('firstTokenTrend.events.slower', { value }), tone: 'eff-up' } : { text: t('firstTokenTrend.events.faster', { value }), tone: 'eff-down' }
}
// 变动在 x 轴上的位置：格 i 的点画在格中间（下标 i），格的起点是 i − 0.5。
interface EventMarker { event: FirstTokenTrendEvent; index: number; label: string; color: string }
const markers = computed<EventMarker[]>(() => {
  const first = toDate(buckets.value[0]?.ts)?.getTime()
  const count = buckets.value.length
  if (first == null || !count) return []
  return [...(data.value?.events ?? [])]
    .map(event => ({ event, index: ((toDate(event.at)?.getTime() ?? NaN) - first) / stepMs.value - 0.5 }))
    .filter(item => Number.isFinite(item.index) && item.index >= -0.5 && item.index <= count - 0.5)
    .sort((a, b) => a.index - b.index)
    .map(item => ({ ...item, label: eventShort(item.event), color: palette.value[item.event.type] ?? palette.value.status }))
})
// ---- 图表 ----
const hoveredEvent = ref<{ event: FirstTokenTrendEvent; x: number } | null>(null)
// 画变动竖线（Chart.js 没装 annotation 插件，这里自己画）。趋势图上带短标签，悬停竖线或标签显示详情。
function eventPlugin(id: string, withLabels: boolean): Plugin<'line'> {
  const rowHeight = 13
  return {
    id,
    afterDatasetsDraw(chart) {
      const { ctx, chartArea } = chart
      const xScale = chart.scales.x
      if (!xScale || !chartArea) return
      ctx.save()
      ctx.font = '11px system-ui, -apple-system, "PingFang SC", sans-serif'
      markers.value.forEach((marker, j) => {
        const x = xScale.getPixelForValue(marker.index)
        if (!Number.isFinite(x) || x < chartArea.left - 1 || x > chartArea.right + 1) return
        ctx.strokeStyle = marker.color
        ctx.globalAlpha = 0.85
        ctx.lineWidth = 1.2
        ctx.setLineDash([3, 3])
        ctx.beginPath()
        ctx.moveTo(x, withLabels ? chartArea.top - 6 : chartArea.top)
        ctx.lineTo(x, chartArea.bottom)
        ctx.stroke()
        if (withLabels) {
          ctx.globalAlpha = 1
          ctx.fillStyle = marker.color
          ctx.textAlign = x > chartArea.right - 40 ? 'right' : x < chartArea.left + 40 ? 'left' : 'center'
          ctx.textBaseline = 'alphabetic'
          ctx.fillText(marker.label, x, chartArea.top - 10 - (j % 3) * rowHeight)
        }
      })
      ctx.restore()
    },
    afterEvent(chart, args) {
      if (!withLabels) return
      const event = args.event
      if (event.type === 'mouseout') { hoveredEvent.value = null; return }
      if (event.type !== 'mousemove' || event.x == null || event.y == null) return
      const xScale = chart.scales.x
      let hit: { event: FirstTokenTrendEvent; x: number } | null = null
      let best = 6
      for (const marker of markers.value) {
        const x = xScale.getPixelForValue(marker.index)
        const distance = Math.abs(x - event.x)
        if (distance <= best && event.y <= chart.chartArea.bottom) { best = distance; hit = { event: marker.event, x } }
      }
      if (hit?.event !== hoveredEvent.value?.event) hoveredEvent.value = hit
    },
  }
}
// 实线的最后一个值写在图右边（设计稿里的「9.4s」「18 个」），虚线不标；离得太近的往下错开。
function endLabelPlugin(id: string, format: (value: number) => string): Plugin<'line'> {
  return {
    id,
    afterDatasetsDraw(chart) {
      const { ctx, chartArea } = chart
      const yScale = chart.scales.y
      if (!yScale || !chartArea) return
      const labels: { y: number; text: string; color: string }[] = []
      chart.data.datasets.forEach((dataset, index) => {
        if ((dataset as { borderDash?: number[] }).borderDash?.length || !chart.isDatasetVisible(index)) return
        const values = dataset.data as (number | null)[]
        for (let i = values.length - 1; i >= 0; i--) {
          const value = values[i]
          if (isNum(value)) { labels.push({ y: yScale.getPixelForValue(value), text: format(value), color: String(dataset.borderColor) }); break }
        }
      })
      labels.sort((a, b) => a.y - b.y)
      for (let i = 1; i < labels.length; i++) if (labels[i].y - labels[i - 1].y < 13) labels[i].y = labels[i - 1].y + 13
      ctx.save()
      ctx.font = '11px ui-monospace, SFMono-Regular, Menlo, monospace'
      ctx.textAlign = 'left'
      ctx.textBaseline = 'middle'
      for (const label of labels) { ctx.fillStyle = label.color; ctx.fillText(label.text, chartArea.right + 6, label.y) }
      ctx.restore()
    },
  }
}
const trendPlugins = [eventPlugin('fttTrendEvents', true), endLabelPlugin('fttTrendEnds', value => `${value.toFixed(1)}s`)]
const concPlugins = [eventPlugin('fttConcEvents', false), endLabelPlugin('fttConcEnds', value => t('firstTokenTrend.kpi.countUnit', { n: value.toFixed(1) }))]

const labels = computed(() => buckets.value.map(bucket => clock(bucket.ts)))
const seriesField: Record<SeriesKey, 'avg_ms' | 'p50_ms' | 'p80_ms'> = { avg: 'avg_ms', p50: 'p50_ms', p80: 'p80_ms' }
const thresholdLabel = computed(() => t('firstTokenTrend.trend.threshold'))

const trendData = computed<ChartData<'line', (number | null)[], string>>(() => {
  const c = palette.value
  const lines = seriesKeys.filter(key => shown[key]).map(key => ({
    label: t(`firstTokenTrend.series.${key}`),
    data: buckets.value.map(bucket => toSeconds(bucket[seriesField[key]])),
    borderColor: c[key],
    backgroundColor: c[key],
    borderWidth: key === 'p50' ? 2.4 : 1.6,
    pointRadius: 0,
    pointHoverRadius: 3,
    pointHitRadius: 8,
    tension: 0.2,
    spanGaps: false,
  }))
  return {
    labels: labels.value,
    datasets: [...lines, { label: thresholdLabel.value, data: buckets.value.map(() => SLOW_MS / 1000), borderColor: c.bad, backgroundColor: c.bad, borderWidth: 1.2, borderDash: [5, 4], pointRadius: 0, pointHitRadius: 0, pointHoverRadius: 0 }],
  }
})

function baseOptions(yUnit: string, topPadding: number, rightPadding = 8) {
  const c = palette.value
  return {
    responsive: true,
    maintainAspectRatio: false,
    animation: false as const,
    layout: { padding: { top: topPadding, right: rightPadding } },
    interaction: { intersect: false, mode: 'index' as const },
    plugins: {
      legend: { display: false },
      tooltip: {
        backgroundColor: c.tipBg, titleColor: c.tipTitle, bodyColor: c.tipBody, borderColor: c.grid, borderWidth: 1, padding: 10,
        callbacks: { title: (items: { dataIndex: number }[]) => bucketSpan(items[0]?.dataIndex ?? 0) },
      },
    },
    scales: {
      x: { type: 'category' as const, offset: true, grid: { display: false }, ticks: { color: c.text, font: { size: 10 }, autoSkip: false, maxRotation: 0, callback: (_value: string | number, index: number) => (showTick(buckets.value[index]?.ts ?? '') ? labels.value[index] : '') } },
      y: { type: 'linear' as const, beginAtZero: true, grid: { color: c.grid }, ticks: { color: c.text, font: { size: 10 }, callback: (value: string | number) => `${value}${yUnit}` } },
    },
  }
}

const trendOptions = computed<ChartOptions<'line'>>(() => {
  const base = baseOptions('s', 44, 48)
  return {
    ...base,
    plugins: {
      ...base.plugins,
      tooltip: {
        ...base.plugins.tooltip,
        filter: (item: TooltipItem<'line'>) => item.dataset.label !== thresholdLabel.value,
        callbacks: { ...base.plugins.tooltip.callbacks, label: (item: TooltipItem<'line'>) => `${item.dataset.label}: ${isNum(item.parsed.y) ? `${item.parsed.y.toFixed(1)}s` : '—'}` },
      },
    },
    scales: { ...base.scales, y: { ...base.scales.y, suggestedMax: 12 } },
  }
})

const concLabels = computed(() => ({ inflight: t('firstTokenTrend.concurrency.inflight'), limit: t('firstTokenTrend.concurrency.limit') }))
const concData = computed<ChartData<'line', (number | null)[], string>>(() => {
  const c = palette.value
  return {
    labels: labels.value,
    datasets: [
      { label: concLabels.value.inflight, data: buckets.value.map(bucket => (isNum(bucket.inflight_avg) ? bucket.inflight_avg : null)), borderColor: c.inflight, backgroundColor: c.inflightFill, fill: 'origin', borderWidth: 2, pointRadius: 0, pointHoverRadius: 3, pointHitRadius: 8, tension: 0.2 },
      { label: concLabels.value.limit, data: buckets.value.map(bucket => (isNum(bucket.concurrency_limit_sum) ? bucket.concurrency_limit_sum : null)), borderColor: c.concurrency, backgroundColor: c.concurrency, borderWidth: 1.4, borderDash: [5, 4], stepped: 'middle', pointRadius: 0, pointHoverRadius: 3, pointHitRadius: 8 },
    ],
  }
})
const concOptions = computed<ChartOptions<'line'>>(() => {
  const base = baseOptions('', 6, 48)
  return {
    ...base,
    plugins: { ...base.plugins, tooltip: { ...base.plugins.tooltip, callbacks: { ...base.plugins.tooltip.callbacks, label: (item: TooltipItem<'line'>) => `${item.dataset.label}: ${isNum(item.parsed.y) ? (item.datasetIndex === 0 ? item.parsed.y.toFixed(1) : String(item.parsed.y)) : '—'}` } } },
  }
})

const splitData = computed<ChartData<'bar', (number | null)[], string>>(() => {
  const c = palette.value
  return {
    labels: labels.value,
    datasets: splitSegments.map(seg => ({ label: t(`firstTokenTrend.split.${seg.key}`), data: buckets.value.map(bucket => toSeconds(bucket[seg.field])), backgroundColor: c[seg.key], borderWidth: 0, barPercentage: 0.66, categoryPercentage: 1 })),
  }
})
const splitOptions = computed<ChartOptions<'bar'>>(() => {
  const base = baseOptions('s', 6)
  return {
    ...base,
    plugins: { ...base.plugins, tooltip: { ...base.plugins.tooltip, callbacks: { ...base.plugins.tooltip.callbacks, label: (item: TooltipItem<'bar'>) => `${item.dataset.label}: ${isNum(item.parsed.y) ? `${item.parsed.y.toFixed(1)}s` : '—'}` } } },
    scales: { x: { ...base.scales.x, stacked: true }, y: { ...base.scales.y, stacked: true } },
  }
})

// ---- 按维度拆开 ----
function statusOf(row: FirstTokenTrendRow) {
  if (row.samples <= 0 || !isNum(row.p50_ms)) return { text: t('firstTokenTrend.dims.status.none'), tone: 'pill-none' }
  if (row.p50_ms < SLOW_MS) return { text: t('firstTokenTrend.dims.status.good'), tone: 'pill-good' }
  if (row.p50_ms < SLOW_MS * 2) return { text: t('firstTokenTrend.dims.status.warn'), tone: 'pill-warn' }
  return { text: t('firstTokenTrend.dims.status.bad'), tone: 'pill-bad' }
}
interface Spark { segments: string[]; last: [number, number]; thrY: number }
function buildSpark(values: (number | null)[]): Spark | null {
  const width = 120, height = 28
  const secs = values.map(v => toSeconds(v))
  const valid = secs.filter(isNum)
  if (!valid.length) return null
  const top = Math.max(25, ...valid)
  const x = (i: number) => 3 + (width - 8) * (secs.length > 1 ? i / (secs.length - 1) : 0.5)
  const y = (v: number) => height - 3 - (height - 6) * v / top
  const segments: string[] = []
  let current: string[] = []
  let last: [number, number] = [0, 0]
  secs.forEach((v, i) => {
    if (!isNum(v)) { if (current.length) segments.push(current.join(' ')); current = []; return }
    last = [Number(x(i).toFixed(1)), Number(y(v).toFixed(1))]
    current.push(`${last[0]},${last[1]}`)
  })
  if (current.length) segments.push(current.join(' '))
  return { segments: segments.filter(seg => seg.includes(' ')), last, thrY: Number(y(SLOW_MS / 1000).toFixed(1)) }
}
const sparks = computed<Record<string, Spark | null>>(() => Object.fromEntries((data.value?.rows ?? []).map(row => [row.key, buildSpark(row.spark ?? [])])))

onMounted(() => {
  themeObserver = new MutationObserver(() => { isDark.value = document.documentElement.classList.contains('dark') })
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
  void loadGroups()
  void load()
  if (autoRefresh.enabled.value) { autoRefresh.resetCountdown(); autoRefresh.start() }
})
onBeforeUnmount(() => { disposed = true; themeObserver?.disconnect() })
</script>

<style scoped>
/* 版式沿用代理优选页（ProxyAutoSwitchView），布局照设计稿 first-token-trend.html。 */
.ftt-workspace { @apply flex w-full min-w-0 flex-col gap-4 text-gray-900 dark:text-gray-100; }
.ftt-workspace > nav { margin-bottom: 0; max-width: 100%; }
.workspace-heading { @apply flex flex-wrap items-start justify-between gap-4; }
.workspace-heading > div:first-child { @apply min-w-0; max-width: 56rem; }
.workspace-eyebrow { @apply mb-1 text-[11px] font-semibold tracking-widest text-primary-600 dark:text-primary-400; }
.workspace-heading h2 { @apply text-2xl font-semibold tracking-tight; }
.workspace-subtitle { @apply mt-1.5 text-sm leading-6 text-gray-500 dark:text-gray-400; }
.heading-actions { @apply flex shrink-0 items-center gap-3; }
.updated-at { @apply text-xs tabular-nums text-gray-400; }
.icon-button { @apply flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-gray-200 text-gray-500 hover:bg-gray-50 dark:border-dark-700 dark:text-gray-400 dark:hover:bg-dark-800; }
.workspace-panel { @apply flex min-w-0 flex-col rounded-2xl border border-gray-200/80 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.panel-error { @apply flex items-center justify-between gap-3 rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-950/40 dark:text-red-300; overflow-wrap: anywhere; }
.panel-error button { @apply shrink-0 underline; }
.panel-empty { @apply flex min-h-44 flex-col items-center justify-center gap-3 p-6 text-center text-sm text-gray-400; }
.panel-empty h4 { @apply text-gray-600 dark:text-gray-300; }
.panel-empty p { @apply max-w-sm text-xs leading-relaxed; }
.note { @apply m-0 text-xs text-gray-500 dark:text-gray-400; }
.muted { @apply text-gray-400 dark:text-gray-500; }
.num { @apply font-mono tabular-nums; }

.filter-panel { @apply flex-row flex-wrap items-center gap-x-6 gap-y-3 px-4 py-3; }
.fgroup { @apply flex flex-wrap items-center gap-2; }
.flabel { @apply text-xs text-gray-500 dark:text-gray-400; }
.field-hint { @apply text-xs; }
.group-select { @apply w-auto max-w-full rounded-lg px-3 py-1.5 text-sm; }
.seg { @apply inline-flex max-w-full flex-wrap gap-1 rounded-xl border border-gray-200 bg-gray-50 p-1 dark:border-dark-700 dark:bg-dark-800; }
.seg button { @apply rounded-lg px-3 py-1 text-sm text-gray-500 transition-colors hover:bg-white dark:text-gray-400 dark:hover:bg-dark-700; }
.seg button.on { @apply bg-gray-900 font-medium text-white hover:bg-gray-900 dark:bg-gray-100 dark:text-gray-900 dark:hover:bg-gray-100; }
.chip { @apply inline-flex items-center gap-1.5 rounded-full border border-gray-200 bg-white px-3 py-1 text-sm text-gray-700 transition-opacity dark:border-dark-600 dark:bg-dark-900 dark:text-gray-200; }
.chip i { @apply block h-[3px] w-3 rounded-sm; }
.chip[aria-pressed='false'] { @apply opacity-40; }
.refresh-toggle { @apply inline-flex shrink-0 items-center gap-2 whitespace-nowrap text-sm text-gray-700 dark:text-gray-200; }
.toggle-track { @apply relative block h-5 w-9 shrink-0 rounded-full bg-gray-300 transition-colors dark:bg-dark-600; }
.toggle-thumb { @apply absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white shadow transition-transform; }
.refresh-toggle[aria-checked='true'] .toggle-track { @apply bg-primary-600 dark:bg-primary-500; }
.refresh-toggle[aria-checked='true'] .toggle-thumb { @apply translate-x-4; }

.kpis { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 10px; }
.kpi { @apply grid min-w-0 gap-0.5 rounded-xl border border-gray-200/80 bg-white px-3.5 py-3 shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.kpi .k { @apply text-xs text-gray-500 dark:text-gray-400; }
.kpi .v { @apply text-xl font-semibold; }
.kpi .d { @apply text-xs text-gray-400; }
.kpi .d.up { @apply text-red-600 dark:text-red-400; }
.kpi .d.down { @apply text-emerald-600 dark:text-emerald-400; }

.card { @apply gap-2 px-4 pb-3 pt-4; }
.card-h { @apply flex flex-wrap items-baseline justify-between gap-x-3.5 gap-y-2; }
.card-h h3 { @apply text-base font-semibold; }
.card-h .sub { @apply text-xs text-gray-500 dark:text-gray-400; }
.legend { @apply flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-gray-500 dark:text-gray-400; }
.legend > span { @apply inline-flex items-center gap-1.5; }
.legend i { @apply block h-2.5 w-2.5 rounded-sm; }
.legend i.dot { @apply rounded-full; }
.legend i.line { @apply h-[3px] w-3.5; }
.legend i.dash { @apply h-0 w-3.5 rounded-none border-t-2 border-dashed bg-transparent; }
.limit-note { @apply text-amber-600 dark:text-amber-400; }
.chart-box { @apply relative w-full min-w-0; }
.trend-box { height: 320px; }
.conc-box { height: 170px; }
.split-box { height: 210px; }
.event-tip { @apply pointer-events-none absolute top-0 z-10 grid max-w-xs gap-0.5 rounded-lg border border-gray-200 bg-white px-3 py-2 text-xs text-gray-600 shadow-lg dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300; transform: translateX(-50%); }
.event-tip strong { @apply font-medium text-gray-900 dark:text-gray-100; }

.table-scroll { @apply min-w-0 overflow-x-auto; }
.data-table { @apply w-full text-[13px]; min-width: 760px; border-collapse: collapse; }
.data-table th, .data-table td { @apply whitespace-nowrap border-b border-gray-100 px-2.5 py-2 text-right dark:border-dark-800; }
.data-table th { @apply text-xs font-medium text-gray-500 dark:text-gray-400; }
.data-table .l { @apply text-left; }
.data-table td.wrap { @apply whitespace-normal; min-width: 14rem; }
.data-table .name > span { @apply text-primary-700 dark:text-primary-400; }
.data-table small { @apply ml-1.5 text-xs; }
.acct { @apply font-medium; }
.tag { @apply inline-flex items-center gap-1.5 text-xs; }
.tag i { @apply block h-2 w-2 rounded-full; }
.effect { @apply text-xs font-semibold; }
.eff-up { @apply text-red-600 dark:text-red-400; }
.eff-down { @apply text-emerald-600 dark:text-emerald-400; }
.eff-none { @apply rounded-full border border-current px-2 py-px font-normal text-gray-400; }
.pill { @apply inline-block rounded-full border border-current px-2 py-px text-xs leading-normal; }
.pill-good { @apply text-emerald-600 dark:text-emerald-400; }
.pill-warn { @apply text-amber-600 dark:text-amber-400; }
.pill-bad { @apply text-red-600 dark:text-red-400; }
.pill-none { @apply text-gray-400; }
.spark { @apply block; }
.spark-thr { @apply stroke-red-500 dark:stroke-red-400; stroke-width: 0.8; stroke-dasharray: 5 4; }
.spark-line { @apply stroke-yellow-600 dark:stroke-yellow-400; fill: none; stroke-width: 1.5; }
.spark-dot { @apply fill-yellow-600 dark:fill-yellow-400; }

.skeleton-stack { @apply flex flex-col gap-4; }
.kpi-skeleton, .chart-skeleton { @apply block rounded-xl bg-gray-100 dark:bg-dark-800; animation: ftt-pulse 1.7s ease-in-out infinite; }
.kpi-skeleton { height: 76px; }
.chart-skeleton { height: 320px; }
.chart-skeleton.short { height: 170px; }
button:disabled { @apply cursor-not-allowed opacity-40; }
button:focus-visible, select:focus-visible { @apply outline-none ring-2 ring-primary-400 ring-offset-2 dark:ring-offset-dark-900; }
@keyframes ftt-pulse { 50% { opacity: .45; } }

@media (max-width: 980px) { .kpis { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
@media (max-width: 760px) { .kpis { grid-template-columns: repeat(2, minmax(0, 1fr)); } .trend-box { height: 280px; } }
@media (prefers-reduced-motion: reduce) { .kpi-skeleton, .chart-skeleton { animation: none; } .toggle-thumb, .toggle-track { transition: none; } }
</style>
