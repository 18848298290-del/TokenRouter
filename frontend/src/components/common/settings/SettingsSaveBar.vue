<template>
  <MotionTransition name="pop-float">
    <div
      v-if="visible"
      class="pointer-events-none sticky bottom-6 flex justify-center"
      data-testid="settings-save-bar"
    >
      <!-- The dialog radius and two unit padding surround control radius buttons, keeping the inner action corners concentric with the outer bar. -->
      <div
        class="pointer-events-auto flex max-w-full flex-wrap items-center gap-x-6 gap-y-2 rounded-dialog bg-gray-900 p-2 pl-5 text-white shadow-2xl ring-1 ring-black/10 dark:bg-dark-900 dark:text-dark-50 dark:ring-dark-500"
        role="status"
      >
        <p class="text-sm font-medium">{{ message }}</p>
        <div class="flex items-center gap-2">
          <slot />
        </div>
      </div>
    </div>
  </MotionTransition>
</template>

<script setup lang="ts">
import MotionTransition from '@/components/common/MotionTransition.vue'

/**
 * SettingsSaveBar.vue presents pending settings changes in a sticky action bar near
 * the viewport bottom. The parent controls visibility and message and supplies discard
 * or save buttons; the outer centering container lets pointer events pass through
 * surrounding space.
 */

/**
 * The save bar props control whether pending changes are announced and provide
 * the message shown beside the caller supplied actions.
 */
defineProps<{
  visible: boolean /* bar visibility flag */
  message: string /* caller supplied message */
}>()
</script>
