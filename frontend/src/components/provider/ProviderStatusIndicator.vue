<template>
  <div class="flex items-center gap-2">
    <!-- Rate Limit Display (429) - Two-line layout -->
    <div v-if="isRateLimited" class="flex flex-col items-center gap-1">
      <span class="badge text-xs badge-warning">{{ t('admin.providers.status.rateLimited') }}</span>
      <span class="text-xs text-gray-400 dark:text-gray-500">{{ rateLimitResumeText }}</span>
    </div>

    <!-- Overload Display (529) - Two-line layout -->
    <div v-else-if="isOverloaded" class="flex flex-col items-center gap-1">
      <span class="badge text-xs badge-danger">{{ t('admin.providers.status.overloaded') }}</span>
      <span class="text-xs text-gray-400 dark:text-gray-500">{{ overloadCountdown }}</span>
    </div>

    <!-- Main Status Badge (shown when not rate limited/overloaded) -->
    <template v-else>
      <div v-if="isTempUnschedulable" class="flex flex-col items-center gap-1">
        <button
          type="button"
          :class="['badge text-xs', statusClass, 'cursor-pointer']"
          :title="t('admin.providers.status.viewTempUnschedDetails')"
          @click="handleTempUnschedClick"
        >
          {{ statusText }}
        </button>
        <span class="max-w-[180px] text-center text-xs leading-4 text-gray-500 dark:text-gray-400">
          {{ tempUnschedRecoveryText }}
        </span>
      </div>
      <span v-else :class="['badge text-xs', statusClass]">
        {{ statusText }}
      </span>
    </template>

    <!-- Error Info Indicator -->
    <div v-if="hasError && provider.error_message" class="group/error relative">
      <Icon
        name="questionCircle"
        size="sm"
        class="h-4 w-4 cursor-help text-red-500 transition-colors hover:text-red-600 dark:text-red-400 dark:hover:text-red-300"
      />
      <!-- Tooltip - 向下显示 -->
      <div
        class="invisible absolute left-0 top-full z-tooltip mt-1.5 min-w-[200px] max-w-[300px] tooltip-panel rounded-control px-3 py-2 text-xs opacity-0 shadow-xl transition duration-normal group-hover/error:visible group-hover/error:opacity-100"
      >
        <div class="whitespace-pre-wrap break-words leading-relaxed text-gray-300">
          {{ provider.error_message }}
        </div>
        <!-- 上方小三角 -->
        <div
          class="tooltip-caret -top-1 left-3 border-l border-t"
        ></div>
      </div>
    </div>

    <!-- Rate Limit Indicator (429) -->
    <div v-if="isRateLimited" class="group relative">
      <span
        class="inline-flex items-center gap-1 rounded-compact bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-400"
      >
        <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
        429
      </span>
      <!-- Tooltip -->
      <div
        class="pointer-events-none absolute bottom-full left-1/2 z-50 mb-2 w-56 -translate-x-1/2 whitespace-normal tooltip-panel rounded-compact px-3 py-2 text-center text-xs leading-relaxed opacity-0 transition-opacity group-hover:opacity-100"
      >
        {{ t('admin.providers.status.rateLimitedUntil', { time: formatDateTime(provider.rate_limit_reset_at) }) }}
        <div
          class="tooltip-caret -bottom-1 left-1/2 -translate-x-1/2 border-b border-r"
        ></div>
      </div>
    </div>

    <!-- Model Status Indicators (普通限流 / 超量请求中) -->
    <div
      v-if="activeModelStatuses.length > 0"
      :class="[
        activeModelStatuses.length <= 4
          ? 'flex flex-col gap-1'
          : activeModelStatuses.length <= 8
            ? 'columns-2 gap-x-2'
            : 'columns-3 gap-x-2'
      ]"
    >
      <div v-for="item in activeModelStatuses" :key="`${item.kind}-${item.model}`" class="group relative mb-1 break-inside-avoid">
        <!-- 积分已用尽 -->
        <span
          v-if="item.kind === 'credits_exhausted'"
          class="inline-flex items-center gap-1 rounded-compact bg-red-100 px-1.5 py-0.5 text-xs font-medium text-red-700 dark:bg-red-900/30 dark:text-red-400"
        >
          <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
          {{ t('admin.providers.status.creditsExhausted') }}
          <span class="text-xs opacity-70">{{ formatCountdown(item.reset_at) }}</span>
        </span>
        <!-- 正在走积分（模型限流但积分可用）-->
        <span
          v-else-if="item.kind === 'credits_active'"
          class="inline-flex items-center gap-1 rounded-compact bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-400"
        >
          <Icon name="bolt" size="xs" />
          {{ formatScopeName(item.model) }}
          <span class="text-xs opacity-70">{{ formatCountdown(item.reset_at) }}</span>
        </span>
        <!-- 普通模型限流 -->
        <span
          v-else
          class="inline-flex items-center gap-1 rounded-compact bg-purple-100 px-1.5 py-0.5 text-xs font-medium text-purple-700 dark:bg-purple-900/30 dark:text-purple-400"
        >
          <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
          {{ formatScopeName(item.model) }}
          <span class="text-xs opacity-70">{{ formatCountdown(item.reset_at) }}</span>
        </span>
        <!-- Tooltip -->
        <div
          class="pointer-events-none absolute bottom-full left-1/2 z-50 mb-2 w-max max-w-[320px] -translate-x-1/2 whitespace-nowrap tooltip-panel rounded-compact px-3 py-2 text-center text-xs leading-relaxed opacity-0 transition-opacity group-hover:opacity-100"
        >
          {{
            item.kind === 'credits_exhausted'
              ? t('admin.providers.status.creditsExhaustedUntil', { time: formatDateTimeToMinute(item.reset_at) })
              : item.kind === 'credits_active'
                ? t('admin.providers.status.modelCreditOveragesUntil', { model: formatScopeName(item.model), time: formatDateTimeToMinute(item.reset_at) })
                : t('admin.providers.status.modelRateLimitedUntil', { model: formatScopeName(item.model), time: formatDateTimeToMinute(item.reset_at) })
          }}
          <div
            class="tooltip-caret -bottom-1 left-1/2 -translate-x-1/2 border-b border-r"
          ></div>
        </div>
      </div>
    </div>

    <!-- Overload Indicator (529) -->
    <div v-if="isOverloaded" class="group relative">
      <span
        class="inline-flex items-center gap-1 rounded-compact bg-red-100 px-1.5 py-0.5 text-xs font-medium text-red-700 dark:bg-red-900/30 dark:text-red-400"
      >
        <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
        529
      </span>
      <!-- Tooltip -->
      <div
        class="pointer-events-none absolute bottom-full left-1/2 z-50 mb-2 w-56 -translate-x-1/2 whitespace-normal tooltip-panel rounded-compact px-3 py-2 text-center text-xs leading-relaxed opacity-0 transition-opacity group-hover:opacity-100"
      >
        {{ t('admin.providers.status.overloadedUntil', { time: formatTime(provider.overload_until) }) }}
        <div
          class="tooltip-caret -bottom-1 left-1/2 -translate-x-1/2 border-b border-r"
        ></div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { Provider } from '@/types'
