<template>
  <div ref="rootRef" class="min-w-0" :class="full ? 'col-span-full' : ''">
    <label v-if="label" class="filter-field-label">{{ label }}</label>
    <slot />
  </div>
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, provide, ref, shallowRef, type ComputedRef } from 'vue'
import {
  FILTER_FIELD_KEY,
  FILTER_PANEL_KEY,
  type FilterChip,
  type FilterControlState,
} from './filterPanel'

// FilterField.vue connects a labeled filter control to its parent panel chip list.
// Explicit valueText takes precedence over an injected Select state, including an
// empty string that hides the chip. Registration follows the field component lifetime
// and is removed on unmount.

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = withDefaults(
  defineProps<{
    label?: string // Visible field label text.
    full?: boolean // Full grid row flag.

    // Overrides nested control state when supplied; an empty string hides the chip
    // even if that control is active.
    valueText?: string

    // Supplies the nested control reset value; undefined lets Select infer it from the
    // first option.
    emptyValue?: string | number | boolean | null
  }>(),
  {
    label: '',
    full: false,
    valueText: undefined,
    emptyValue: undefined,
  },
)
// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<{
  // Requests field selection clearing.
  (event: 'clear'): void
}>()

// rootRef supplies the chip sorting element, while controlState retains the computed
// state most recently registered by a nested Select control.
const rootRef = ref<HTMLElement | null>(null)
const controlState = shallowRef<ComputedRef<FilterControlState> | null>(null)

provide(FILTER_FIELD_KEY, {
  emptyValue: () => props.emptyValue,
  bindControl: (state) => {
    controlState.value = state
  },
})

// chip gives supplied valueText precedence over nested control state, including an
// empty override that hides an otherwise active control chip.
const chip = computed<FilterChip | null>(() => {
  if (props.valueText !== undefined) {
    if (!props.valueText) return null
    return {
      label: props.label,
      text: props.valueText,
      clear: () => emit('clear'),
      el: rootRef.value,
    }
  }
  const state = controlState.value?.value
  if (!state?.active) return null
  return { label: props.label, text: state.text, clear: state.clear, el: rootRef.value }
})

// unregister is the optional panel cleanup returned during setup and invoked before
// this field unmounts.
const unregister = inject(FILTER_PANEL_KEY, null)?.register(chip)
// Remove this field registration before unmount so its retained computed chip stops
// contributing to the parent panel summary.
onBeforeUnmount(() => unregister?.())
</script>

<style scoped>
/*
  Filter field labels use small neutral text so controls remain the main visual
  elements of the panel.
*/
.filter-field-label {
  @apply mb-1.5 block text-xs font-medium text-gray-500 dark:text-dark-300;
}
</style>
