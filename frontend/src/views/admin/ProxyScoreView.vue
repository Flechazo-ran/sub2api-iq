<template>
  <AppLayout>
    <div class="psc-workspace">
      <RoutingNav />
      <div class="workspace-heading">
        <div>
          <p class="workspace-eyebrow">{{ t('nav.routing') }}</p>
          <h2>{{ t('proxyScore.title') }}</h2>
          <p class="workspace-subtitle">{{ t('proxyScore.intro') }}</p>
        </div>
        <div class="heading-actions">
          <span v-if="loadedAt" class="updated-at">{{ loadedAt }}</span>
          <button type="button" class="icon-button" :disabled="loading" :aria-label="t('proxyScore.refresh')" data-testid="psc-refresh" @click="load"><Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" /></button>
        </div>
      </div>

      <section class="workspace-panel" aria-labelledby="psc-heading">
        <header class="panel-heading">
          <h3 id="psc-heading" class="sr-only">{{ t('proxyScore.title') }}</h3>
          <div class="range-tabs" role="tablist" :aria-label="t('proxyScore.title')">
            <button v-for="r in ranges" :key="r" type="button" role="tab" class="range-tab" :class="{ on: selectedRange === r }" :aria-selected="selectedRange === r" :data-testid="`psc-range-${r}`" @click="switchRange(r)">{{ t(`proxyScore.range.${r}`) }}</button>
          </div>
        </header>

        <div v-if="error" role="alert" class="panel-error" data-testid="psc-error">
          {{ error }}
          <button :disabled="loading" @click="load">{{ t('proxyScore.retry') }}</button>
        </div>

        <div v-if="!loaded && loading" class="psc-skeleton" :aria-label="t('proxyScore.loading')">
          <span v-for="n in 5" :key="n" class="cell-skeleton psc-skel-row" />
        </div>

        <div v-else class="table-scroll">
          <table class="data-table psc-table" data-testid="psc-table">
            <thead>
              <tr>
                <th class="cell-left">{{ t('proxyScore.col.proxy') }}</th>
                <th>{{ t('proxyScore.col.grade') }}</th>
                <th>{{ t('proxyScore.col.p50') }}</th>
                <th>{{ t('proxyScore.col.mean') }}</th>
                <th>{{ t('proxyScore.col.p80') }}</th>
                <th>{{ t('proxyScore.col.samples') }}</th>
                <th>{{ t('proxyScore.col.judgedSlow') }}</th>
                <th class="cell-left">{{ t('proxyScore.col.expires') }}</th>
                <th class="cell-left">{{ t('proxyScore.col.advice') }}</th>
              </tr>
            </thead>
            <tbody v-if="rows.length === 0 && loaded">
              <tr><td colspan="9" class="table-empty" data-testid="psc-empty">{{ t('proxyScore.empty') }}</td></tr>
            </tbody>
            <tbody v-else>
              <tr v-for="row in rows" :key="row.id" :data-proxy-id="row.id" :data-testid="`psc-row-${row.id}`">
                <td class="cell-left proxy-name-cell">{{ row.name }}</td>
                <td class="grade-cell">
                  <span v-if="row.grade" :class="gradeClass(row.grade)" class="grade-badge" :data-testid="`psc-grade-${row.id}`">{{ row.grade }}</span>
                  <span v-else class="grade-badge grade-none" :data-testid="`psc-grade-${row.id}`">{{ t('proxyScore.grade.noData') }}</span>
                  <span v-if="row.judged_slow >= 3 && row.grade" class="demote-mark" :title="t('proxyScore.demotedHint')">↓</span>
                </td>
                <td class="tabular-nums">{{ fmtMs(row.p50_ms) }}</td>
                <td class="tabular-nums">{{ fmtMs(row.mean_ms) }}</td>
                <td class="tabular-nums">{{ fmtMs(row.p80_ms) }}</td>
                <td class="tabular-nums">{{ row.samples > 0 ? row.samples.toLocaleString() : '—' }}</td>
                <td class="tabular-nums" :title="t('proxyScore.judgedSlowHint')">{{ row.judged_slow > 0 ? row.judged_slow : '—' }}</td>
                <td class="cell-left">
                  <span v-if="row.expires_at" :class="expiryClass(row.expires_at)">{{ fmtExpiry(row.expires_at) }}</span>
                  <span v-else>—</span>
                </td>
                <td class="cell-left">
                  <span :class="adviceClass(row.advice)">{{ t(`proxyScore.advice.${row.advice}`) }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import RoutingNav from '@/components/admin/operations/RoutingNav.vue'
import Icon from '@/components/icons/Icon.vue'
import { getProxyScore } from '@/api/admin/proxyScore'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { ProxyScoreRange, ProxyScoreRow, ProxyScoreGrade, ProxyScoreAdvice } from '@/api/admin/proxyScore'

const { t } = useI18n()

const ranges: ProxyScoreRange[] = ['7d', '30d']
const selectedRange = ref<ProxyScoreRange>('7d')
const rows = ref<ProxyScoreRow[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const loadedAt = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = await getProxyScore(selectedRange.value)
    rows.value = result.rows
    loaded.value = true
    loadedAt.value = new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  } catch (e: unknown) {
    error.value = extractApiErrorMessage(e, t('proxyScore.loadFailed'))
  } finally {
    loading.value = false
  }
}

function switchRange(r: ProxyScoreRange) {
  if (r === selectedRange.value) return
  selectedRange.value = r
  loaded.value = false
  rows.value = []
  load()
}

onMounted(load)

