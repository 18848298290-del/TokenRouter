<template>
  <ModelMappingEditor
    :model-value="modelValue"
    :title="title"
    :hint="hint ?? t('admin.providers.mapRequestModels')"
    :add-label="t('admin.providers.addMapping')"
    :empty-text="t('admin.providers.modelMappingEmpty')"
    :source-label="sourcePlaceholder ?? t('admin.providers.requestModel')"
    :target-label="targetPlaceholder ?? t('admin.providers.actualModel')"
    :source-placeholder="sourcePlaceholder ?? t('admin.providers.requestModel')"
    :target-placeholder="targetPlaceholder ?? t('admin.providers.actualModel')"
    :field-errors="fieldErrors"
    :test-id="testId"
    @update:model-value="emit('update:modelValue', $event)"
    @add="emit('add', $event)"
    @remove="(row, index) => emit('remove', row, index)"
  >
    <template v-if="$slots['header-actions']" #header-actions>
      <slot name="header-actions" />
    </template>
  </ModelMappingEditor>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ModelMappingEditor from '@/components/common/ModelMappingEditor.vue'
import {
  WILDCARD_ONLY_RULES,
  validateModelMappingRows,
  type ModelMappingRow,
} from '@/utils/modelMappingRules'

// ProviderModelMappingEditor.vue connects provider model mapping labels and wildcard
// diagnostics to the shared row editor. Optional live validation supplies field errors,
// while row changes and add or remove events are forwarded to the parent that owns
// payload construction.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  modelValue: ModelMappingRow[] // Parent-owned selected value.
  title?: string // Displayed section title.
  hint?: string // Supplemental field help text.
  sourcePlaceholder?: string // Request model input placeholder.
  targetPlaceholder?: string // Upstream model input placeholder.
  // Enables live wildcard diagnostics; the parent still validates and builds the
  // persisted mapping.
  wildcardValidation?: boolean
  testId?: string // Stable test selector value.
}>()

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  'update:modelValue': [rows: ModelMappingRow[]] // Replacement model value draft value.
  add: [row: ModelMappingRow] // Mapping row addition notification.
  remove: [row: ModelMappingRow, index: number] // Mapping row removal notification.
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()

// fieldErrors supplies optional wildcard diagnostics to the shared editor; payload
// construction still applies the owning form validation rules.
const fieldErrors = computed(() => {
  if (!props.wildcardValidation) return undefined
  return validateModelMappingRows(props.modelValue, WILDCARD_ONLY_RULES).map((issue) => ({
    from: issue.from ? t('admin.providers.wildcardOnlyAtEnd') : undefined,
    to: issue.to ? t('admin.providers.targetNoWildcard') : undefined,
  }))
})
</script>
