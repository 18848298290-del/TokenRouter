<template>
  <div
    v-if="state?.eligible"
    class="min-w-0 max-w-full space-y-1"
    data-testid="ollama-cloud-usage-cell"
  >
    <UsageProgressBar
      v-if="snapshot?.data?.five_hour"
      label="5h"
      :utilization="snapshot.data.five_hour.used_percent"
      :resets-at="snapshot.data.five_hour.reset_at"
      color="indigo"
      data-testid="ollama-cloud-five-hour"
    />
    <UsageProgressBar
      v-if="snapshot?.data?.seven_day"
      label="7d"
      :utilization="snapshot.data.seven_day.used_percent"
      :resets-at="snapshot.data.seven_day.reset_at"
      color="emerald"
      data-testid="ollama-cloud-seven-day"
    />
    <div v-if="state.configured" class="flex items-center pt-0.5">
      <button
        type="button"
        class="inline-flex items-center gap-0.5 rounded-compact px-1.5 py-0.5 text-xs font-medium text-primary-600 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-500 dark:hover:bg-primary-500/8"
        :disabled="refreshing"
        :title="t('admin.providers.usageWindow.activeQuery')"
        :aria-label="t('admin.providers.usageWindow.activeQuery')"
        data-testid="ollama-cloud-usage-query"
        @click="refreshUsage"
      >
        <Icon name="refresh" size="xs" :class="{ 'animate-spin': refreshing }" :stroke-width="2" />
        {{ t('admin.providers.usageWindow.activeQuery') }}
      </button>
    </div>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { Provider, OllamaCloudUsageState } from '@/api/admin/providers.types'
import Icon from '@/components/icons/Icon.vue'
import UsageProgressBar from './UsageProgressBar.vue'

/**
 * OllamaCloudUsageCell.vue displays the saved Ollama Cloud usage snapshot and refreshes it on
 * administrator request. Incoming provider snapshots replace local display state, while a
 * successful manual refresh emits its returned state so the parent can update the provider row.
 */

/**
 * props supplies the saved cloud snapshot, and successful manual refreshes are returned to the
 * parent through the updated event.
 */
const props = defineProps<{
  provider: Provider // current provider row record
}>()
/**
 * emit carries refreshed cloud state after local display state has accepted the same response
 * from the administrative API.
 */
const emit = defineEmits<{
  /**
   * Emits the successful refresh state so the parent can replace its provider usage snapshot.
   */
  updated: [state: OllamaCloudUsageState]
}>()
/**
 * Translation, local snapshot state, and the refresh flag drive the two usage bars and manual
 * refresh feedback.
 */
const { t } = useI18n()
const state = ref(props.provider.ollama_cloud_usage)
const refreshing = ref(false)
const snapshot = computed(() => state.value?.snapshot)

watch(
  () => props.provider.ollama_cloud_usage,
  (next) => {
    state.value = next
  },
)

/**
 * refreshUsage fetches a fresh Ollama Cloud snapshot unless a refresh is already active.
 * Success updates local state before emitting updated; failure logs the error and keeps the
 * previous snapshot visible.
 */
const refreshUsage = async () => {
  if (refreshing.value) return
  refreshing.value = true
  try {
    const next = await adminAPI.providers.refreshOllamaCloudUsage(props.provider.id)
    state.value = next
    emit('updated', next)
  } catch (error) {
    // Retain the last snapshot when refresh fails so the row continues showing its previous
    // observed cloud usage.
    console.error('Failed to refresh Ollama Cloud usage:', error)
  } finally {
    refreshing.value = false
  }
}
</script>
