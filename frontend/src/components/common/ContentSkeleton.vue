<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Skeleton from './Skeleton.vue'

/**
 * ContentSkeleton.vue renders loading placeholders for lists, forms, details, and
 * articles. Callers select the content shape and row count and own the surrounding
 * card and request state; decorative skeleton elements sit under one accessible
 * loading status.
 */

// @project-doc docs/architecture/frontend_ui_conventions.md#loading_feedback

/**
 * The content props choose the placeholder structure and repetition count; the
 * parent mounts this region while its content is loading.
 */
withDefaults(
  defineProps<{
    variant?: 'list' | 'form' | 'detail' | 'article' /* visual presentation variant */
    rows?: number /* placeholder row count */
  }>(),
  {
    variant: 'list',
    rows: 3,
  },
)

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()
</script>

<template>
  <div role="status" :aria-label="t('common.loading')" aria-busy="true" data-loading-skeleton>
    <div v-if="variant === 'form'" class="space-y-6" aria-hidden="true">
      <div v-for="row in rows" :key="row" class="grid gap-3 sm:grid-cols-3 sm:items-center">
        <Skeleton width="60%" :height="16" />
        <Skeleton :height="36" class="sm:col-span-2" />
      </div>
    </div>
    <div
      v-else-if="variant === 'detail'"
      class="grid gap-x-6 gap-y-5 sm:grid-cols-2"
      aria-hidden="true"
    >
      <div v-for="row in rows" :key="row" class="min-w-0 space-y-3">
        <Skeleton width="40%" :height="12" />
        <Skeleton width="75%" :height="20" />
      </div>
    </div>
    <div v-else-if="variant === 'article'" class="space-y-6" aria-hidden="true">
      <Skeleton width="45%" :height="28" />
      <div v-for="row in rows" :key="row" class="space-y-3">
        <Skeleton :height="16" />
        <Skeleton :height="16" />
        <Skeleton width="70%" :height="16" />
      </div>
    </div>
    <div v-else class="divide-y divide-gray-100 dark:divide-dark-700" aria-hidden="true">
      <div v-for="row in rows" :key="row" class="flex items-center gap-4 py-4 first:pt-0 last:pb-0">
        <Skeleton :width="36" :height="36" class="shrink-0" />
        <div class="min-w-0 flex-1 space-y-3">
          <Skeleton width="60%" :height="16" />
          <Skeleton width="85%" :height="12" />
        </div>
        <Skeleton width="15%" :height="16" class="shrink-0" />
      </div>
    </div>
  </div>
</template>