import { formatCountdown, formatDateTime, formatDateTimeToMinute, formatCountdownWithSuffix, formatTime } from '@/utils/format'

// ProviderStatusIndicator.vue displays provider availability and recovery details.
// Stored health, scheduling eligibility, expiration, temporary suspension, and
// rate limits supply distinct reasons for an unavailable provider. Local quota
// exhaustion uses the effective amounts returned by the administration API;
// expired windows therefore contribute zero consumption to the displayed state.
// Each enabled dimension independently participates in that comparison, including
// monthly-only limits and cumulative limits that remain active across rollovers.
//
// The component combines these observations into status text, styling, and
// countdowns. Reading the provider updates the presentation while the owning
// backend services remain responsible for accounting, recovery, and persistence.

const { t } = useI18n()

const props = defineProps<{
  provider: Provider
}>()

const emit = defineEmits<{
  (e: 'show-temp-unsched', provider: Provider): void
}>()

// Computed: is rate limited (429)
const isRateLimited = computed(() => {
  if (!props.provider.rate_limit_reset_at) return false
  return new Date(props.provider.rate_limit_reset_at) > new Date()
})

type ProviderModelStatusItem = {
  kind: 'rate_limit' | 'credits_exhausted' | 'credits_active'
  model: string
  reset_at: string
}

// Computed: active model statuses (普通模型限流 + 积分耗尽 + 走积分中)
const activeModelStatuses = computed<ProviderModelStatusItem[]>(() => {
  const extra = props.provider.extra as Record<string, unknown> | undefined
  const modelLimits = extra?.model_rate_limits as
    | Record<string, { rate_limited_at: string; rate_limit_reset_at: string }>
    | undefined
  const now = new Date()
  const items: ProviderModelStatusItem[] = []

  if (!modelLimits) return items

  // 检查 AICredits key 是否生效（积分是否耗尽）
  const aiCreditsEntry = modelLimits['AICredits']
  const hasActiveAICredits = aiCreditsEntry && new Date(aiCreditsEntry.rate_limit_reset_at) > now
  const allowOverages = !!(extra?.allow_overages)

  for (const [model, info] of Object.entries(modelLimits)) {
    if (new Date(info.rate_limit_reset_at) <= now) continue

    if (model === 'AICredits') {
      // AICredits key → 积分已用尽
      items.push({ kind: 'credits_exhausted', model, reset_at: info.rate_limit_reset_at })
    } else if (allowOverages && !hasActiveAICredits) {
      // 普通模型限流 + overages 启用 + 积分可用 → 正在走积分
      items.push({ kind: 'credits_active', model, reset_at: info.rate_limit_reset_at })
    } else {
      // 普通模型限流
      items.push({ kind: 'rate_limit', model, reset_at: info.rate_limit_reset_at })
    }
  }

  return items
})

