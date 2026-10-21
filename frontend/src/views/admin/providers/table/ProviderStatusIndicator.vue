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
      <!-- Restriction detail tooltip below the badge. -->
      <div
        class="invisible absolute left-0 top-full z-tooltip mt-1.5 min-w-[200px] max-w-[300px] tooltip-panel rounded-control px-3 py-2 text-xs opacity-0 shadow-xl transition duration-normal group-hover/error:visible group-hover/error:opacity-100"
      >
        <div class="whitespace-pre-wrap break-words leading-relaxed text-gray-300">
          {{ provider.error_message }}
        </div>
        <!-- Tooltip anchor arrow. -->
        <div class="tooltip-caret -top-1 left-3 border-l border-t"></div>
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
        {{
          t('admin.providers.status.rateLimitedUntil', {
            time: formatDateTime(provider.rate_limit_reset_at),
          })
        }}
        <div class="tooltip-caret -bottom-1 left-1/2 -translate-x-1/2 border-b border-r"></div>
      </div>
    </div>

    <!-- Model cooldown and credit-backed overage indicators. -->
    <div
      v-if="activeModelStatuses.length > 0"
      :class="[
        activeModelStatuses.length <= 4
          ? 'flex flex-col gap-1'
          : activeModelStatuses.length <= 8
            ? 'columns-2 gap-x-2'
            : 'columns-3 gap-x-2',
      ]"
    >
      <div
        v-for="item in activeModelStatuses"
        :key="`${item.kind}-${item.model}`"
        class="group relative mb-1 break-inside-avoid"
      >
        <!-- AI credits are exhausted. -->
        <span
          v-if="item.kind === 'credits_exhausted'"
          class="inline-flex items-center gap-1 rounded-compact bg-red-100 px-1.5 py-0.5 text-xs font-medium text-red-700 dark:bg-red-900/30 dark:text-red-400"
        >
          <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
          {{ t('admin.providers.status.creditsExhausted') }}
          <span class="text-xs opacity-70">{{ formatCountdown(item.reset_at) }}</span>
        </span>
        <!-- The model cooldown is shown as credit-backed overage use.-->
        <span
          v-else-if="item.kind === 'credits_active'"
          class="inline-flex items-center gap-1 rounded-compact bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-400"
        >
          <Icon name="bolt" size="xs" />
          {{ formatScopeName(item.model) }}
          <span class="text-xs opacity-70">{{ formatCountdown(item.reset_at) }}</span>
        </span>
        <!-- Ordinary model cooldown. -->
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
              ? t('admin.providers.status.creditsExhaustedUntil', {
                  time: formatDateTimeToMinute(item.reset_at),
                })
              : item.kind === 'credits_active'
                ? t('admin.providers.status.modelCreditOveragesUntil', {
                    model: formatScopeName(item.model),
                    time: formatDateTimeToMinute(item.reset_at),
                  })
                : t('admin.providers.status.modelRateLimitedUntil', {
                    model: formatScopeName(item.model),
                    time: formatDateTimeToMinute(item.reset_at),
                  })
          }}
          <div class="tooltip-caret -bottom-1 left-1/2 -translate-x-1/2 border-b border-r"></div>
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
        {{
          t('admin.providers.status.overloadedUntil', { time: formatTime(provider.overload_until) })
        }}
        <div class="tooltip-caret -bottom-1 left-1/2 -translate-x-1/2 border-b border-r"></div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { Provider } from '@/api/admin/providers.types'
import {
  formatCountdown,
  formatDateTime,
  formatDateTimeToMinute,
  formatCountdownWithSuffix,
  formatTime,
} from '@/utils/format'

/**
 * ProviderStatusIndicator.vue combines provider health, scheduling flags, temporary
 * restrictions, and local quota observations into availability badges and recovery details.
 * Each enabled spending dimension compares the effective usage supplied by the API against its
 * positive limit. Model-specific rate limits also distinguish exhausted AI credits from
 * requests that can continue through overages.
 *
 * The main status calculation checks stored errors, temporary suspension, then inactive status,
 * quota exhaustion, and a paused scheduling flag. The template gives provider-wide rate limits
 * and overload precedence over that main badge. All time comparisons occur when reactive
 * computations run;
 * this component owns no ticking clock. Temporary-suspension clicks delegate to the parent only
 * while that restriction is considered active.
 */

/**
 * The translator labels provider status and recovery details derived from the current provider
 * record and its scheduling restrictions.
 */
const { t } = useI18n()

/**
 * props supplies availability observations while the parent handles the temporary-suspension
 * detail event emitted for an active restriction.
 */
const props = defineProps<{
  provider: Provider // current provider availability record
}>()

/**
 * emit asks the parent to show temporary-suspension details for the selected provider after the
 * local active-restriction guard passes.
 */
const emit = defineEmits<{
  /**
   * show-temp-unsched asks the parent to inspect temporary restriction details for this
   * provider.
   */
  (e: 'show-temp-unsched', provider: Provider): void
}>()

/**
 * isRateLimited compares the provider-level reset timestamp with the current clock when the
 * reactive computation runs; missing timestamps are inactive.
 */
const isRateLimited = computed(() => {
  if (!props.provider.rate_limit_reset_at) return false
  return new Date(props.provider.rate_limit_reset_at) > new Date()
})

