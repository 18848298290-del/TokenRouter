<template>
  <div class="flex flex-col gap-0.5">
    <!-- 并发槽位 -->
    <CapacityBadge :color-class="concurrencyClass" :current="currentConcurrency" :max="provider.concurrency">
      <Icon name="grid" size="md" class="h-2.5 w-2.5" />
    </CapacityBadge>

    <!-- 5h窗口费用限制 -->
    <CapacityBadge v-if="showWindowCost" :color-class="windowCostClass" :tooltip="windowCostTooltip" :current="'$' + formatCost(currentWindowCost)" :max="'$' + formatCost(provider.window_cost_limit)">
      <Icon name="dollar" size="md" class="h-2.5 w-2.5" />
    </CapacityBadge>

    <!-- 会话数量限制 -->
    <CapacityBadge v-if="showSessionLimit" :color-class="sessionLimitClass" :tooltip="sessionLimitTooltip" :current="activeSessions" :max="provider.max_sessions!">
      <Icon name="users" size="md" class="h-2.5 w-2.5" />
    </CapacityBadge>

    <!-- RPM 限制 -->
    <CapacityBadge v-if="showRpmLimit" :color-class="rpmClass" :tooltip="rpmTooltip" :current="currentRPM" :max="provider.base_rpm!" :suffix="rpmStrategyTag">
      <Icon name="clock" size="md" class="h-2.5 w-2.5" />
    </CapacityBadge>

    <!-- API Key 提供商配额限制 -->
    <QuotaBadge v-if="showDailyQuota" :used="provider.quota_daily_used ?? 0" :limit="provider.quota_daily_limit!" label="D" />
    <QuotaBadge v-if="showWeeklyQuota" :used="provider.quota_weekly_used ?? 0" :limit="provider.quota_weekly_limit!" label="W" />
    <QuotaBadge v-if="showMonthlyQuota" :used="provider.quota_monthly_used ?? 0" :limit="provider.quota_monthly_limit!" label="M" />
    <QuotaBadge v-if="showTotalQuota" :used="provider.quota_used ?? 0" :limit="provider.quota_limit!" />
  </div>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Provider } from '@/types'
import CapacityBadge from '@/components/provider/CapacityBadge.vue'
import QuotaBadge from '@/components/provider/QuotaBadge.vue'

// ProviderCapacityCell.vue displays provider request capacity and local quotas.
// Platform and authentication eligibility determine which controls are meaningful;
// enabled daily, weekly, monthly, and total spending limits appear as compact
// badges alongside the provider's configured capacity information.

const props = defineProps<{
  provider: Provider
}>()

const { t } = useI18n()

// ====== 并发 ======
const currentConcurrency = computed(() => props.provider.current_concurrency || 0)

const concurrencyClass = computed(() => {
  const current = currentConcurrency.value
  const max = props.provider.concurrency
  if (current >= max) return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
  if (current > 0) return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
})

// ====== 窗口费用 ======
const isAnthropicOAuthOrSetupToken = computed(() =>
  props.provider.platform === 'anthropic' &&
  (props.provider.type === 'oauth' || props.provider.type === 'setup-token')
)

const showWindowCost = computed(() =>
  isAnthropicOAuthOrSetupToken.value &&
  props.provider.window_cost_limit != null &&
  props.provider.window_cost_limit > 0
)

const currentWindowCost = computed(() => props.provider.current_window_cost ?? 0)

const windowCostClass = computed(() => {
  if (!showWindowCost.value) return ''
  const current = currentWindowCost.value
  const limit = props.provider.window_cost_limit || 0
  const reserve = props.provider.window_cost_sticky_reserve || 10
  if (current >= limit + reserve) return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
  if (current >= limit) return 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
  if (current >= limit * 0.8) return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
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

// ====== 会话限制 ======
const showSessionLimit = computed(() =>
  isAnthropicOAuthOrSetupToken.value &&
  props.provider.max_sessions != null &&
  props.provider.max_sessions > 0
)

const activeSessions = computed(() => props.provider.active_sessions ?? 0)

const sessionLimitClass = computed(() => {
  if (!showSessionLimit.value) return ''
  const current = activeSessions.value
  const max = props.provider.max_sessions || 0
  if (current >= max) return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
  if (current >= max * 0.8) return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
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

// ====== RPM ======
const showRpmLimit = computed(() =>
  isAnthropicOAuthOrSetupToken.value &&
  props.provider.base_rpm != null &&
  props.provider.base_rpm > 0
)

const currentRPM = computed(() => props.provider.current_rpm ?? 0)
const rpmStrategy = computed(() => props.provider.rpm_strategy || 'tiered')
const rpmStrategyTag = computed(() => rpmStrategy.value === 'sticky_exempt' ? '[S]' : '[T]')

const rpmBuffer = computed(() => {
  const base = props.provider.base_rpm || 0
  return props.provider.rpm_sticky_buffer ?? (base > 0 ? Math.max(1, Math.floor(base / 5)) : 0)
})

const rpmClass = computed(() => {
  if (!showRpmLimit.value) return ''
  const current = currentRPM.value
  const base = props.provider.base_rpm ?? 0
  const buffer = rpmBuffer.value
  if (rpmStrategy.value === 'tiered') {
    if (current >= base + buffer) return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
    if (current >= base) return 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
  } else {
    if (current >= base) return 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
  }
  if (current >= base * 0.8) return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
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
    if (current >= base * 0.8) return t('admin.providers.capacity.rpm.tieredWarning')
    return t('admin.providers.capacity.rpm.tieredNormal')
  } else {
    if (current >= base) return t('admin.providers.capacity.rpm.stickyExemptOver')
    if (current >= base * 0.8) return t('admin.providers.capacity.rpm.stickyExemptWarning')
    return t('admin.providers.capacity.rpm.stickyExemptNormal')
  }
})

// 格式化费用显示
const formatCost = (value: number | null | undefined) => {
  if (value === null || value === undefined) return '0'
  return value.toFixed(2)
}

// ====== 配额 ======
const isQuotaEligible = computed(() => props.provider.type === 'apikey' || props.provider.type === 'bedrock')

const showDailyQuota = computed(() =>
  isQuotaEligible.value && props.provider.quota_daily_limit != null && props.provider.quota_daily_limit > 0
)
const showWeeklyQuota = computed(() =>
  isQuotaEligible.value && props.provider.quota_weekly_limit != null && props.provider.quota_weekly_limit > 0
)
// showMonthlyQuota reports whether the provider has an eligible positive
// monthly spending limit.
const showMonthlyQuota = computed(() =>
  isQuotaEligible.value && props.provider.quota_monthly_limit != null && props.provider.quota_monthly_limit > 0
)
const showTotalQuota = computed(() =>
  isQuotaEligible.value && props.provider.quota_limit != null && props.provider.quota_limit > 0
)
</script>
