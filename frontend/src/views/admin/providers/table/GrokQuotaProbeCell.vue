<template>
  <div v-if="visible" class="space-y-1">
    <div class="flex flex-wrap items-center gap-1.5">
      <button
        type="button"
        class="inline-flex items-center gap-0.5 rounded-compact px-1.5 py-0.5 text-xs font-medium text-primary-600 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-500 dark:hover:bg-primary-500/8"
        :disabled="loading"
        :title="t('admin.providers.usageWindow.grokProbeTooltip')"
        @click="handleProbe"
      >
        <Icon
          name="refresh"
          size="md"
          :animate-on-hover="false"
          class="h-2.5 w-2.5"
          :class="{ 'animate-spin': loading }"
        />
        {{ t('admin.providers.usageWindow.grokProbe') }}
      </button>
    </div>

    <!-- Compact mode leaves usage and balance summaries to the parent while retaining probe failures beside the refresh action. -->
    <div v-if="!compact && summary" class="text-xs text-gray-600 dark:text-gray-300">
      {{ summary }}
    </div>
    <div v-if="error" class="truncate text-xs text-red-600 dark:text-red-400" :title="error">
      {{ truncatedError }}
    </div>
  </div>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { GrokQuotaProbeResult } from '@/api/admin/grok'
import type { Provider } from '@/api/admin/providers.types'

/**
 * GrokQuotaProbeCell.vue lets administrators probe quota for Grok OAuth providers and reports
 * the returned observation to its parent. Compact mode leaves the weekly summary to the
 * surrounding usage view. Probe failures remain local display errors, and changing provider
 * identity clears the displayed probe state.
 */

/**
 * props identifies the Grok row and compact presentation mode used when the surrounding usage
 * view owns summary rendering.
 */
const props = withDefaults(
  defineProps<{
    provider: Provider // current provider row record
    /**
     * When true, the parent owns weekly summary rendering while this component retains the
     * probe button and error feedback.
     */
    compact?: boolean
  }>(),
  { compact: false },
)

/**
 * emit returns successful probe response objects to the parent even when the response includes
 * a probe_error observation.
 */
const emit = defineEmits<{
  /**
   * Emits the returned probe result, including its optional probe_error, so the parent can
   * refresh its usage view.
   */
  probed: [result: GrokQuotaProbeResult]
}>()

/**
 * Translation and request state drive probe feedback; visibility restricts the control to Grok
 * providers authenticated through OAuth.
 */
const { t } = useI18n()

const visible = computed(
  () => props.provider.platform === 'grok' && props.provider.type === 'oauth',
)
const loading = ref(false)
const error = ref<string | null>(null)
const data = ref<GrokQuotaProbeResult | null>(null)

/**
 * extractErrorMessage selects the first available message from common API error shapes. It
 * checks direct message and reason before nested response details, then uses the localized
 * generic error label.
 */
const extractErrorMessage = (e: unknown): string => {
  const err = e as {
    message?: string // direct error message text
    reason?: string // direct error reason text
    response?: {
      data?: {
        message?: string // direct error message text
        error?: string // nested API error text
      } // nested HTTP error payload
    } // optional HTTP failure response
  }
  return (
    err?.message ||
    err?.reason ||
    err?.response?.data?.message ||
    err?.response?.data?.error ||
    t('common.error')
  )
}

/**
 * summary renders weekly utilization when a result exists and compact mode is disabled,
 * clamping the displayed percentage to its bar range.
 */
const summary = computed(() => {
  if (props.compact || !data.value) return ''
  // The standalone probe shows a weekly summary only when billing includes that period and a
  // measured utilization percentage.
  const billing = data.value.billing
  if (billing?.period_type?.toLowerCase() === 'weekly' && billing.usage_percent != null) {
    return t('admin.providers.usageWindow.grokWeeklyUsage', {
      // Clamp the standalone summary to the percentage bar range.
      percent: Math.round(Math.min(100, Math.max(0, billing.usage_percent))),
    })
  }
  return ''
})

/**
 * truncatedError limits inline failure text to eighty characters while the full error remains
 * available to the template tooltip.
 */
const truncatedError = computed(() => {
  if (!error.value) return ''
  // Keep inline errors compact; the tooltip retains the full message.
  return error.value.length > 80 ? `${error.value.slice(0, 80)}...` : error.value
})

/**
 * handleProbe queries Grok quota when no probe is active and emits the returned result. A
 * returned probe_error is displayed alongside the result; thrown failures produce a local
 * message.
 */
const handleProbe = async () => {
  if (loading.value) return
  loading.value = true
  error.value = null
  try {
    data.value = await adminAPI.grok.queryQuota(props.provider.id)
    error.value = data.value.probe_error || null
    emit('probed', data.value)
  } catch (e) {
    error.value = extractErrorMessage(e)
  } finally {
    loading.value = false
  }
}

watch(
  () => props.provider.id,
  () => {
    data.value = null
    error.value = null
    loading.value = false
  },
)
</script>
