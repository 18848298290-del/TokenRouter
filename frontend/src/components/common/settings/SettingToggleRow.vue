<template>
  <SettingRow :id="id" :label="label" :hint="hint" :help="help" :setting="setting">
    <template v-if="$slots.hint" #hint>
      <slot name="hint" />
    </template>
    <Toggle
      :id="id"
      :model-value="modelValue"
      :disabled="disabled"
      :aria-label="label"
      :aria-describedby="hint ? `${id}-hint` : undefined"
      :data-setting="setting"
      :data-testid="testid"
      size="md"
      @update:model-value="emit('update:modelValue', $event)"
    />
  </SettingRow>
</template>

<script setup lang="ts">
import Toggle from '@/components/common/Toggle.vue'
import SettingRow from './SettingRow.vue'

/**
 * SettingToggleRow.vue combines SettingRow and Toggle for a controlled boolean
 * setting. Labels and descriptions are forwarded to the shared controls, the parent
 * receives model updates, and dependent inputs can follow this row in the containing
 * settings panel.
 */

/**
 * The toggle row props supply the controlled boolean, label, explanations, and
 * input identifiers forwarded to SettingRow and Toggle.
 */
defineProps<{
  id: string /* input association identifier */
  label: string /* visible field label */
  modelValue: boolean /* controlled toggle state */
  hint?: string /* inline explanation text */
  help?: string /* tooltip explanation text */
  setting?: string /* setting data identifier */
  disabled?: boolean /* interaction disabled flag */
  testid?: string /* button test identifier */
}>()
/**
 * emit forwards the Toggle boolean update to the page that owns the setting draft and
 * its eventual save operation.
 */
const emit = defineEmits<{
  'update:modelValue': [value: boolean] /* replacement parent controlled value */
}>()
</script>
