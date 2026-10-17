<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import RequestTimeline from '@/components/requests/RequestTimeline.vue'
import ModelIcon from '@/components/common/ModelIcon.vue'
import PlatformBadge from '@/components/common/PlatformBadge.vue'
import RequestIdLink from '@/components/common/RequestIdLink.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { formatDateTime, formatTokens } from '@/utils/format'
import { formatTimingMs } from '@/utils/requestTiming'
import type { IconName } from '@/components/icons/registry'
import type { UsageLogTiming } from '@/types'
import type { RequestAlias, RequestDetail } from '@/api/requests'

// RequestDetailCard.vue presents one request with its timing, attempts, usage,
// errors and related identifiers. Timing lists the request's failure stages and
// each usage row's performance as separate timelines on one scale, because
// usage owns its performance while failure diagnostics belong to the request.
//
// Each attempt shows the identifiers it owns: the upstream request ID with the
// header it came from and any response ID recorded for the same provider and
// attempt number. The related identifiers section lists only the remaining
// aliases, so an ID appears once and an alias whose attempt is missing from
// the attempt list keeps its provider and attempt ownership.

const props = defineProps<{
  item: RequestDetail
}>()
const emit = defineEmits<{
  (event: 'openError', id: number): void
}>()

const { t } = useI18n()
const { formatBalanceAmount } = useBalanceDisplay()

// 请求状态和上游尝试结果共用一组徽章样式。
const STATE_STYLES: Record<string, { badge: string; icon: IconName }> = {
  completed: { badge: 'badge-success', icon: 'checkCircle' },
  usage_unknown: { badge: 'badge-warning', icon: 'questionCircle' },
  usage_recorded: { badge: 'badge-primary', icon: 'checkCircle' },
  failed: { badge: 'badge-danger', icon: 'xCircle' },
  canceled: { badge: 'badge-gray', icon: 'ban' },
  running: { badge: 'badge-warning', icon: 'clock' },
}

const stateStyle = (state?: string) => STATE_STYLES[state || ''] ?? { badge: 'badge-gray', icon: 'questionCircle' as IconName }
const stateLabel = (state?: string) => (state ? t(`requests.states.${state}`, state) : '-')

// 状态码按 2xx、4xx、5xx 着色。
function statusClass(code?: number) {
  if (!code) return 'text-gray-500 dark:text-dark-300'
  if (code >= 500) return 'text-red-600 dark:text-red-400'
  if (code >= 400) return 'text-amber-600 dark:text-amber-400'
  return 'text-emerald-600 dark:text-emerald-400'
}

const errorText = computed(() => {
  const code = props.item.error_code
  return code ? t(`requests.errorCodes.${code}`, code) : ''
})

const phaseLabel = (phase: string) => t(`requests.errorPhases.${phase}`, phase)
const aliasLabel = (kind: string) => t(`requests.aliasKinds.${kind}`, kind)
const formatCost = (value: number) => formatBalanceAmount(value, { fractionDigits: 6 })

const usageRows = computed(() => props.item.usage ?? [])
const errorRows = computed(() => props.item.errors ?? [])
const aliasRows = computed(() => props.item.aliases ?? [])
const attemptRows = computed(() => props.item.attempts ?? [])

// Timeline pairs one timing record with the caption shown when several exist.
interface Timeline {
  key: string // Stable list key.
  label: string // Failure timing title or the usage row's model and attempt.
  timing: UsageLogTiming // Ingress-relative observations to draw.
}

// timelines collects the request's failure stages and every usage row's
// performance. Captions appear only when a request carries more than one; a
// lone failure timeline titles the section, while a lone usage timeline keeps
// the generic title because its usage row already names the model and attempt.
const timelines = computed<Timeline[]>(() => {
  const list: Timeline[] = []
  const failure = props.item.failure_timings
  if (failure && Object.keys(failure).length) {
    list.push({ key: 'failure', label: t('requests.failureTimings'), timing: failure })
  }
  for (const usage of usageRows.value) {
    if (!usage.timings) continue
    const attempt = usage.timings.values.attempt_number
    list.push({
      key: `usage:${usage.id}`,
      label: attempt ? `${usage.model} · ${t('requests.attemptNumber', { number: attempt })}` : usage.model,
      timing: { ...usage.timings.values, upstream_stream: usage.timings.upstream_stream },
    })
  }
  return list
})
// timingTitle names the timing section after its only timeline when that is the failure record.
const timingTitle = computed(() => {
  const [only] = timelines.value
  return timelines.value.length === 1 && only.key === 'failure' ? only.label : t('requests.timings')
})

