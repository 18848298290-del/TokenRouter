<script setup lang="ts">
import { computed } from 'vue'
import {
  QUOTA_THRESHOLD_TYPE_FIXED,
  QUOTA_THRESHOLD_TYPE_PERCENTAGE,
  type QuotaThresholdType,
} from '@/constants/provider'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'

// QuotaNotifyToggle.vue edits notification enablement, threshold amount, and threshold
// interpretation for one quota dimension. The owner supplies nullable drafts, and the
// selector emits a fixed-amount or percentage type while leaving persistence and
// inherited defaults to the parent.
// usdUnitSymbol labels monetary quota inputs with the configured display symbol used by
// the balance presentation helper.
const { usdUnitSymbol } = useBalanceDisplay()

defineProps<{
  // enabled, threshold, and thresholdType are nullable drafts owned by the parent quota
  // row. The selected type determines whether the threshold is a USD amount or a
  // percentage.
  enabled: boolean | null // Nullable per-dimension notification switch.
  threshold: number | null // Amount interpreted by thresholdType.
  thresholdType: QuotaThresholdType | null // Selects USD or percentage units.
}>()

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  'update:enabled': [value: boolean | null] // Replacement enabled draft value.
  'update:threshold': [value: number | null] // Replacement threshold draft value.
  'update:thresholdType': [value: QuotaThresholdType | null] // Replacement threshold type draft value.
}>()

// thresholdTypeOptions lets each quota notification threshold use a fixed USD amount or a
// percentage of its limit.
const thresholdTypeOptions = computed(() => [
  { value: QUOTA_THRESHOLD_TYPE_FIXED, label: usdUnitSymbol },
  { value: QUOTA_THRESHOLD_TYPE_PERCENTAGE, label: '%' },
])

// onThresholdTypeChange emits the selected notification threshold interpretation to the
// owning row. Only the percentage option selects percentage mode; other selector values
// map to the fixed monetary threshold type.
const onThresholdTypeChange = (value: string | number | boolean | null) => {
  emit(
    'update:thresholdType',
    value === QUOTA_THRESHOLD_TYPE_PERCENTAGE
      ? QUOTA_THRESHOLD_TYPE_PERCENTAGE
      : QUOTA_THRESHOLD_TYPE_FIXED,
  )
}
</script>

<template>
  <div class="flex items-center gap-1.5">
    <Toggle
      :model-value="!!enabled"
      size="sm"
      off-tone="soft"
      @update:model-value="emit('update:enabled', $event)"
    />
    <template v-if="enabled">
      <input
        :value="threshold"
        @input="
          emit('update:threshold', parseFloat(($event.target as HTMLInputElement).value) || null)
        "
        type="number"
        min="0"
        :max="thresholdType === QUOTA_THRESHOLD_TYPE_PERCENTAGE ? 100 : undefined"
        :step="thresholdType === QUOTA_THRESHOLD_TYPE_PERCENTAGE ? 1 : 0.01"
        class="input py-1 text-sm flex-1 min-w-0"
      />
      <Select
        :model-value="thresholdType || QUOTA_THRESHOLD_TYPE_FIXED"
        :options="thresholdTypeOptions"
        class="quota-threshold-type-select w-[4.5rem] flex-shrink-0 text-xs"
        :searchable="false"
        @change="onThresholdTypeChange"
      />
    </template>
  </div>
</template>

<style scoped>
.quota-threshold-type-select :deep(.input-trigger) {
  @apply rounded-control px-2 py-1 text-xs;
}

.quota-threshold-type-select :deep(.select-value) {
  @apply text-center;
}
</style>
