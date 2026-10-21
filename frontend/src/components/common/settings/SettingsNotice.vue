<template>
  <div
    class="flex items-start gap-2 rounded-control border px-3 py-2 text-xs"
    :class="TONE_CLASSES[tone]"
    :role="tone === 'info' ? undefined : 'alert'"
  >
    <Icon :name="ICONS[tone]" size="sm" class="shrink-0" />
    <div class="min-w-0 flex-1 space-y-1">
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'

/**
 * SettingsNotice.vue renders informational, warning, or error text within a settings
 * form. Each tone selects a shared icon and color set, and warning or error notices
 * receive the alert role while informational content remains ordinary form guidance.
 */

/**
 * The tone prop selects the notice palette, glyph, and alert role; omitted tone
 * uses the informational presentation in settings forms.
 */
withDefaults(defineProps<{ tone?: 'info' | 'warning' | 'error' /* surface color tone */ }>(), {
  tone: 'info',
})

/**
 * TONE_CLASSES assigns border, background, and text colors for each supported notice
 * tone across light and dark themes.
 */
const TONE_CLASSES = {
  info: 'border-sky-200 bg-sky-50 text-sky-800 dark:border-sky-800/60 dark:bg-sky-900/20 dark:text-sky-200',
  warning:
    'border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-800/60 dark:bg-amber-900/20 dark:text-amber-200',
  error:
    'border-red-200 bg-red-50 text-red-700 dark:border-red-800/60 dark:bg-red-900/20 dark:text-red-300',
} as const
/**
 * ICONS associates each notice tone with the glyph rendered beside its slotted
 * explanatory text in the form.
 */
const ICONS = {
  info: 'infoCircle',
  warning: 'exclamationTriangle',
  error: 'exclamationCircle',
} as const
</script>
