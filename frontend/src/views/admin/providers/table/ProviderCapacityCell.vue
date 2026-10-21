<template>
  <div class="flex flex-col gap-0.5">
    <!-- Concurrent request slots. -->
    <CapacityBadge
      :color-class="concurrencyClass"
      :current="currentConcurrency"
      :max="provider.concurrency"
    >
      <Icon name="grid" size="md" class="h-2.5 w-2.5" />
    </CapacityBadge>

    <!-- Five-hour window spending limit. -->
    <CapacityBadge
      v-if="showWindowCost"
      :color-class="windowCostClass"
      :tooltip="windowCostTooltip"
      :current="'$' + formatCost(currentWindowCost)"
      :max="'$' + formatCost(provider.window_cost_limit)"
    >
      <Icon name="dollar" size="md" class="h-2.5 w-2.5" />
    </CapacityBadge>

    <!-- Active session capacity. -->
    <CapacityBadge
      v-if="showSessionLimit"
      :color-class="sessionLimitClass"
      :tooltip="sessionLimitTooltip"
      :current="activeSessions"
      :max="provider.max_sessions!"
    >
      <Icon name="users" size="md" class="h-2.5 w-2.5" />
    </CapacityBadge>

    <!-- Request-rate capacity. -->
    <CapacityBadge
      v-if="showRpmLimit"
      :color-class="rpmClass"
      :tooltip="rpmTooltip"
      :current="currentRPM"
      :max="provider.base_rpm!"
      :suffix="rpmStrategyTag"
    >
      <Icon name="clock" size="md" class="h-2.5 w-2.5" />
    </CapacityBadge>

    <!-- Eligible local spending quotas. -->
    <QuotaBadge
      v-if="showDailyQuota"
      :used="provider.quota_daily_used ?? 0"
      :limit="provider.quota_daily_limit!"
      label="D"
    />
    <QuotaBadge
      v-if="showWeeklyQuota"
      :used="provider.quota_weekly_used ?? 0"
      :limit="provider.quota_weekly_limit!"
      label="W"
    />
    <QuotaBadge
      v-if="showMonthlyQuota"
      :used="provider.quota_monthly_used ?? 0"
      :limit="provider.quota_monthly_limit!"
      label="M"
    />
    <QuotaBadge
      v-if="showTotalQuota"
      :used="provider.quota_used ?? 0"
      :limit="provider.quota_limit!"
    />
  </div>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Provider } from '@/api/admin/providers.types'
import CapacityBadge from '@/views/admin/providers/table/CapacityBadge.vue'
import QuotaBadge from '@/views/admin/providers/table/QuotaBadge.vue'

/**
 * ProviderCapacityCell.vue displays provider concurrency, Anthropic window-cost and session
 * capacity, request-rate tiers, and local spending quotas. Platform and authentication
 * predicates select applicable badges. Missing current observations use zero in these capacity
 * displays, while positive configured limits determine whether optional capacity dimensions are
 * shown.
 *
 * Window cost and tiered RPM distinguish normal admission from the sticky reserve and
 * exhaustion; sticky-exempt RPM uses a different upper-state rule. Warning colors begin at
 * eighty percent of the base limit. Daily, weekly, monthly, and cumulative spending badges are
 * independently enabled for API-key and Bedrock providers, using the effective usage already
 * supplied on the provider record.
 */

/**
 * props supplies provider capacity observations and configured limits, with eligibility
 * predicates selecting the applicable badges for this table row.
 */
const props = defineProps<{
  provider: Provider // current provider capacity record
}>()

/**
 * The translator provides capacity explanations for normal, sticky-only, full, and exhausted
 * states derived from the configured limits.
 */
const { t } = useI18n()

/**
 * Concurrency state treats a missing observation as zero, uses red at configured capacity, and
 * yellow for nonzero activity below capacity.
 */
const currentConcurrency = computed(() => props.provider.current_concurrency || 0)

const concurrencyClass = computed(() => {
  const current = currentConcurrency.value
  const max = props.provider.concurrency
  if (current >= max) return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
  if (current > 0) return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
})

/**
 * Anthropic OAuth and setup-token records can display a positive window-cost limit, with
 * current cost and sticky reserve controlling badge severity.
 */
const isAnthropicOAuthOrSetupToken = computed(
  () =>
    props.provider.platform === 'anthropic' &&
    (props.provider.type === 'oauth' || props.provider.type === 'setup-token'),
)

const showWindowCost = computed(
  () =>
    isAnthropicOAuthOrSetupToken.value &&
    props.provider.window_cost_limit != null &&
    props.provider.window_cost_limit > 0,
)

const currentWindowCost = computed(() => props.provider.current_window_cost ?? 0)

const windowCostClass = computed(() => {
  if (!showWindowCost.value) return ''
  const current = currentWindowCost.value
  const limit = props.provider.window_cost_limit || 0
  const reserve = props.provider.window_cost_sticky_reserve || 10
  if (current >= limit + reserve)
    return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
  if (current >= limit)
    return 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
  // Warn when spending reaches eighty percent of base capacity.
  if (current >= limit * 0.8)
    return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
})

const windowCostTooltip = computed(() => {
  if (!showWindowCost.value) return ''
  const current = currentWindowCost.value
  const limit = props.provider.window_cost_limit || 0
  const reserve = props.provider.window_cost_sticky_reserve || 10
  if (current >= limit + reserve) return t('admin.providers.capacity.windowCost.blocked')
  if (current >= limit) return t('admin.providers.capacity.windowCost.stickyOnly')
  return t('admin.providers.capacity.windowCost.normal')
})