const formatScopeName = (scope: string): string => {
  const aliases: Record<string, string> = {
    // Claude 系列
    'claude-fable-5-1': 'CFable51',
    'claude-fable-5': 'CFable5',
    'claude-opus-4-6': 'COpus46',
    'claude-opus-4-6-thinking': 'COpus46T',
    'claude-opus-4-7': 'COpus47',
    'claude-opus-4-8': 'COpus48',
    'claude-opus-5': 'COpus5',
    'claude-sonnet-4-6': 'CSon46',
    'claude-sonnet-4-5': 'CSon45',
    'claude-sonnet-4-5-thinking': 'CSon45T',
    'claude-sonnet-5': 'CSon5',
    // Gemini 2.5 系列
    'gemini-2.5-flash': 'G25F',
    'gemini-2.5-flash-lite': 'G25FL',
    'gemini-2.5-flash-thinking': 'G25FT',
    'gemini-2.5-pro': 'G25P',
    'gemini-2.5-flash-image': 'G25I',
    // Gemini 3.5 系列
    'gemini-3.5-flash': 'G35F',
    // Gemini 3 系列
    'gemini-3-flash': 'G3F',
    'gemini-3.1-pro-high': 'G3PH',
    'gemini-3.1-pro-low': 'G3PL',
    'gemini-3-pro-image': 'G3PI',
    'gemini-3.1-flash-image': 'G31FI',
    // 其他
    'gpt-oss-120b-medium': 'GPT120',
    'tab_flash_lite_preview': 'TabFL',
    // 旧版 scope 别名（兼容）
    claude: 'Claude',
    claude_sonnet: 'CSon',
    claude_opus: 'COpus',
    claude_haiku: 'CHaiku',
    gemini_text: 'Gemini',
    gemini_image: 'GImg',
    gemini_flash: 'GFlash',
    gemini_pro: 'GPro',
  }
  return aliases[scope] || scope
}

// Computed: is overloaded (529)
const isOverloaded = computed(() => {
  if (!props.provider.overload_until) return false
  return new Date(props.provider.overload_until) > new Date()
})

// Computed: is temp unschedulable
const isTempUnschedulable = computed(() => {
  if (!props.provider.temp_unschedulable_until) return false
  return new Date(props.provider.temp_unschedulable_until) > new Date()
})

// Computed: has error status
const hasError = computed(() => {
  return props.provider.status === 'error'
})

// isQuotaExceeded reports exhaustion of any positive local spending limit.
// The API supplies effective usage for expired windows, allowing each active
// daily, weekly, monthly, or total amount to be compared independently.
const isQuotaExceeded = computed(() => {
  const exceeded = (used?: number | null, limit?: number | null) =>
    typeof limit === 'number' && limit > 0 && typeof used === 'number' && used >= limit
  return (
    exceeded(props.provider.quota_used, props.provider.quota_limit) ||
    exceeded(props.provider.quota_daily_used, props.provider.quota_daily_limit) ||
    exceeded(props.provider.quota_weekly_used, props.provider.quota_weekly_limit) ||
    exceeded(props.provider.quota_monthly_used, props.provider.quota_monthly_limit)
  )
})

// Computed: countdown text for rate limit (429)
const rateLimitCountdown = computed(() => {
  return formatCountdown(props.provider.rate_limit_reset_at)
})

const rateLimitResumeText = computed(() => {
  if (!rateLimitCountdown.value) return ''
  return t('admin.providers.status.rateLimitedAutoResume', { time: rateLimitCountdown.value })
})

// Computed: countdown text for overload (529)
const overloadCountdown = computed(() => {
  return formatCountdownWithSuffix(props.provider.overload_until)
})

const tempUnschedRecoveryText = computed(() => {
  if (!isTempUnschedulable.value || !props.provider.temp_unschedulable_until) return ''
  return t('admin.providers.status.tempUnschedulableUntil', {
    time: formatDateTime(props.provider.temp_unschedulable_until)
  })
})

// Computed: status badge class
const statusClass = computed(() => {
  if (hasError.value) {
    return 'badge-danger'
  }
  if (isTempUnschedulable.value) {
    return 'badge-warning'
  }
  if (props.provider.status !== 'active') {
    return props.provider.status === 'error' ? 'badge-danger' : 'badge-gray'
  }
  if (isQuotaExceeded.value) {
    return 'badge-warning'
  }
  if (!props.provider.schedulable) {
    return 'badge-gray'
  }
  return 'badge-success'
})

// Computed: status text
const statusText = computed(() => {
  if (hasError.value) {
    return t('admin.providers.status.error')
  }
  if (isTempUnschedulable.value) {
    return t('admin.providers.status.tempUnschedulable')
  }
  if (props.provider.status !== 'active') {
    return t(`admin.providers.status.${props.provider.status}`)
  }
  if (isQuotaExceeded.value) {
    return t('admin.providers.status.quotaExceeded')
  }
  if (!props.provider.schedulable) {
    return t('admin.providers.status.paused')
  }
  return t(`admin.providers.status.${props.provider.status}`)
})

const handleTempUnschedClick = () => {
  if (!isTempUnschedulable.value) return
  emit('show-temp-unsched', props.provider)
}
</script>
