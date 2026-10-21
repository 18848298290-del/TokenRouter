<template>
  <label class="flex flex-col items-center gap-0.5 cursor-pointer">
    <span class="text-xs text-gray-500 dark:text-gray-400 whitespace-nowrap">{{ label }}</span>
    <!-- The payment switch exposes label, checked, and toggle while rendering the shared application Switch control. -->
    <Toggle
      :model-value="checked"
      size="sm"
      :disabled="disabled"
      @update:model-value="emit('toggle')"
      @click.stop
    />
  </label>
</template>

<script setup lang="ts">
import Toggle from '@/components/common/Toggle.vue'

/**
 * ToggleSwitch.vue adapts payment provider controls to the shared application Toggle
 * component. It forwards the controlled checked and disabled values and emits the payment
 * editor's toggle action so the parent retains ownership of provider state.
 */

defineProps<{
  label: string // displayed setting label
  checked: boolean // controlled setting value
  disabled?: boolean // update-in-progress interaction guard
}>()
const emit = defineEmits<{
  toggle: [] // requested checked-value inversion
}>()
</script>