/**
 * Session capacity applies to eligible Anthropic records with a positive maximum; missing
 * active sessions use zero in colors and hints.
 */
const showSessionLimit = computed(
  () =>
    isAnthropicOAuthOrSetupToken.value &&
    props.provider.max_sessions != null &&
    props.provider.max_sessions > 0,
)

const activeSessions = computed(() => props.provider.active_sessions ?? 0)

const sessionLimitClass = computed(() => {
  if (!showSessionLimit.value) return ''
  const current = activeSessions.value
  const max = props.provider.max_sessions || 0
  if (current >= max) return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
  // Warn before the active-session count reaches its configured maximum.
  if (current >= max * 0.8)
    return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
})

const sessionLimitTooltip = computed(() => {
  if (!showSessionLimit.value) return ''
  const current = activeSessions.value
  const max = props.provider.max_sessions || 0
  const idle = props.provider.session_idle_timeout_minutes || 5
  if (current >= max) return t('admin.providers.capacity.sessions.full', { idle })
  return t('admin.providers.capacity.sessions.normal', { idle })
})

/**
 * RPM capacity combines the configured base, strategy, current observation, and sticky buffer
 * to distinguish normal, sticky-only, and blocked states.
 */
const showRpmLimit = computed(
  () =>
    isAnthropicOAuthOrSetupToken.value &&
    props.provider.base_rpm != null &&
    props.provider.base_rpm > 0,
)

const currentRPM = computed(() => props.provider.current_rpm ?? 0)
const rpmStrategy = computed(() => props.provider.rpm_strategy || 'tiered')
const rpmStrategyTag = computed(() => (rpmStrategy.value === 'sticky_exempt' ? '[S]' : '[T]'))

/**
 * rpmBuffer uses an explicitly supplied buffer, including zero; otherwise positive base RPM
 * derives a minimum-one buffer from one fifth of base.
 */
const rpmBuffer = computed(() => {
  const base = props.provider.base_rpm || 0
  // Derive a minimum-one reserve when an explicit sticky buffer is absent.
  return props.provider.rpm_sticky_buffer ?? (base > 0 ? Math.max(1, Math.floor(base / 5)) : 0)
})

const rpmClass = computed(() => {
  if (!showRpmLimit.value) return ''
  const current = currentRPM.value
  const base = props.provider.base_rpm ?? 0
  const buffer = rpmBuffer.value
  if (rpmStrategy.value === 'tiered') {
    if (current >= base + buffer)
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
    if (current >= base)
      return 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
  } else {
    if (current >= base)
      return 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
  }
  // Warn when current RPM approaches the base admission threshold.
  if (current >= base * 0.8)
    return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
})

const rpmTooltip = computed(() => {
  if (!showRpmLimit.value) return ''
  const current = currentRPM.value
  const base = props.provider.base_rpm ?? 0
  const buffer = rpmBuffer.value
  if (rpmStrategy.value === 'tiered') {
    if (current >= base + buffer) return t('admin.providers.capacity.rpm.tieredBlocked', { buffer })
    if (current >= base) return t('admin.providers.capacity.rpm.tieredStickyOnly', { buffer })
    // Warn when current RPM approaches the base admission threshold.
    if (current >= base * 0.8) return t('admin.providers.capacity.rpm.tieredWarning')
    return t('admin.providers.capacity.rpm.tieredNormal')
  } else {
    if (current >= base) return t('admin.providers.capacity.rpm.stickyExemptOver')
    // Warn when current RPM approaches the base admission threshold.
    if (current >= base * 0.8) return t('admin.providers.capacity.rpm.stickyExemptWarning')
    return t('admin.providers.capacity.rpm.stickyExemptNormal')
  }
})

/**
 * formatCost renders a current window cost with two fractional digits. Null or undefined
 * observations display zero, matching the capacity badge treatment of missing current usage
 * rather than an unknown placeholder.
 */
const formatCost = (value: number | null | undefined) => {
  if (value === null || value === undefined) return '0'
  return value.toFixed(2)
}

/**
 * Local spending badges apply to API-key and Bedrock records, with each positive daily, weekly,
 * monthly, or total limit displayed independently.
 */
const isQuotaEligible = computed(
  () => props.provider.type === 'apikey' || props.provider.type === 'bedrock',
)

const showDailyQuota = computed(
  () =>
    isQuotaEligible.value &&
    props.provider.quota_daily_limit != null &&
    props.provider.quota_daily_limit > 0,
)
const showWeeklyQuota = computed(
  () =>
    isQuotaEligible.value &&
    props.provider.quota_weekly_limit != null &&
    props.provider.quota_weekly_limit > 0,
)
/**
 * showMonthlyQuota exposes a monthly spending badge when the eligible provider has a positive
 * monthly limit, independently of other quota dimensions.
 */
const showMonthlyQuota = computed(
  () =>
    isQuotaEligible.value &&
    props.provider.quota_monthly_limit != null &&
    props.provider.quota_monthly_limit > 0,
)
const showTotalQuota = computed(
  () =>
    isQuotaEligible.value && props.provider.quota_limit != null && props.provider.quota_limit > 0,
)
</script>
