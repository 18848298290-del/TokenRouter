<template>
  <div
    v-if="uniqueIps.length > 0"
    class="flex flex-shrink-0 items-center justify-end gap-2"
    :class="inline ? '' : 'border-b border-gray-200 px-4 py-2 dark:border-dark-700'"
  >
    <button
      type="button"
      :class="
        inline
          ? compact
            ? 'btn btn-secondary btn-sm h-[34px] w-[34px] p-0'
            : 'btn btn-secondary h-9 w-9 p-0'
          : 'inline-flex items-center gap-1 rounded-compact px-2 py-1 text-xs font-medium text-primary-600 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-400 dark:hover:bg-primary-900/30'
      "
      :disabled="loading || pendingCount === 0"
      :aria-label="loading ? t('usage.ipGeo.batchFetching') : t('usage.ipGeo.batchFetch')"
      :title="loading ? t('usage.ipGeo.batchFetching') : t('usage.ipGeo.batchFetch')"
      @click="run"
    >
      <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
      <span v-if="!inline">{{
        loading ? t('usage.ipGeo.batchFetching') : t('usage.ipGeo.batchFetch')
      }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { fetchBatch, getEntry } from '@/utils/ipGeoLookup'

/**
 * IpGeoBatchToolbar.vue starts geographic lookups for the distinct nonempty IP
 * addresses supplied by a page. Existing cache states determine whether work remains;
 * a failed batch emits a notification signal for the parent while the toolbar owns its
 * loading indicator.
 */

/**
 * props supplies the current page addresses and selects standalone, embedded, or
 * compact embedded batch lookup presentation.
 */
const props = withDefaults(
  defineProps<{
    ips: Array<string | null | undefined> /* page IP addresses */
    inline?: boolean /* embedded toolbar layout */
    compact?: boolean /* compact embedded button */
  }>(),
  {
    inline: false,
    compact: false,
  },
)

/**
 * emit reports a false batch lookup result so the page can choose its own user facing
 * failure notification.
 */
const emit = defineEmits<{
  /**
   * failed tells the parent that the batch lookup reported failure.
   */
  (e: 'failed'): void
}>()

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()
/**
 * inline exposes the layout flag that switches the toolbar wrapper and button styling
 * for embedded use.
 */
const inline = computed(() => props.inline)
/**
 * compact exposes the embedded button size flag while the standalone toolbar keeps its
 * text action.
 */
const compact = computed(() => props.compact)

/**
 * uniqueIps removes empty entries and duplicate addresses before the batch action or
 * pending count reads the list.
 */
const uniqueIps = computed(() =>
  Array.from(new Set(props.ips.filter((ip): ip is string => Boolean(ip)))),
)

/**
 * pendingCount includes idle and failed cache entries so a completed or loading
 * address does not enable the batch button.
 */
const pendingCount = computed(
  () =>
    uniqueIps.value.filter((ip) => {
      const status = getEntry(ip).status
      return status === 'idle' || status === 'error'
    }).length,
)

/**
 * loading belongs to the batch action and disables repeated button clicks until its
 * awaited request finishes.
 */
const loading = ref(false)

/**
 * run starts a batch lookup and controls the local busy state. A false result emits
 * failed for parent feedback, and finally clears loading whether lookup resolves or
 * throws.
 */
const run = async () => {
  loading.value = true
  try {
    const ok = await fetchBatch(uniqueIps.value)
    if (!ok) emit('failed')
  } finally {
    loading.value = false
  }
}
</script>
