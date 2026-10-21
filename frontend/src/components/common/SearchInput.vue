<template>
  <div class="input-icon-wrap w-full">
    <div class="input-icon">
      <Icon name="search" size="md" class="text-gray-400" />
    </div>
    <input
      :value="modelValue"
      type="text"
      class="input input-has-icon"
      :placeholder="placeholder"
      @input="handleInput"
    />
  </div>
</template>

<script setup lang="ts">
import { useDebounceFn } from '@vueuse/core'
import { SEARCH_DEBOUNCE_MS } from '@/constants/ui'
import Icon from '@/components/icons/Icon.vue'

// SearchInput.vue publishes text edits immediately and schedules a separate search
// event through the debounce helper. The parent owns the visible value, while
// debounceMs is read during setup to configure delayed search delivery.

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = withDefaults(
  defineProps<{
    modelValue: string // Parent-owned current field value.
    placeholder?: string // Empty field placeholder text.
    debounceMs?: number // Search notification delay milliseconds.
  }>(),
  {
    placeholder: 'Search...',
    debounceMs: SEARCH_DEBOUNCE_MS,
  },
)

// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<{
  // Publishes replacement model value.
  (e: 'update:modelValue', value: string): void
  // Publishes debounced search text.
  (e: 'search', value: string): void
}>()

// debouncedEmitSearch schedules the separate search notification using debounceMs
// captured during setup while model updates remain immediate.
const debouncedEmitSearch = useDebounceFn((value: string) => {
  emit('search', value)
}, props.debounceMs)

// handleInput publishes the typed string before scheduling the debounced search. Model
// listeners receive every edit, while the separate search event follows the delay
// configured during component setup.
const handleInput = (event: Event) => {
  const value = (event.target as HTMLInputElement).value
  emit('update:modelValue', value)
  debouncedEmitSearch(value)
}
</script>