/**
 * ProviderModelStatusItem describes one active model restriction or AI-credit status badge.
 */
type ProviderModelStatusItem = {
  kind: 'rate_limit' | 'credits_exhausted' | 'credits_active' // displayed model restriction category
  model: string // model or credit scope identifier
  reset_at: string // restriction expiry timestamp
}

/**
 * activeModelStatuses interprets active model cooldowns together with the AICredits marker and
 * allow_overages, distinguishing blocked quota from credit-backed traffic.
 */
const activeModelStatuses = computed<ProviderModelStatusItem[]>(() => {
  const extra = props.provider.extra as Record<string, unknown> | undefined
  const modelLimits = extra?.model_rate_limits as
    | Record<
        string,
        {
          rate_limited_at: string // model limit observation timestamp
          rate_limit_reset_at: string // model limit expiry timestamp
        }
      >
    | undefined
  const now = new Date()
  const items: ProviderModelStatusItem[] = []

  if (!modelLimits) return items

  // An active AICredits marker overrides the overage interpretation so ordinary model cooldowns
  // cannot imply available credits after credit exhaustion.
  const aiCreditsEntry = modelLimits['AICredits']
  const hasActiveAICredits = aiCreditsEntry && new Date(aiCreditsEntry.rate_limit_reset_at) > now
  const allowOverages = !!extra?.allow_overages

  for (const [model, info] of Object.entries(modelLimits)) {
    if (new Date(info.rate_limit_reset_at) <= now) continue

    if (model === 'AICredits') {
      // The dedicated credit marker represents exhausted credits, even when the provider
      // otherwise permits overage use for model cooldowns.
      items.push({ kind: 'credits_exhausted', model, reset_at: info.rate_limit_reset_at })
    } else if (allowOverages && !hasActiveAICredits) {
      // With overages enabled and no active exhausted-credit marker, show the model cooldown as
      // credit-backed use in its status badge.
      items.push({ kind: 'credits_active', model, reset_at: info.rate_limit_reset_at })
    } else {
      // Keep ordinary cooldowns visibly restricted when overages are unavailable or the
      // separate credit marker reports exhaustion for the provider.
      items.push({ kind: 'rate_limit', model, reset_at: info.rate_limit_reset_at })
    }
  }

  return items
})

/**
 * formatScopeName returns a compact alias for recognized model and legacy scope identifiers.
 * Unknown identifiers remain unchanged so newly reported model scopes still have a readable
 * label in status details.
 */
const formatScopeName = (scope: string): string => {
  const aliases: Record<string, string> = {
    // Claude model aliases.
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
    // Gemini 2.5 model aliases.
    'gemini-2.5-flash': 'G25F',
    'gemini-2.5-flash-lite': 'G25FL',
    'gemini-2.5-flash-thinking': 'G25FT',
    'gemini-2.5-pro': 'G25P',
    'gemini-2.5-flash-image': 'G25I',
    // Gemini 3.5 model aliases.
    'gemini-3.5-flash': 'G35F',
    // Gemini 3 model aliases.
    'gemini-3-flash': 'G3F',
    'gemini-3.1-pro-high': 'G3PH',
    'gemini-3.1-pro-low': 'G3PL',
    'gemini-3-pro-image': 'G3PI',
    'gemini-3.1-flash-image': 'G31FI',
    // Other model aliases.
    'gpt-oss-120b-medium': 'GPT120',
    tab_flash_lite_preview: 'TabFL',
    // Retain legacy scope identifiers so saved cooldown records remain recognizable beside the
    // model-specific names returned by current responses.
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

/**
 * Overload, temporary suspension, and stored error predicates remain distinct inputs to
 * recovery details and the primary status precedence.
 */
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

/**
 * isQuotaExceeded compares each effective total, daily, weekly, and monthly usage amount
 * independently against its positive limit supplied by the API.
 */
const isQuotaExceeded = computed(() => {
  /**
   * exceeded reports quota exhaustion only for numeric usage and a positive numeric limit.
   * Missing usage or disabled limits produce false, keeping unknown observations separate from
   * measured exhaustion.
   */
  const exceeded = (used?: number | null, limit?: number | null) =>
    typeof limit === 'number' && limit > 0 && typeof used === 'number' && used >= limit
  return (
    exceeded(props.provider.quota_used, props.provider.quota_limit) ||
    exceeded(props.provider.quota_daily_used, props.provider.quota_daily_limit) ||
    exceeded(props.provider.quota_weekly_used, props.provider.quota_weekly_limit) ||
    exceeded(props.provider.quota_monthly_used, props.provider.quota_monthly_limit)
  )
})

/**
 * Recovery text formats rate-limit, overload, and temporary-suspension timestamps for their
 * separate status details using the shared date helpers.
 */
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
    time: formatDateTime(props.provider.temp_unschedulable_until),
  })
})

/**
 * Status styling and text share precedence: stored error, temporary restriction, inactive
 * status, exhausted quota, paused scheduling, then active availability.
 */
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

/**
 * handleTempUnschedClick emits the current provider for temporary-suspension inspection when
 * that restriction is active. Expired or absent restrictions produce no event, leaving the
 * parent dialog selection unchanged.
 */
const handleTempUnschedClick = () => {
  if (!isTempUnschedulable.value) return
  emit('show-temp-unsched', props.provider)
}
</script>
