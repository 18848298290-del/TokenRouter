<template>
  <SettingToggleRow
    :id="id"
    :model-value="modelValue === 'force_on'"
    :label="label"
    :hint="hint"
    :disabled="disabled"
    :testid="testId"
    @update:model-value="emit('update:modelValue', $event ? 'force_on' : 'force_off')"
  />
</template>
<script setup lang="ts">
import { useId } from 'vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import type { OpenAICompactMode } from '@/api/admin/providers.types'

// OpenAICompactionToggle.vue presents an administrator-selected OpenAI compaction switch.
// The parent supplies an already normalized enabled or disabled value, and the row
// converts switch events back to that value for the parent form to persist.
defineProps<{
  modelValue: OpenAICompactMode // Parent-owned selected value.
  label: string // Displayed input label.
  hint?: string // Supplemental field help text.
  disabled?: boolean // Disables the rendered control.
  testId?: string // Stable test selector value.
}>()
// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  'update:modelValue': [value: OpenAICompactMode] // Replacement model value draft value.
}>()
// id connects the compaction label and switch with an identifier unique to this component
// instance.
const id = useId()
</script>
