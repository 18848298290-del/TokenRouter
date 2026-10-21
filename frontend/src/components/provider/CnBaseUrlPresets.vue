<template>
  <div class="flex flex-wrap gap-2">
    <button
      v-for="preset in presets"
      :key="preset.mode + ':' + preset.protocol + ':' + preset.url"
      type="button"
      data-testid="cn-base-url-preset"
      :class="[
        'rounded-control px-3 py-1 text-xs transition-colors',
        isActive(preset)
          ? 'bg-primary-100 text-primary-700 dark:bg-primary-500/8 dark:text-primary-500'
          : 'bg-gray-100 text-gray-700 hover:bg-primary-50 hover:text-primary-700 dark:bg-dark-600 dark:text-gray-300 dark:hover:bg-primary-900/30 dark:hover:text-primary-400',
      ]"
      @click="emit('select', preset)"
    >
      {{ preset.label }} ({{ displayUrl(preset.url) }})
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import {
  CN_BASE_URL_PRESETS,
  type CnBaseUrlPreset,
} from '@/components/provider/form/providerEndpoints'

// CnBaseUrlPresets.vue offers platform endpoint presets for the provider address fields.
// Protocol filtering controls the visible choices, while selecting a preset returns its
// URL and provider mode together for the owning form to apply.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  platform: 'kimi' | 'zhipu' | 'deepseek' // Provider platform identifier.

  // Provider billing mode used for highlighting presets; selecting a preset can change
  // this mode.
  mode?: 'payg' | 'coding'

  // When supplied, restricts visible presets to this protocol while retaining both
  // billing modes.
  protocol?: 'adaptive' | 'chat_completions' | 'anthropic' | 'responses'

  // Current address used for exact URL highlighting alongside the selected provider
  // billing mode.
  currentUrl?: string
}>()

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  // select supplies the chosen preset with its address, provider mode, and protocol. The
  // parent applies those values to its form and remains responsible for validating any
  // later manual address edits.
  (e: 'select', preset: CnBaseUrlPreset): void
}>()

// presets filters the platform address shortcuts by protocol while keeping both
// pay-as-you-go and coding choices visible.
const presets = computed(() => {
  const all = CN_BASE_URL_PRESETS[props.platform] ?? []

  if (props.protocol == null) return all
  return all.filter((p) => p.protocol === props.protocol)
})

// isActive reports whether the preset matches the selected mode or current URL. A mode
// match highlights every matching preset even when its URL differs from the address
// input.
const isActive = (preset: CnBaseUrlPreset) =>
  (props.mode != null && preset.mode === props.mode) || preset.url === props.currentUrl

// displayUrl removes an HTTP or HTTPS scheme for compact preset labels.
const displayUrl = (url: string) => url.replace(/^https?:\/\//i, '')
</script>
