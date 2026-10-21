<template>
  <p
    v-if="version || formattedUpdatedAt"
    class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-dark-400"
  >
    <span v-if="version" :title="version">models.dev · {{ version.slice(0, 12) }}</span>
    <span v-if="formattedUpdatedAt"
      >{{ t('admin.pricing.defaults.updatedAt') }} {{ formattedUpdatedAt }}</span
    >
  </p>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

// ModelCatalogInfo.vue displays the catalog version and a usable last-update timestamp. Empty,
// zero-date, and invalid timestamps are omitted so an uninitialized catalog does not show a
// misleading update time. Valid dates use the browser's local display format.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  version: string // catalog version identifier
  updatedAt: string // catalog update timestamp
}>()

// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()

// formattedUpdatedAt omits empty, zero-date, and invalid timestamps so a catalog with no usable
// update date displays no time.
const formattedUpdatedAt = computed(() => {
  // Empty and zero-date timestamps represent an uninitialized catalog update, so omit them
  // before date formatting could display a misleading historical time.
  if (!props.updatedAt || props.updatedAt.startsWith('0001')) return ''
  const date = new Date(props.updatedAt)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString()
})
</script>
