<template>
  <div v-if="queryEnabled" class="space-y-1" data-testid="provider-upstream-usage">
    <!-- The loading skeleton follows the mobile result alignment so replacing it with usage content keeps the value column on the same side. -->
    <div
      v-if="queryEnabled && loading"
      class="flex flex-col items-end gap-1 lg:items-start"
      data-testid="upstream-usage-skeleton"
    >
      <div class="skeleton h-3 w-28"></div>
      <div class="skeleton h-3 w-36"></div>
    </div>
    <div
      v-else-if="queryEnabled && error"
      class="flex items-center justify-end gap-1 text-xs text-amber-600 dark:text-amber-400 lg:justify-start"
    >
      <span class="truncate" :title="error.message || error.code || ''">
        {{ errorLabel }}
      </span>
    </div>
    <div v-else-if="queryEnabled && normalizedUsage" class="space-y-1">
      <div
        v-if="balanceLabel"
        class="min-w-0 break-words text-xs leading-tight text-gray-600 dark:text-gray-300"
        :title="balanceTitle"
      >
        {{ balanceLabel }}
      </div>
      <div v-for="limit in visibleLimits" :key="limit.name" class="space-y-0.5">
        <UsageProgressBar
          :label="limit.name"
          :utilization="limit.utilization"
          :resets-at="limit.resetAt"
          :wide-label="limit.wideLabel"
          color="indigo"
        />
        <div
          v-if="limit.hasAmount && limit.showAmount"
          class="min-w-0 break-words pl-9 text-xs leading-tight text-gray-400 dark:text-gray-500"
          :title="limitAmountTitle(limit)"
        >
          {{ formatAmount(limit.remaining, normalizedUsage?.unit) }} /
          {{ formatAmount(limit.limit, normalizedUsage?.unit) }}
        </div>
      </div>
      <div
        v-if="subscriptionLabel || subscriptionExpiry"
        data-testid="upstream-subscription-row"
        class="flex flex-wrap items-center justify-end gap-1 text-xs text-gray-500 dark:text-gray-400 lg:justify-start"
      >
        <span
          v-if="subscriptionLabel"
          class="rounded-compact bg-sky-50 px-1.5 py-0.5 text-sky-700 dark:bg-sky-900/30 dark:text-sky-300"
        >
          {{ subscriptionLabel }}
        </span>
        <span v-if="subscriptionRemainingLabel">{{ subscriptionRemainingLabel }}</span>
        <span v-if="subscriptionExpiry">{{ subscriptionExpiry }}</span>
      </div>
      <div
        class="text-xs text-gray-400 dark:text-gray-500"
        :title="effectiveResult?.observed_at || ''"
      >
        {{ t('admin.providers.upstreamUsage.observedAt') }}
        {{ formatObservedAt(effectiveResult?.observed_at || '') }}
      </div>
    </div>
    <div
      v-if="queryEnabled && showQueryButton"
      class="mt-0.5 flex items-center justify-end gap-1.5 lg:justify-start"
    >
      <button
        type="button"
        class="inline-flex items-center gap-0.5 rounded-compact px-1.5 py-0.5 text-xs font-medium text-primary-600 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-500 dark:hover:bg-primary-500/8"
        :disabled="loading"
        :title="t('admin.providers.upstreamUsage.query')"
        :aria-label="t('admin.providers.upstreamUsage.query')"
        @click="query(true)"
      >
        <Icon name="refresh" size="xs" :class="{ 'animate-spin': loading }" :stroke-width="2" />
        {{ t('admin.providers.usageWindow.activeQuery') }}
      </button>
    </div>
    <!-- The owning provider usage cell supplies the placeholder when no query observation is available. -->
    <div
      v-if="queryEnabled && !normalizedUsage && !error && !loading && showQueryButton"
      class="h-3"
      aria-hidden="true"
    ></div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type {
  Provider,
  UpstreamUsageLimit,
  UpstreamUsageQueryError,
  UpstreamUsageQueryResult,
} from '@/api/admin/providers.types'
import Icon from '@/components/icons/Icon.vue'
import UsageProgressBar from './UsageProgressBar.vue'
import { isUpstreamUsageQueryEnabled } from '@/utils/upstreamUsage'

/**
 * ProviderUpstreamUsageCell.vue renders manually queried upstream balances, limits, and
 * subscription observations supplied by the provider page. A valid saved CN monitor snapshot
 * can supply initial data until a manual result is available. Mounting the cell does not itself
 * request upstream usage, and saved query configuration controls whether upstream content is
 * displayed.
 *
 * Nested normalized usage takes precedence over legacy top-level fields. Balance and quota
 * observations retain their reported units, including percentage-only coding windows. For New
 * API and Zivv, matching balance and limit amounts are suppressed using a small comparison
 * tolerance, while distinct quota values remain visible. Missing amounts use placeholders, and
 * explicit zero values remain measurable observations.
 */

