<template>
  <div class="settings-section space-y-6" data-group-field="routing-policy">
    <SettingsSection>
      <ModelMappingEditor
        :model-value="mappingRows"
        :title="t('admin.groups.routingPolicy.mapping')"
        :hint="t('admin.groups.routingPolicy.mappingHint')"
        title-style="section"
        :add-label="t('admin.groups.routingPolicy.addMapping')"
        :empty-text="t('admin.groups.routingPolicy.mappingEmpty')"
        :source-label="t('admin.groups.routingPolicy.source')"
        :target-label="t('admin.groups.routingPolicy.target')"
        :source-placeholder="t('admin.groups.routingPolicy.source')"
        :target-placeholder="t('admin.groups.routingPolicy.target')"
        :error="mappingError"
        @update:model-value="onMappingRowsChange"
      >
        <template #footer>
          <input
            class="sr-only"
            tabindex="-1"
            :value="mappingError ? '' : 'valid'"
            required
            :aria-label="t('admin.groups.routingPolicy.mapping')"
          />
        </template>
      </ModelMappingEditor>
    </SettingsSection>
    <SettingsSection>
      <SettingToggleRow
        :id="`${idPrefix}-restrict-models`"
        :model-value="value.restrict_models"
        :label="t('admin.groups.routingPolicy.restrict')"
        :hint="t('admin.groups.routingPolicy.allowlistHint')"
        setting="restrict_models"
        @update:model-value="update({ restrict_models: $event })"
      />
      <template v-if="value.restrict_models">
        <label :for="`${idPrefix}-restriction-source`" class="input-label">{{
          t('admin.groups.settings.restrictionSource')
        }}</label>
        <Select
          :id="`${idPrefix}-restriction-source`"
          :aria-label="t('admin.groups.settings.restrictionSource')"
          :model-value="value.restriction_model_source || 'group_mapped'"
          :options="sourceOptions"
          @update:model-value="
            update({
              restriction_model_source: String(
                $event,
              ) as GroupRoutingPolicy['restriction_model_source'],
            })
          "
        />
        <ModelTagInput
          :aria-label="t('admin.groups.settings.allowedModels')"
          :models="value.allowed_models"
          @update:models="update({ allowed_models: $event })"
        />
      </template>
    </SettingsSection>
  </div>
</template>
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GroupRoutingPolicy } from '@/api/groups.types'
import Select from '@/components/common/Select.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import ModelMappingEditor from '@/components/common/ModelMappingEditor.vue'
import ModelTagInput from '@/components/admin/pricing/ModelTagInput.vue'
import { findModelConflict } from '@/components/admin/pricing/types'
import { cloneRoutingPolicy } from './routingPolicy'
import {
  mappingRowsToRecord,
  recordToMappingRows,
  type ModelMappingRow,
} from '@/utils/modelMappingRules'

// GroupRoutingPolicyFields.vue edits group model mappings and model restrictions through a
// local row list. Editable rows can contain blank sources or repeated patterns while the
// published mapping record cannot represent those draft states. Validation therefore reads the
// rows retained by the component.
//
// A serialized signature marks the mapping most recently emitted to the parent. When that
// record returns through the prop, the watcher keeps the local rows so intermediate input is
// retained. A different mapping rebuilds the rows from the external record. Other policy
// changes merge into a cloned policy and keep its remaining settings.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = withDefaults(
  defineProps<{
    // modelValue supplies the parent-owned value edited by these controls. Updates are emitted to
    // the parent for publication back through this prop.
    modelValue?: GroupRoutingPolicy
    idPrefix?: string // field identifier prefix
  }>(),
  {
    idPrefix: 'group-model-policy',
  },
)
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  'update:modelValue': [
    value: GroupRoutingPolicy, // replacement field value
  ] // replacement parent-owned field value
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
// value clones the supplied routing policy and fills defaults before the editor merges a field
// update into it.
const value = computed(() => cloneRoutingPolicy(props.modelValue))
// sourceOptions translates the requested, group-mapped, and upstream model identities used when
// evaluating model restrictions.
const sourceOptions = computed(() =>
  ['requested', 'group_mapped', 'upstream'].map((key) => ({
    value: key,
    label: t(`admin.groups.routingPolicy.basis.${key}`),
  })),
)

// mappingRows retains incomplete and duplicate input rows; lastPublished marks the serialized
// mapping most recently emitted to the parent.
const mappingRows = ref<ModelMappingRow[]>([])
let lastPublished = ''
// Ignore the mapping record just emitted by this editor so empty or duplicate rows survive
// the parent prop round trip.
watch(
  () => [props.modelValue?.model_mapping] as const,
  () => {
    const mapping = value.value.model_mapping
    const signature = JSON.stringify(mapping)
    if (signature === lastPublished) return
    mappingRows.value = recordToMappingRows(mapping)
  },
  { immediate: true, deep: true },
)
// mappingError checks the local rows for blank endpoints and pattern conflicts that the
// serialized record cannot fully represent.
const mappingError = computed(() => {
  const sources = mappingRows.value.map((row) => row.from.trim())
  if (sources.some((source) => !source) || mappingRows.value.some((row) => !row.to.trim()))
    return t('admin.groups.routingPolicy.incompleteMapping')
  return findModelConflict(sources) ? t('admin.groups.routingPolicy.conflict') : ''
})
// update emits a complete routing policy with the supplied partial field changes.
function update(patch: Partial<GroupRoutingPolicy>) {
  emit('update:modelValue', { ...value.value, ...patch })
}
// onMappingRowsChange stores editable rows and publishes their record representation. It
// records the serialized mapping before emission so the prop watcher can recognize the
// returning value. Blank and duplicate rows remain in local state even when serialization
// collapses them.
//
// An externally supplied mapping with a different signature is handled by the watcher, which
// rebuilds the row draft. This function owns the outgoing half of that round trip and the
// signature used to distinguish it.
function onMappingRowsChange(rows: ModelMappingRow[]) {
  mappingRows.value = rows
  const mapping = mappingRowsToRecord(rows)
  lastPublished = JSON.stringify(mapping)
  update({ model_mapping: mapping })
}
</script>
