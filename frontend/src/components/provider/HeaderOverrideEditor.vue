<template>
  <RuleListEditor
    :items="rows"
    :add-label="t('admin.providers.headerOverride.addRow')"
    add-placement="footer"
    :empty-text="t('admin.providers.headerOverride.empty')"
    @add="addRow"
    @remove="removeRow"
  >
    <template #row="{ item: row }">
      <div class="grid min-w-0 grid-cols-1 gap-2 sm:grid-cols-2">
        <input
          v-model="row.name"
          type="text"
          class="input min-w-0 font-mono"
          :placeholder="t('admin.providers.headerOverride.namePlaceholder')"
          :aria-label="t('admin.providers.headerOverride.namePlaceholder')"
        />
        <input
          v-model="row.value"
          type="text"
          class="input min-w-0"
          :placeholder="t('admin.providers.headerOverride.valuePlaceholder')"
          :aria-label="t('admin.providers.headerOverride.valuePlaceholder')"
        />
      </div>
    </template>
    <template #footer>
      <div class="flex flex-wrap gap-2">
        <HeaderOverrideJsonTools :rows="rows" @update:rows="emit('update:rows', $event)" />
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.providers.headerOverride.emptyValueHint') }}
      </p>
    </template>
  </RuleListEditor>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import HeaderOverrideJsonTools from './HeaderOverrideJsonTools.vue'
import type { HeaderOverrideRow } from '@/components/provider/form/headerOverrides'

// HeaderOverrideEditor.vue edits the ordered header override rows supplied by a provider
// form. Add and remove operations emit replacement arrays, while field bindings edit the
// existing row objects and leave validation and credential serialization to the owning
// form.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  rows: HeaderOverrideRow[] // Parent-owned editable row objects.
}>()

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  // update:rows supplies a replacement header-row array to the parent provider form.
  // Retained rows keep their object identities, while addition supplies a new empty
  // draft for editing.
  (e: 'update:rows', rows: HeaderOverrideRow[]): void
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()

// addRow emits a replacement row array ending in an empty header draft. Existing row
// objects retain their identities for field bindings and the shared list editor keys.
const addRow = () => {
  emit('update:rows', [...props.rows, { name: '', value: '' }])
}

// removeRow emits the rows remaining after the supplied position is removed. The retained
// objects are shared with the parent, and an unmatched index leaves all rows present.
const removeRow = (index: number) => {
  emit(
    'update:rows',
    props.rows.filter((_, i) => i !== index),
  )
}
</script>
