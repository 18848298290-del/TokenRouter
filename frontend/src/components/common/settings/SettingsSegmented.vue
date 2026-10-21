<template>
  <div
    v-segmented
    class="segmented flex-wrap"
    :class="block ? 'flex w-full' : 'max-w-full'"
    role="radiogroup"
    :aria-label="ariaLabel"
  >
    <button
      v-for="option in options"
      :key="String(option.value)"
      type="button"
      role="radio"
      :aria-checked="modelValue === option.value"
      :disabled="disabled || option.disabled"
      :data-testid="option.testid"
      class="segmented-item inline-flex items-center justify-center gap-1.5 px-3 py-1.5 text-sm disabled:cursor-not-allowed disabled:opacity-50"
      :class="[{ 'segmented-item-active': modelValue === option.value }, block && 'flex-1']"
      @click="select(option.value)"
    >
      <Icon v-if="option.icon" :name="option.icon" size="sm" :animate-on-hover="false" />
      {{ option.label }}
    </button>
  </div>
</template>

<script setup lang="ts" generic="T extends string | number | boolean | null">
import Icon from '@/components/icons/Icon.vue'
import type { IconName } from '@/components/icons/registry'
import { vSegmented } from '@/directives/segmented'

/**
 * SettingsSegmented.vue presents typed mutually exclusive setting options with a
 * shared selected background. The parent owns the selected value, individual options
 * or the whole control can be disabled, and deselectable mode emits null when the
 * active option is clicked again.
 */

/**
 * SettingsSegmentedOption describes one typed choice and its button presentation
 * metadata.
 */
export interface SettingsSegmentedOption<V> {
  value: V /* option selection value */
  label: string /* option display label */
  icon?: IconName /* optional option glyph */
  disabled?: boolean /* interaction disabled flag */
  testid?: string /* button test identifier */
}

/**
 * props pairs typed options with the controlled selection and supplies accessibility,
 * width, disabled, and deselection choices.
 */
const props = defineProps<{
  modelValue: T /* parent controlled value */
  options: SettingsSegmentedOption<T>[] /* ordered selectable choices */
  ariaLabel: string /* accessible group name */
  block?: boolean /* equal width layout */
  disabled?: boolean /* interaction disabled flag */

  /**
   * deselectable clears the model to null when the selected option is clicked again,
   * so callers must accept that empty selection.
   */
  deselectable?: boolean
}>()
/**
 * emit publishes a selected option value or the null clearing value when deselection
 * is enabled for an already selected option.
 */
const emit = defineEmits<{
  'update:modelValue': [value: T | null] /* replacement parent controlled value */
}>()

/**
 * select emits the chosen typed value to the parent. When deselectable is enabled and
 * the same option is already selected, it emits null to clear the controlled
 * selection.
 */
function select(value: T) {
  if (props.deselectable && props.modelValue === value) {
    emit('update:modelValue', null)
    return
  }
  emit('update:modelValue', value)
}
</script>