function fmtMs(ms: number | null): string {
  if (ms == null) return '—'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function fmtExpiry(expiresAt: string | null): string {
  if (!expiresAt) return '—'
  const d = new Date(expiresAt)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

function daysUntil(expiresAt: string): number {
  return Math.ceil((new Date(expiresAt).getTime() - Date.now()) / 86400000)
}

function expiryClass(expiresAt: string | null): string {
  if (!expiresAt) return ''
  const days = daysUntil(expiresAt)
  if (days <= 0) return 'expiry-tag expiry-expired'
  if (days <= 7) return 'expiry-tag expiry-soon'
  return ''
}

const gradeClasses: Record<ProxyScoreGrade, string> = { S: 'grade-s', A: 'grade-a', B: 'grade-b', C: 'grade-c', D: 'grade-d', '': 'grade-none' }

function gradeClass(grade: ProxyScoreGrade): string {
  return gradeClasses[grade] ?? 'grade-none'
}

function adviceClass(advice: ProxyScoreAdvice): string {
  return { renew: 'advice-renew', observe: 'advice-observe', drop: 'advice-drop' }[advice] ?? ''
}
</script>

<style scoped>
/* 版式和配色沿用 ProxyAutoSwitchView */
.psc-workspace { @apply flex w-full min-w-0 flex-col gap-4 text-gray-900 dark:text-gray-100; }
.psc-workspace > nav { margin-bottom: 0; max-width: 100%; }
.workspace-heading { @apply flex flex-wrap items-start justify-between gap-4; }
.workspace-heading > div { @apply min-w-0; max-width: 56rem; }
.workspace-eyebrow { @apply mb-1 text-[11px] font-semibold tracking-widest text-primary-600 dark:text-primary-400; }
.workspace-heading h2 { @apply text-2xl font-semibold tracking-tight; }
.workspace-subtitle { @apply mt-1.5 text-sm leading-6 text-gray-500 dark:text-gray-400; }
.workspace-panel { @apply flex min-w-0 flex-col overflow-hidden rounded-2xl border border-gray-200/80 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.panel-heading { @apply flex shrink-0 items-center justify-between gap-3 px-5 pb-4 pt-5; }
.icon-button { @apply flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-gray-200 text-gray-500 hover:bg-gray-50 dark:border-dark-700 dark:text-gray-400 dark:hover:bg-dark-800; }
.panel-error { @apply mx-5 mb-4 flex items-center justify-between gap-3 rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-950/40 dark:text-red-300; overflow-wrap: anywhere; }
.panel-error button { @apply shrink-0 underline; }

.heading-actions { @apply flex items-center gap-2; }
.updated-at { @apply text-xs text-gray-400 dark:text-gray-500; }

.range-tabs { @apply flex gap-1; }
.range-tab { @apply rounded-lg px-3 py-1.5 text-xs text-gray-500 transition-colors hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-800; }
.range-tab.on { @apply bg-gray-900 font-medium text-white dark:bg-gray-100 dark:text-gray-900; }

.psc-skeleton { @apply space-y-2 px-5 py-4; }
.psc-skel-row { @apply block h-8 w-full rounded; }
.cell-skeleton { @apply bg-gray-100 dark:bg-dark-700; animation: psc-pulse 1.7s ease-in-out infinite; }

.table-scroll { @apply min-w-0 overflow-x-auto border-t border-gray-100 dark:border-dark-700; }
.data-table { @apply w-full border-collapse text-left text-xs; }
.data-table th { @apply whitespace-nowrap border-b border-gray-100 px-3 py-2.5 font-medium text-gray-400 dark:border-dark-800 dark:text-gray-500; }
.data-table th:not(.cell-left) { @apply text-right; }
.data-table td { @apply border-b border-gray-100 px-3 py-3 align-middle dark:border-dark-800; }
.data-table .tabular-nums { @apply text-right; font-variant-numeric: tabular-nums; }
.data-table .cell-left { @apply text-left; }
.table-empty { @apply py-8 text-center text-gray-400 dark:text-gray-500; }
.psc-table { min-width: 820px; }
.psc-table td { @apply whitespace-nowrap; }
.proxy-name-cell { @apply font-mono text-[11px]; }

.grade-cell { @apply flex items-center justify-end gap-1; }
.grade-badge { @apply inline-flex w-7 items-center justify-center rounded px-2 py-0.5 text-xs font-bold; }
.grade-s { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300; }
.grade-a { @apply bg-sky-50 text-sky-700 dark:bg-sky-950/40 dark:text-sky-300; }
.grade-b { @apply bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300; }
.grade-c { @apply bg-orange-50 text-orange-700 dark:bg-orange-950/40 dark:text-orange-300; }
.grade-d { @apply bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-300; }
.grade-none { @apply bg-gray-100 text-gray-400 dark:bg-dark-800 dark:text-gray-500; }
.demote-mark { @apply text-xs text-amber-500 dark:text-amber-400; }

.expiry-tag { @apply rounded px-1.5 py-0.5 text-[11px] font-medium; }
.expiry-soon { @apply bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-400; }
.expiry-expired { @apply bg-rose-50 text-rose-600 dark:bg-rose-950/40 dark:text-rose-400; }

.advice-renew { @apply text-xs font-medium text-emerald-600 dark:text-emerald-400; }
.advice-observe { @apply text-xs font-medium text-amber-600 dark:text-amber-400; }
.advice-drop { @apply text-xs text-gray-400 dark:text-gray-500; }

@keyframes psc-pulse { 50% { opacity: .45; } }
@media (prefers-reduced-motion: reduce) { .cell-skeleton { animation: none; } }
</style>
