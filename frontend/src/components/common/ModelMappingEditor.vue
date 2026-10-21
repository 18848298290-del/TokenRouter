<template>
  <RuleListEditor
    :items="modelValue"
    :title="title"
    :hint="hint"
    :title-style="titleStyle"
    :add-label="addLabel"
    :add-placement="addPlacement"
    :empty-text="emptyText"
    :max="max"
    :disabled="disabled"
    :error="error"
    :test-id="testId"
    @add="addRow"
    @remove="removeRow"
  >
    <template v-if="$slots['title-suffix']" #title-suffix>
      <slot name="title-suffix" />
    </template>
    <template v-if="$slots['header-actions']" #header-actions>
      <slot name="header-actions" />
    </template>
    <template v-if="$slots['header-extra']" #header-extra>
      <slot name="header-extra" />
    </template>
    <template #row="{ item, index }">
      <div
        class="grid min-w-0 grid-cols-1 items-start gap-2 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]"
      >
        <div class="min-w-0">
          <input
            v-model="item.from"
            type="text"
            class="input min-w-0"
            :class="{ 'font-mono': monospace, 'input-error': fieldErrors?.[index]?.from }"
            :placeholder="sourcePlaceholder"
            :aria-label="sourceLabel"
            :aria-invalid="fieldErrors?.[index]?.from ? 'true' : undefined"
            :disabled="disabled"
            :data-testid="testId ? `${testId}-source-${index}` : undefined"
            @input="publish"
          />
          <p v-if="fieldErrors?.[index]?.from" class="input-error-text" role="alert">
            {{ fieldErrors[index]?.from }}
          </p>
        </div>
        <div class="hidden h-9 items-center text-gray-400 sm:flex dark:text-dark-400">
          <Icon name="arrowRight" size="sm" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <input
            v-model="item.to"
            type="text"
            class="input min-w-0"
            :class="{ 'font-mono': monospace, 'input-error': fieldErrors?.[index]?.to }"
            :placeholder="targetPlaceholder"
            :aria-label="targetLabel"
            :aria-invalid="fieldErrors?.[index]?.to ? 'true' : undefined"
            :disabled="disabled"
            :data-testid="testId ? `${testId}-target-${index}` : undefined"
            @input="publish"
          />
          <p v-if="fieldErrors?.[index]?.to" class="input-error-text" role="alert">
            {{ fieldErrors[index]?.to }}
          </p>
        </div>
      </div>
    </template>
    <template v-if="$slots.footer" #footer>
      <slot name="footer" />
    </template>
  </RuleListEditor>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import RuleListEditor from './RuleListEditor.vue'
import type { ModelMappingRow } from '@/utils/modelMappingRules'

// ModelMappingEditor.vue edits source and target model pairs inside the shared
// rule-list shell. Rows are mutable objects: input bindings update their fields in
// place, then publish a replacement array so parent consumers observe a collection
// update. Field errors remain aligned to row indices.
//
// Object identity matters because RuleListEditor uses it for display keys. Adding and
// removing rows therefore keep surviving objects intact, and lifecycle events include
// the affected row after the model update. The parent owns validation decisions and
// persistence, while this component forwards labels, slots, and list controls.

// ModelMappingFieldErrors aligns translated source and target errors with row indices.
export type ModelMappingFieldErrors = Array<
  | {
      from?: string // Source model error text.
      to?: string // Target model error text.
    }
  | undefined
>

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = withDefaults(
  defineProps<{
    // Owns mutable row objects edited in place by the inputs; model updates replace
    // the array while sharing retained rows.
    modelValue: ModelMappingRow[]
    sourceLabel: string // Source input accessible name.
    targetLabel: string // Target input accessible name.
    sourcePlaceholder?: string // Source input placeholder text.
    targetPlaceholder?: string // Target input placeholder text.
    title?: string // Dialog or section heading.
    hint?: string // Optional explanatory hint text.
    titleStyle?: 'label' | 'section' // Editor heading style recipe.
    addLabel?: string // Add action label override.
    addPlacement?: 'header' | 'footer' // Add button placement selector.
    emptyText?: string // Empty collection message text.
    // Disables adding when the collection reaches this count; undefined leaves the
    // maximum unbounded in the shared list shell.
    max?: number
    disabled?: boolean // Disables user control interaction.
    monospace?: boolean // Monospace input typography flag.
    // Translated errors indexed by row; missing entries leave that row with ordinary
    // input styling and no field alerts.
    fieldErrors?: ModelMappingFieldErrors
    error?: string // Visible field error feedback.
    testId?: string // Optional test identifier prefix.
  }>(),
  {
    titleStyle: 'label',
    addPlacement: 'header',
    monospace: true,
  },
)

// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<{
  'update:modelValue': [rows: ModelMappingRow[]] // Replacement parent model payload.
  add: [row: ModelMappingRow] // Added mapping row notification.
  remove: [row: ModelMappingRow, index: number] // Removed row and index.
}>()

// publish emits a new array containing the current mutable row objects. Input bindings
// have already updated the edited field, so retaining row identity keeps
// RuleListEditor keys and focused inputs stable.
const publish = () => {
  // Publish a fresh collection after the input binding changes its row; sharing that
  // row keeps its display key and focused element stable.
  emit('update:modelValue', [...props.modelValue])
}

// addRow appends an empty mapping through a replacement model array. It publishes the
// model update before the add event, and existing row objects remain shared with the
// parent.
const addRow = () => {
  const row: ModelMappingRow = { from: '', to: '' }
  emit('update:modelValue', [...props.modelValue, row])
  emit('add', row)
}

// removeRow removes the indexed mapping through a replacement model array. It
// publishes remove with the removed object and index when that row exists, while
// surviving objects retain their identity.
const removeRow = (index: number) => {
  const row = props.modelValue[index]
  emit(
    'update:modelValue',
    props.modelValue.filter((_, rowIndex) => rowIndex !== index),
  )
  if (row) emit('remove', row, index)
}
</script>