/**
 * props combines the provider record with parent-owned query state and an optional request
 * callback for explicit administrator refresh actions.
 */
const props = withDefaults(
  defineProps<{
    provider: Provider // current provider row record
    /**
     * Manual results take precedence over a saved monitor snapshot whenever the parent supplies
     * a non-null response.
     */
    result?: UpstreamUsageQueryResult | null
    error?: UpstreamUsageQueryError | null // optional parent query failure
    loading?: boolean // parent query request activity
    showQueryButton?: boolean // show inline query action
    /**
     * Optional callback delegates query execution to the parent, which owns loading, result,
     * and error state.
     */
    request?:
      | ((
          provider: Provider,
          options?: {
            force?: boolean // bypass parent query cache
          },
        ) => void)
      | null
  }>(),
  {
    result: null,
    error: null,
    loading: false,
    showQueryButton: true,
    request: null,
  },
)

/**
 * Translation supplies usage labels and error feedback while all amounts retain the upstream
 * unit provided in the normalized observation.
 */
const { t } = useI18n()

/**
 * queryEnabled hides upstream observations when saved configuration disables queries; the
 * parent remains responsible for displaying local usage statistics.
 */
const queryEnabled = computed(() => isUpstreamUsageQueryEnabled(props.provider))

/**
 * monitorResult accepts a version-one CN snapshot with an adapter and parseable observation
 * time, then exposes its stored amounts before a manual result exists.
 */
const monitorResult = computed<UpstreamUsageQueryResult | null>(() => {
  if (!['kimi', 'zhipu', 'deepseek'].includes(props.provider.platform)) return null
  const raw = props.provider.extra?.cn_usage_monitor_snapshot
  if (!raw || typeof raw !== 'object') return null
  const snapshot = raw as Record<string, unknown>
  if (
    snapshot.version !== 1 ||
    typeof snapshot.adapter !== 'string' ||
    typeof snapshot.observed_at !== 'string' ||
    !Number.isFinite(Date.parse(snapshot.observed_at))
  )
    return null
  return {
    provider_id: props.provider.id,
    adapter: snapshot.adapter,
    observed_at: snapshot.observed_at,
    provider: typeof snapshot.provider === 'string' ? snapshot.provider : props.provider.platform,
    mode: typeof snapshot.mode === 'string' ? snapshot.mode : '',
    unit: typeof snapshot.unit === 'string' ? snapshot.unit : undefined,
    balance: snapshot.balance as UpstreamUsageQueryResult['balance'],
    balances: snapshot.balances as UpstreamUsageQueryResult['balances'],
    available: typeof snapshot.available === 'boolean' ? snapshot.available : undefined,
    limits: snapshot.limits as UpstreamUsageQueryResult['limits'],
    subscription: snapshot.subscription as UpstreamUsageQueryResult['subscription'],
    expires_at: typeof snapshot.expires_at === 'string' ? snapshot.expires_at : undefined,
  }
})

/**
 * effectiveResult chooses a present manual result before the saved monitor snapshot, keeping
 * manual query ownership with the parent page.
 */
const effectiveResult = computed(() => props.result ?? monitorResult.value)

/**
 * formatNumber renders finite observations with compact million or billion suffixes when
 * enabled. Missing or nonfinite values use a placeholder, and small nonzero values receive up
 * to four fractional digits.
 */
const formatNumber = (value: number | null | undefined, compact = true) => {
  if (value == null || !Number.isFinite(value)) return '-'
  const absolute = Math.abs(value)
  // Abbreviate only million-scale values; smaller balances retain localized numeric formatting.
  if (compact && absolute >= 1_000_000) {
    const units = [
      { divisor: 1_000_000_000, suffix: 'B' },
      { divisor: 1_000_000, suffix: 'M' },
    ]
    const unit = units.find((item) => absolute >= item.divisor)
    if (unit) {
      // Trim compact suffix decimals while retaining up to three places.
      const compactValue = (value / unit.divisor).toFixed(3).replace(/\.?0+$/, '')
      return `${compactValue}${unit.suffix}`
    }
  }
  // Retain smaller nonzero amounts before rounding them away.
  const maximumFractionDigits = absolute > 0 && absolute < 0.01 ? 4 : 2
  return new Intl.NumberFormat(undefined, {
    maximumFractionDigits,
    minimumFractionDigits: 0,
  }).format(value)
}