// aliasKey retains separate rows when providers, attempts, or sources share an identifier.
const aliasKey = (alias: RequestAlias) =>
  JSON.stringify([alias.kind, alias.value, alias.source, alias.attempt_number, alias.provider_id])

// Attempt is one upstream attempt entry of the request detail.
type Attempt = NonNullable<RequestDetail['attempts']>[number]

// attemptResponses returns response-object identifiers with matching provider and attempt ownership.
const attemptResponses = (attempt: Attempt) =>
  aliasRows.value.filter((alias) =>
    alias.kind === 'response'
    && alias.attempt_number === attempt.number
    && alias.provider_id === attempt.provider_id)

// shownByAttempt reports whether an attempt row already displays the alias.
// Classified identifiers match by provider and attempt number; historical
// unclassified upstream aliases carry no owner and match by value instead.
function shownByAttempt(alias: RequestAlias) {
  return attemptRows.value.some((attempt) => {
    if (alias.kind === 'upstream') return !alias.attempt_number && attempt.upstream_request_id === alias.value
    if (alias.attempt_number !== attempt.number || alias.provider_id !== attempt.provider_id) return false
    return alias.kind === 'response' || (alias.kind === 'upstream_request' && alias.value === attempt.upstream_request_id)
  })
}
// listedAliases holds the identifiers left for the related identifiers section.
const listedAliases = computed(() => aliasRows.value.filter((alias) => !shownByAttempt(alias)))

// Identifier kinds whose origin matters enough to say so when it is unknown.
const SOURCE_KINDS = new Set(['caller', 'upstream', 'upstream_request', 'response'])

// aliasMeta describes where an identifier came from and which attempt owns it.
function aliasMeta(alias: RequestAlias) {
  const parts: string[] = []
  if (alias.source) parts.push(t('requests.via', { source: alias.source }))
  else if (SOURCE_KINDS.has(alias.kind)) parts.push(t('requests.sourceUnrecorded'))
  if (alias.provider_id) parts.push(`${t('requests.provider')} #${alias.provider_id}`)
  if (alias.attempt_number) parts.push(t('requests.attemptNumber', { number: alias.attempt_number }))
  return parts.join(' · ')
}

// attemptHasDetails reports whether an attempt needs the detail list under its summary line.
const attemptHasDetails = (attempt: Attempt) =>
  Boolean(attempt.upstream_request_id || attempt.reason || attempt.stage || attemptResponses(attempt).length)
</script>

