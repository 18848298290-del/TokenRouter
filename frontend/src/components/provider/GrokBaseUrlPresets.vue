<template>
  <div class="flex flex-wrap gap-2">
    <button
      v-for="preset in GROK_BASE_URL_PRESETS"
      :key="preset.url"
      type="button"
      data-testid="grok-base-url-preset"
      class="rounded-control bg-gray-100 px-3 py-1 text-xs text-gray-700 transition-colors hover:bg-primary-50 hover:text-primary-700 dark:bg-dark-600 dark:text-gray-300 dark:hover:bg-primary-900/30 dark:hover:text-primary-400"
      @click="emit('select', preset.url)"
    >
      {{ presetLabel(preset) }} ({{ displayUrl(preset.url) }})
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import {
  GROK_BASE_URL_PRESETS,
  type GrokBaseUrlPreset,
} from '@/components/provider/form/providerEndpoints'

// GrokBaseUrlPresets.vue offers Grok gateway, official, and regional endpoint shortcuts.
// Selecting a shortcut emits its URL to the owning form, whose address input remains
// available for manually supplied relay addresses and other destinations.

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  // select supplies the chosen Grok endpoint URL to the parent address field. It carries
  // the full URL even though the button label removes its scheme for compact display.
  (e: 'select', url: string): void
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()

// presetLabel returns a literal regional label or translates the preset label key. The
// nullish check preserves an explicitly supplied empty label, so callers should supply
// displayable literal values.
const presetLabel = (preset: GrokBaseUrlPreset) =>
  preset.label ?? t(`admin.providers.grokCustomBaseUrl.presets.${preset.labelKey}`)

// displayUrl removes an HTTP or HTTPS scheme from the displayed address.
const displayUrl = (url: string) => url.replace(/^https?:\/\//i, '')
</script>