/**
 * formatExactNumber renders a finite observation with up to four fractional digits and no
 * suffix abbreviation. Missing or nonfinite values use a placeholder, while valid zero remains
 * visible.
 */
const formatExactNumber = (value: number | null | undefined) => {
  if (value == null || !Number.isFinite(value)) return '-'
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 4 }).format(value)
}

/**
 * formatAmount appends the supplied upstream unit to a compact numeric observation. Null or
 * undefined amounts use a bare placeholder; other values retain the numeric formatter result
 * and optional unit.
 */
const formatAmount = (value: number | null | undefined, unit?: string) => {
  if (value == null) return '-'
  const suffix = unit ? ` ${unit}` : ''
  return `${formatNumber(value)}${suffix}`
}

/**
 * formatExactAmount appends the reported unit to the detailed numeric formatter result. Missing
 * amounts use a bare placeholder, and detailed values retain up to four fractional digits
 * without compact suffixes.
 */
const formatExactAmount = (value: number | null | undefined, unit?: string) => {
  if (value == null) return '-'
  const suffix = unit ? ` ${unit}` : ''
  return `${formatExactNumber(value)}${suffix}`
}

/**
 * normalizedUsage prefers the nested usage object and adapts older top-level response fields
 * into the same display shape when it is absent.
 */
const normalizedUsage = computed(() => {
  const result = effectiveResult.value
  if (!result) return null
  return (
    result.usage ?? {
      provider: result.provider || '',
      mode: result.mode || '',
      unit: result.unit,
      balance: result.balance,
      balances: result.balances,
      available: result.available,
      limits: result.limits,
      subscription: result.subscription,
      expires_at: result.expires_at,
    }
  )
})

/**
 * hasSeparateQuota identifies New API and Zivv observations whose balance and key-quota fields
 * may otherwise repeat the same amounts in separate rows.
 */
const hasSeparateQuota = computed(
  () =>
    props.result?.adapter === 'new_api' ||
    props.result?.adapter === 'zivv' ||
    normalizedUsage.value?.provider === 'new_api' ||
    normalizedUsage.value?.provider === 'zivv',
)

/**
 * amountsEqual compares two present amounts using absolute or relative tolerance. Missing
 * values never compare equal, and the tolerance scales with the larger magnitude while
 * retaining a minimum absolute margin.
 */
const amountsEqual = (left?: number, right?: number) => {
  if (left == null || right == null) return false
  return (
    // Accept small absolute or proportional rounding differences between duplicate observations.
    Math.abs(left - right) <=
    Math.max(0.000001, Math.max(Math.abs(left), Math.abs(right)) * 0.000001)
  )
}

/**
 * isDuplicateLimit reports whether a New API or Zivv limit repeats all three balance amounts.
 * Missing balance data or any unmatched used, total, or remaining value keeps that limit
 * visible.
 */
const isDuplicateLimit = (limit: UpstreamUsageLimit) => {
  if (!hasSeparateQuota.value || !normalizedUsage.value?.balance) return false
  const balance = normalizedUsage.value.balance
  return (
    amountsEqual(limit.used, balance.used) &&
    amountsEqual(limit.limit, balance.total) &&
    amountsEqual(limit.remaining, balance.remaining)
  )
}

/**
 * limitDisplayName localizes recognized total-quota names for adapters that expose separate key
 * quota. Other names remain as reported so unfamiliar upstream limit categories retain their
 * supplied labels.
 */
const limitDisplayName = (name: string) => {
  const normalizedName = name.toLowerCase()
  if (
    hasSeparateQuota.value &&
    (normalizedName === 'hard_limit' ||
      normalizedName === 'token_quota' ||
      normalizedName === 'key_quota')
  ) {
    return t('admin.providers.upstreamUsage.totalLimit')
  }
  return name
}

/**
 * Balance labels and tooltips prefer a list of currency balances, then the single balance
 * observation with its reported unit.
 */
const balanceEntries = computed(() => normalizedUsage.value?.balances ?? [])

const balanceLabel = computed(() => {
  const usage = normalizedUsage.value
  if (!usage) return ''
  if (balanceEntries.value.length > 0) {
    return balanceEntries.value
      .map((entry) => `${entry.currency} ${formatNumber(entry.remaining, false)}`)
      .join(' · ')
  }
  if (!usage.balance) return ''
  const balance = usage.balance
  const unit = usage.unit
  return t('admin.providers.upstreamUsage.remainingLine', {
    remaining: formatAmount(balance.remaining, unit),
  })
})

