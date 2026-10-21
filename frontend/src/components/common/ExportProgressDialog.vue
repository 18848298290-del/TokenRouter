<template>
  <BaseDialog :show="show" :title="t('usage.exporting')" width="narrow" @close="handleCancel">
    <div class="space-y-4">
      <div class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('usage.exportingProgress') }}
      </div>
      <div class="flex items-center justify-between text-sm text-gray-700 dark:text-gray-300">
        <span>{{ t('usage.exportedCount', { current, total }) }}</span>
        <span class="font-medium text-gray-900 dark:text-white">{{ normalizedProgress }}%</span>
      </div>
      <div class="h-2 w-full rounded-full bg-gray-200 dark:bg-dark-700">
        <div
          role="progressbar"
          :aria-valuenow="normalizedProgress"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-label="`${t('usage.exportingProgress')}: ${normalizedProgress}%`"
          class="h-2 rounded-full bg-primary-600 transition-[width,background-color]"
          :style="{ width: `${normalizedProgress}%` }"
        ></div>
      </div>
      <div
        v-if="estimatedTime"
        class="text-xs text-gray-500 dark:text-gray-400"
        aria-live="polite"
        aria-atomic="true"
      >
        {{ t('usage.estimatedTime', { time: estimatedTime }) }}
      </div>
    </div>

    <template #footer>
      <button @click="handleCancel" type="button" class="btn btn-secondary">
        {{ t('usage.cancelExport') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from './BaseDialog.vue'

// ExportProgressDialog.vue presents export progress and cancellation through
// BaseDialog. The parent supplies counts and estimated time, while the displayed
// percentage is rounded and bounded for both progress-bar width and accessible
// progress values.

// Props describes the values and options supplied by the parent component.
interface Props {
  show: boolean // Parent-controlled visible state.
  progress: number // Reported export percentage value.
  current: number // Exported record count.
  total: number // Total matching record count.
  estimatedTime: string // Formatted remaining time text.
}

// Emits defines the component notifications delivered to its parent listeners.
interface Emits {
  // Requests parent cancellation.
  (e: 'cancel'): void
}

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = defineProps<Props>()
// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<Emits>()
// t resolves the component action and status labels using the active application
// locale.
const { t } = useI18n()

// normalizedProgress rounds a finite percentage into the zero-to-one-hundred range
// shared by bar width and accessible progress values.
const normalizedProgress = computed(() => {
  // Non-finite progress would produce unusable width and ARIA values; render zero so
  // both visual and accessible progress remain numeric.
  const value = Number.isFinite(props.progress) ? props.progress : 0
  // Round and clamp the displayed percentage to valid progress-bar bounds.
  return Math.min(100, Math.max(0, Math.round(value)))
})

// handleCancel publishes a cancellation request for the parent export controller.
const handleCancel = () => {
  emit('cancel')
}
</script>