<template>
  <article class="card overflow-hidden">
    <!-- 头部：状态、端点和请求 ID -->
    <header class="space-y-4 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <div class="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          <span class="badge" :class="stateStyle(item.state).badge">
            <Icon :name="stateStyle(item.state).icon" size="xs" :animate-on-hover="false" />
            {{ stateLabel(item.state) }}
          </span>
          <span v-if="item.method" class="rounded-compact bg-gray-100 px-1.5 py-0.5 font-mono text-xs font-medium text-gray-600 dark:bg-dark-700 dark:text-dark-200">{{ item.method }}</span>
          <span class="min-w-0 break-all font-mono text-sm text-gray-900 dark:text-dark-50">{{ item.path || '-' }}</span>
        </div>
        <RequestIdLink :value="item.request_id" :link="false" full class="shrink-0" />
      </div>
      <p v-if="errorText" class="flex items-center gap-1.5 text-sm text-red-600 dark:text-red-400">
        <Icon name="exclamationCircle" size="sm" class="shrink-0" :animate-on-hover="false" />
        {{ errorText }}
      </p>
      <div v-if="item.pending || item.legacy" class="space-y-2">
        <SettingsNotice v-if="item.pending">{{ t('requests.pending') }}</SettingsNotice>
        <SettingsNotice v-if="item.legacy" tone="warning">{{ t('requests.legacy') }}</SettingsNotice>
      </div>
    </header>

    <!-- 概要：开始时间、耗时、状态码和模型 -->
    <dl class="grid grid-cols-2 gap-4 px-6 py-4 md:grid-cols-4">
      <div>
        <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t(item.legacy ? 'requests.usageCreatedAt' : 'requests.startedAt') }}</dt>
        <dd class="mt-1 text-sm font-medium tabular-nums text-gray-900 dark:text-dark-50">{{ formatDateTime(item.started_at) }}</dd>
      </div>
      <div>
        <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('requests.duration') }}</dt>
        <dd class="mt-1 text-sm font-medium tabular-nums text-gray-900 dark:text-dark-50">{{ formatTimingMs(item.duration_ms) }}</dd>
      </div>
      <div>
        <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('requests.statusCode') }}</dt>
        <dd class="mt-1 font-mono text-sm font-medium" :class="statusClass(item.status_code)">{{ item.status_code ?? '-' }}</dd>
      </div>
      <div class="min-w-0">
        <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('requests.model') }}</dt>
        <dd class="mt-1 flex min-w-0 items-center gap-1.5 text-sm font-medium text-gray-900 dark:text-dark-50">
          <template v-if="item.model">
            <ModelIcon :model="item.model" size="16px" class="shrink-0" />
            <span class="truncate" :title="item.model">{{ item.model }}</span>
          </template>
          <span v-else class="text-gray-400 dark:text-dark-500">-</span>
        </dd>
      </div>
    </dl>

    <div class="divide-y divide-gray-100 border-t border-gray-100 dark:divide-dark-700 dark:border-dark-700">
      <!-- 发起请求和平台 -->
      <div v-if="item.parent_request_id || item.platform" class="flex flex-wrap items-center gap-x-8 gap-y-2 px-6 py-3 text-sm">
        <div v-if="item.parent_request_id" class="flex min-w-0 items-center gap-2">
          <span class="shrink-0 text-xs text-gray-500 dark:text-dark-400">{{ t('requests.parent') }}</span>
          <RequestIdLink :value="item.parent_request_id" full />
        </div>
        <div v-if="item.platform" class="flex items-center gap-2">
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('requests.platform') }}</span>
          <PlatformBadge :platform="item.platform" />
        </div>
      </div>

      <!-- Timing: one timeline per failure record or usage row, all scaled to the request duration. -->
      <section v-if="timelines.length" class="px-6 py-5">
        <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-dark-50">{{ timingTitle }}</h3>
        <div class="space-y-5">
          <div v-for="timeline in timelines" :key="timeline.key">
            <p v-if="timelines.length > 1" class="mb-2 text-xs font-medium text-gray-700 dark:text-dark-200">{{ timeline.label }}</p>
            <RequestTimeline :timing="timeline.timing" :duration="item.duration_ms" />
          </div>
        </div>
      </section>

      <!-- Attempts: a summary line, then details that align with the outcome badge from sm upward. -->
      <section v-if="attemptRows.length" class="px-6 py-5">
        <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-dark-50">
          {{ t('requests.attempts') }}
          <span class="ml-1 font-normal text-gray-400 dark:text-dark-400">{{ attemptRows.length }}</span>
        </h3>
        <ol class="divide-y divide-gray-100 rounded-control border border-gray-200 dark:divide-dark-700 dark:border-dark-600">
          <li v-for="(attempt, attemptIndex) in attemptRows" :key="`${attempt.number}:${attempt.provider_id}:${attemptIndex}`" class="px-4 py-3">
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1 whitespace-nowrap text-sm">
              <span class="w-6 shrink-0 font-mono text-xs text-gray-400 dark:text-dark-400">
                <template v-if="attempt.number > 0">#{{ attempt.number }}</template>
                <template v-else>
                  <span aria-hidden="true" :title="t('requests.unassignedAttempt')">-</span>
                  <span class="sr-only">{{ t('requests.unassignedAttempt') }}</span>
                </template>
              </span>
              <span class="badge" :class="stateStyle(attempt.outcome).badge">{{ stateLabel(attempt.outcome) }}</span>
              <span v-if="attempt.status_code" class="font-mono text-xs" :class="statusClass(attempt.status_code)">HTTP {{ attempt.status_code }}</span>
              <span v-if="attempt.provider_id" class="text-xs text-gray-500 dark:text-dark-300">{{ t('requests.provider') }} #{{ attempt.provider_id }}</span>
              <span v-if="attempt.duration_ms != null" class="ml-auto font-mono text-xs tabular-nums text-gray-600 dark:text-dark-200">{{ formatTimingMs(attempt.duration_ms) }}</span>
            </div>
            <dl v-if="attemptHasDetails(attempt)" class="mt-2 grid grid-cols-1 items-baseline gap-x-3 gap-y-1 text-xs sm:ml-9 sm:grid-cols-[9rem_minmax(0,1fr)] sm:gap-y-1.5">
              <template v-if="attempt.upstream_request_id">
                <dt class="mt-1 text-gray-500 first:mt-0 dark:text-dark-400 sm:mt-0">{{ aliasLabel(attempt.upstream_request_id_header ? 'upstream_request' : 'upstream') }}</dt>
                <dd class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
                  <RequestIdLink :value="attempt.upstream_request_id" :link="false" full />
                  <span class="text-gray-400 dark:text-dark-500">{{ attempt.upstream_request_id_header ? t('requests.via', { source: attempt.upstream_request_id_header }) : t('requests.sourceUnrecorded') }}</span>
                </dd>
              </template>
              <template v-for="alias in attemptResponses(attempt)" :key="aliasKey(alias)">
                <dt class="mt-1 text-gray-500 first:mt-0 dark:text-dark-400 sm:mt-0">{{ aliasLabel('response') }}</dt>
                <dd class="min-w-0"><RequestIdLink :value="alias.value" :link="false" full /></dd>
              </template>
              <template v-if="attempt.reason || attempt.stage">
                <dt class="mt-1 text-gray-500 first:mt-0 dark:text-dark-400 sm:mt-0">{{ t('requests.reason') }}</dt>
                <dd class="min-w-0 break-words text-gray-700 dark:text-dark-100">
                  <span v-if="attempt.stage" class="font-medium">{{ phaseLabel(attempt.stage) }}</span>
                  <span v-if="attempt.stage && attempt.reason" class="text-gray-400 dark:text-dark-500"> · </span>
                  <span v-if="attempt.reason">{{ attempt.reason }}</span>
                </dd>
              </template>
            </dl>
            <RequestTimeline v-if="attempt.failure_timings" class="mt-3 sm:ml-9" :timing="attempt.failure_timings" />
          </li>
        </ol>
      </section>

      <section v-if="item.usage_write_error || (item.usage_write_state && item.usage_write_state !== 'persisted' && (item.usage_write_state !== 'pending' || !usageRows.length))" class="px-6 py-5">
        <SettingsNotice tone="warning">
          {{ t(`requests.usageWriteStates.${item.usage_write_state}`) }}
          <span v-if="item.usage_write_error" class="ml-1">{{ item.usage_write_error }}</span>
        </SettingsNotice>
      </section>

      <!-- Usage: model, tokens and cost, then the performance of the producing attempt. -->
      <section v-if="usageRows.length" class="px-6 py-5">
        <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-dark-50">{{ t('requests.usage') }}</h3>
        <ul class="divide-y divide-gray-100 rounded-control border border-gray-200 dark:divide-dark-700 dark:border-dark-600">
          <li v-for="usage in usageRows" :key="usage.id" class="px-4 py-3">
            <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
              <span class="flex min-w-0 items-center gap-1.5 font-medium text-gray-900 dark:text-dark-50">
                <ModelIcon :model="usage.model" size="16px" class="shrink-0" />
                <span class="truncate">{{ usage.model }}</span>
              </span>
              <span class="text-xs tabular-nums text-gray-500 dark:text-dark-300">
                {{ t('requests.tokens', { input: formatTokens(usage.input_tokens), output: formatTokens(usage.output_tokens) }) }}
              </span>
              <span class="ml-auto text-sm font-medium tabular-nums text-emerald-600 dark:text-emerald-400">{{ formatCost(usage.actual_cost) }}</span>
            </div>
            <dl
              v-if="usage.timings?.values.attempt_number || usage.duration_ms != null || usage.ttft_ms != null || usage.tps != null"
              class="mt-2 flex flex-wrap gap-x-5 gap-y-1 text-xs"
            >
              <div v-if="usage.timings?.values.attempt_number" class="flex gap-1.5">
                <dt class="text-gray-500 dark:text-dark-400">{{ t('requests.producedBy') }}</dt>
                <dd class="font-mono text-gray-900 dark:text-dark-100">#{{ usage.timings.values.attempt_number }}</dd>
              </div>
              <div v-if="usage.duration_ms != null" class="flex gap-1.5">
                <dt class="text-gray-500 dark:text-dark-400">{{ t('requests.upstreamDuration') }}</dt>
                <dd class="font-mono tabular-nums text-gray-900 dark:text-dark-100">{{ formatTimingMs(usage.duration_ms) }}</dd>
              </div>
              <div v-if="usage.ttft_ms != null" class="flex gap-1.5">
                <dt class="text-gray-500 dark:text-dark-400">{{ usage.timings?.ttft_mode === 'first_event' ? t('usage.latencyFirstEvent') : t('usage.latencyFirstToken') }}</dt>
                <dd class="font-mono tabular-nums text-gray-900 dark:text-dark-100">{{ formatTimingMs(usage.ttft_ms) }}</dd>
              </div>
              <div v-if="usage.tps != null" class="flex gap-1.5">
                <dt class="text-gray-500 dark:text-dark-400">TPS</dt>
                <dd class="font-mono tabular-nums text-gray-900 dark:text-dark-100">{{ usage.tps.toFixed(1) }}</dd>
              </div>
            </dl>
          </li>
        </ul>
      </section>

      <!-- 错误记录，点击打开错误详情 -->
      <section v-if="errorRows.length" class="px-6 py-5">
        <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-dark-50">{{ t('requests.errors') }}</h3>
        <ul class="divide-y divide-gray-100 overflow-hidden rounded-control border border-gray-200 dark:divide-dark-700 dark:border-dark-600">
          <li v-for="failure in errorRows" :key="failure.id">
            <button
              type="button"
              class="flex w-full items-center gap-4 px-4 py-3 text-left text-sm transition-colors hover:bg-gray-50 dark:hover:bg-dark-800"
              @click="emit('openError', failure.id)"
            >
              <span class="font-mono text-xs font-medium" :class="statusClass(failure.status_code)">HTTP {{ failure.status_code }}</span>
              <span class="text-gray-700 dark:text-dark-100">{{ phaseLabel(failure.phase) }}</span>
              <span class="font-mono text-xs text-gray-400 dark:text-dark-400">#{{ failure.id }}</span>
              <Icon name="chevronRight" size="sm" class="ml-auto text-gray-400 dark:text-dark-400" />
            </button>
          </li>
        </ul>
      </section>

      <!-- Related identifiers left after attempts show their own IDs; administrators only. -->
      <section v-if="listedAliases.length" class="px-6 py-5">
        <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-dark-50">{{ t('requests.aliases') }}</h3>
        <dl class="grid grid-cols-1 items-baseline gap-x-3 gap-y-1 text-xs sm:grid-cols-[9rem_minmax(0,1fr)] sm:gap-y-2">
          <template v-for="alias in listedAliases" :key="aliasKey(alias)">
            <dt class="mt-1 text-gray-500 first:mt-0 dark:text-dark-400 sm:mt-0">{{ aliasLabel(alias.kind) }}</dt>
            <dd class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
              <RequestIdLink :value="alias.value" :link="false" full />
              <span v-if="aliasMeta(alias)" class="text-gray-400 dark:text-dark-500">{{ aliasMeta(alias) }}</span>
            </dd>
          </template>
        </dl>
      </section>
    </div>
  </article>
</template>
