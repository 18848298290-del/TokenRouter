<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Skeleton from '@/components/common/Skeleton.vue'

/**
 * SettingsSkeleton.vue reserves the navigation and settings-card space while the
 * initial bulk request loads. Its status label announces loading, while decorative
 * rows stay hidden from assistive technology and use the same responsive field
 * layout as the settings page.
 */
/**
 * The translation service supplies the loading status label announced while editable settings panels have not mounted.
 */
const { t } = useI18n()
</script>

<template>
  <div
    class="space-y-4"
    role="status"
    :aria-label="t('common.loading')"
    aria-busy="true"
    data-testid="settings-skeleton"
  >
    <!-- Navigation and field placeholders occupy separate regions, with fields stacked at narrow viewport widths. -->
    <div class="card flex gap-3 overflow-hidden p-3" aria-hidden="true">
      <Skeleton v-for="tab in 7" :key="tab" :width="96" :height="36" class="shrink-0" />
    </div>
    <div v-for="section in 2" :key="section" class="card" aria-hidden="true">
      <div class="card-header space-y-3">
        <Skeleton :width="128" :height="20" />
        <Skeleton width="65%" :height="12" />
      </div>
      <div class="space-y-6 p-6">
        <div v-for="field in 3" :key="field" class="grid gap-3 sm:grid-cols-3 sm:items-center">
          <Skeleton :width="96" :height="12" />
          <Skeleton :height="36" class="sm:col-span-2" />
        </div>
        <div class="flex justify-end">
          <Skeleton :width="112" :height="36" />
        </div>
      </div>
    </div>
  </div>
</template>