const balanceTitle = computed(() => {
  if (balanceEntries.value.length > 0) {
    return balanceEntries.value
      .map((entry) => `${entry.currency} ${formatExactNumber(entry.remaining)}`)
      .join(' · ')
  }
  const balance = normalizedUsage.value?.balance
  if (!balance) return ''
  const unit = normalizedUsage.value?.unit
  return t('admin.providers.upstreamUsage.remainingTooltip', {
    remaining: formatExactAmount(balance.remaining, unit),
  })
})

/**
 * isPercentUsage marks percentage-only quota windows so the progress bar conveys utilization
 * without repeating a separate raw amount line.
 */
const isPercentUsage = computed(() => normalizedUsage.value?.unit === 'PERCENT')

/**
 * allLimits combines provider and subscription limits, and visibleLimits derives labels,
 * optional amounts, bounded bar geometry, and reset timestamps for display.
 */
const allLimits = computed(() => {
  const usage = normalizedUsage.value
  return [...(usage?.limits ?? []), ...(usage?.subscription?.limits ?? [])]
})

const visibleLimits = computed(() =>
  allLimits.value.map((limit) => ({
    name: limitDisplayName(limit.name),
    used: limit.used,
    limit: limit.limit,
    remaining: limit.remaining,
    hasAmount: limit.used != null || limit.limit != null || limit.remaining != null,
    // Percentage windows already expose their observation through the bar label; suppress a
    // duplicate raw used-versus-limit amount in that representation.
    showAmount: !isPercentUsage.value && !isDuplicateLimit(limit),
    wideLabel: limitDisplayName(limit.name).length > 4,
    utilization:
      limit.limit && limit.limit > 0 && limit.used != null
        ? Math.min(100, Math.max(0, (limit.used / limit.limit) * 100))
        : 0,
    resetAt: limit.reset_at ?? null,
  })),
)

/**
 * Subscription text distinguishes unlimited plans, separate key quotas, and ordinary remaining
 * balances while retaining the upstream plan name.
 */
const subscriptionLabel = computed(() => {
  const subscription = normalizedUsage.value?.subscription
  if (!subscription) return ''
  if (subscription.unlimited)
    return t('admin.providers.upstreamUsage.unlimited', { plan: subscription.plan_name })
  return subscription.plan_name
})

const subscriptionRemainingLabel = computed(() => {
  const subscription = normalizedUsage.value?.subscription
  if (!subscription || subscription.unlimited || subscription.remaining == null) return ''
  // Separate key-quota rows already represent this allowance, so omit the generic subscription
  // remainder that would repeat the same quota interpretation.
  if (hasSeparateQuota.value) return ''
  return t('admin.providers.upstreamUsage.subscriptionRemaining', {
    remaining: formatAmount(subscription.remaining, normalizedUsage.value?.unit),
  })
})

/**
 * limitAmountTitle formats remaining and maximum quota observations for the detailed tooltip.
 */
const limitAmountTitle = (limit: Pick<UpstreamUsageLimit, 'remaining' | 'limit'>) => {
  const unit = normalizedUsage.value?.unit
  return t('admin.providers.upstreamUsage.limitTooltip', {
    remaining: formatExactAmount(limit.remaining, unit),
    limit: formatExactAmount(limit.limit, unit),
  })
}

/**
 * subscriptionExpiry prefers the subscription deadline over the overall usage deadline and
 * formats whichever timestamp is present for the display.
 */
const subscriptionExpiry = computed(() => {
  const expiresAt =
    normalizedUsage.value?.subscription?.expires_at || normalizedUsage.value?.expires_at
  if (!expiresAt) return ''
  return t('admin.providers.upstreamUsage.expiresAt', { time: formatObservedAt(expiresAt) })
})

/**
 * errorLabel prefers a translated error code, then the supplied message, and finally the
 * localized generic query-failure label.
 */
const errorLabel = computed(() => {
  const code = props.error?.code
  if (code) return t(`admin.providers.upstreamUsage.errors.${code}`, code)
  return props.error?.message || t('admin.providers.upstreamUsage.queryFailed')
})

/**
 * formatObservedAt renders a valid timestamp using the browser locale. Invalid timestamps
 * remain unchanged so the administrator can inspect upstream text that the browser cannot
 * interpret as a date.
 */
const formatObservedAt = (value: string) => {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

/**
 * query delegates an upstream usage request with the supplied force flag. An absent callback
 * produces no work; the parent owns result publication, loading feedback, and query error
 * state.
 */
const query = (force: boolean) => {
  props.request?.(props.provider, { force })
}
</script>
