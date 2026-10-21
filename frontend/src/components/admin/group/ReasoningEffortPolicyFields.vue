<template>
  <div class="grid min-w-0 grid-cols-1 gap-4 md:grid-cols-2">
    <div>
      <label :for="`${idPrefix}-max-effort`" class="input-label">
        {{ t('admin.groups.form.maxReasoningEffort') }}
      </label>
      <Select
        :id="`${idPrefix}-max-effort`"
        :model-value="maxEffort"
        :options="reasoningEffortOptions"
        :placeholder="t('admin.groups.form.maxReasoningEffortUnlimited')"
        :aria-label="t('admin.groups.form.maxReasoningEffort')"
        :searchable="false"
        clearable
        @update:model-value="updateMaxEffort"
      />
      <p class="input-hint">{{ t('admin.groups.form.maxReasoningEffortHint') }}</p>
    </div>

    <div>
      <label :for="`${idPrefix}-over-limit`" class="input-label">
        {{ t('admin.groups.form.maxReasoningEffortOverLimit') }}
      </label>
      <Select
        :id="`${idPrefix}-over-limit`"
        :model-value="overLimit"
        :options="overLimitOptions"
        :aria-label="t('admin.groups.form.maxReasoningEffortOverLimit')"
        :searchable="false"
        :disabled="!maxEffort"
        @update:model-value="updateOverLimit"
      />
      <p class="input-hint">{{ t('admin.groups.form.maxReasoningEffortOverLimitHint') }}</p>
    </div>

    <RuleListEditor
      class="border-t border-gray-200 pt-6 dark:border-dark-600 md:col-span-2"
      :items="mappings"
      :item-key="(group) => group.id"
      :title="t('admin.groups.form.reasoningEffortMappings')"
      :hint="t('admin.groups.form.reasoningEffortMappingsHint')"
      title-style="section"
      :add-label="t('admin.groups.form.addReasoningEffortMapping')"
      :empty-text="t('admin.groups.form.reasoningEffortMappingsEmpty')"
      :remove-label="t('admin.groups.form.removeReasoningEffortMapping')"
      variant="card"
      :item-label="
        (index) => t('admin.groups.form.reasoningEffortMappingIndex', { index: index + 1 })
      "
      @add="addGroup"
      @remove="(index) => removeGroup(mappings[index].id)"
    >
      <template #row="{ item: group }">
        <div class="space-y-4">
          <div class="grid grid-cols-1 items-start gap-3 md:grid-cols-2">
            <div>
              <label :for="`${idPrefix}-${group.id}-match-type`" class="input-label">
                {{ t('admin.groups.form.reasoningEffortMatchType') }}
              </label>
              <Select
                :id="`${idPrefix}-${group.id}-match-type`"
                :model-value="group.match_type"
                :options="matchTypeOptions"
                :placeholder="t('admin.groups.form.reasoningEffortMatchTypePlaceholder')"
                :error="showValidation && !!groupErrors(group.id).match_type"
                :aria-label="t('admin.groups.form.reasoningEffortMatchType')"
                :searchable="false"
                clearable
                @update:model-value="updateGroup(group.id, 'match_type', $event)"
              />
              <p
                v-if="showValidation && groupErrors(group.id).match_type"
                class="input-error-text"
                role="alert"
              >
                {{ mappingErrorText(groupErrors(group.id).match_type) }}
              </p>
            </div>

            <div class="min-w-0">
              <label :for="`${idPrefix}-${group.id}-model`" class="input-label">
                {{ t('admin.groups.form.reasoningEffortModel') }}
              </label>
              <input
                :id="`${idPrefix}-${group.id}-model`"
                :value="group.model"
                type="text"
                maxlength="200"
                autocomplete="off"
                class="input"
                :placeholder="t('admin.groups.form.reasoningEffortModelPlaceholder')"
                :aria-label="t('admin.groups.form.reasoningEffortModel')"
                @input="onModelInput(group.id, $event)"
              />
            </div>
          </div>

          <p
            v-if="showValidation && groupErrors(group.id).duplicateScope"
            class="text-xs text-red-500"
            role="alert"
          >
            {{ mappingErrorText(groupErrors(group.id).duplicateScope) }}
          </p>

          <RuleListEditor
            :items="group.pairs"
            :item-key="(pair) => pair.id"
            :add-label="t('admin.groups.form.addReasoningEffortPair')"
            add-placement="footer"
            :remove-label="t('admin.groups.form.removeReasoningEffortPair')"
            @add="addPair(group.id)"
            @remove="(index) => removePair(group.id, group.pairs[index].id)"
          >
            <template #header-extra>
              <!-- Shared column headings replace per-row labels so remove buttons remain horizontally aligned with their mapping selectors. -->
              <div
                class="hidden gap-2 pr-11 text-sm font-medium text-primary-900 md:grid md:grid-cols-[minmax(0,1fr)_1.25rem_minmax(0,1fr)] dark:text-dark-50"
              >
                <span>{{ t('admin.groups.form.reasoningEffortFrom') }}</span>
                <span />
                <span>{{ t('admin.groups.form.reasoningEffortTo') }}</span>
              </div>
            </template>
            <template #row="{ item: pair }">
              <div
                class="grid grid-cols-1 items-start gap-2 md:grid-cols-[minmax(0,1fr)_1.25rem_minmax(0,1fr)]"
              >
                <div class="min-w-0">
                  <Select
                    :id="`${idPrefix}-${pair.id}-from`"
                    :model-value="pair.from"
                    :options="reasoningEffortMappingOptions"
                    :placeholder="t('admin.groups.form.reasoningEffortFromPlaceholder')"
                    :error="showValidation && !!pairErrors(pair.id).from"
                    :aria-label="t('admin.groups.form.reasoningEffortFrom')"
                    :searchable="false"
                    clearable
                    @update:model-value="updatePair(group.id, pair.id, 'from', $event)"
                  />
                  <p
                    v-if="showValidation && pairErrors(pair.id).from"
                    class="input-error-text"
                    role="alert"
                  >
                    {{ mappingErrorText(pairErrors(pair.id).from) }}
                  </p>
                </div>

                <div
                  class="hidden h-9 items-center justify-center text-gray-400 md:flex dark:text-dark-400"
                >
                  <Icon name="arrowRight" size="sm" />
                </div>

                <div class="min-w-0">
                  <Select
                    :id="`${idPrefix}-${pair.id}-to`"
                    :model-value="pair.to"
                    :options="reasoningEffortMappingOptions"
                    :placeholder="t('admin.groups.form.reasoningEffortToPlaceholder')"
                    :error="showValidation && !!pairErrors(pair.id).to"
                    :aria-label="t('admin.groups.form.reasoningEffortTo')"
                    :searchable="false"
                    clearable
                    @update:model-value="updatePair(group.id, pair.id, 'to', $event)"
                  />
                  <p
                    v-if="showValidation && pairErrors(pair.id).to"
                    class="input-error-text"
                    role="alert"
                  >
                    {{ mappingErrorText(pairErrors(pair.id).to) }}
                  </p>
                </div>
              </div>
            </template>
          </RuleListEditor>
        </div>
      </template>
    </RuleListEditor>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import {
  createReasoningEffortMappingPair,
  createReasoningEffortMappingRow,
  normalizeReasoningEffortOverLimit,
  normalizeReasoningEffortMatchType,
  reasoningEffortMappingOptionsForPlatform,
  reasoningEffortOptionsForPlatform,
  reasoningEffortOverLimitDeny,
  reasoningEffortOverLimitDowngrade,
  validateReasoningEffortMappings,
  type ReasoningEffortMappingErrorCode,
  type ReasoningEffortMappingRow,
} from '@/components/admin/group/groupsReasoningEffort'

// ReasoningEffortPolicyFields.vue edits a group's reasoning ceiling and scoped effort mappings.
// Mapping groups and pairs retain identifiers used by validation errors, while parent-owned
// arrays are replaced through update events. Removing a group's final pair also removes that
// empty group.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  idPrefix: string // field identifier prefix
  // maxEffort and overLimit are parent-owned ceiling settings. An empty maxEffort means no
  // ceiling and disables the overLimit selector; its action applies only with a nonempty ceiling.
  maxEffort: string // configured reasoning ceiling
  overLimit: string // ceiling excess action
  mappings: ReasoningEffortMappingRow[] // editable reasoning mappings
}>()

// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  // The update events replace the selected ceiling, over-limit action, or complete mapping
  // draft. The parent publishes those values back through props after accepting the field edit.
  (event: 'update:maxEffort', value: string): void
  // The update events replace the selected ceiling, over-limit action, or complete mapping
  // draft. The parent publishes those values back through props after accepting the field edit.
  (event: 'update:overLimit', value: string): void
  // The update events replace the selected ceiling, over-limit action, or complete mapping
  // draft. The parent publishes those values back through props after accepting the field edit.
  (event: 'update:mappings', value: ReasoningEffortMappingRow[]): void
}>()

// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
// showValidation controls error visibility, while the adjacent choice lists translate supported
// ceiling, over-limit, and scope values.
const showValidation = ref(false)
const reasoningEffortOptions = computed(() => reasoningEffortOptionsForPlatform())
const reasoningEffortMappingOptions = computed(() => reasoningEffortMappingOptionsForPlatform())
const overLimitOptions = computed(() => [
  {
    value: reasoningEffortOverLimitDowngrade,
    label: t('admin.groups.form.maxReasoningEffortOverLimitDowngrade'),
  },
  {
    value: reasoningEffortOverLimitDeny,
    label: t('admin.groups.form.maxReasoningEffortOverLimitDeny'),
  },
])
const matchTypeOptions = computed(() => [
  {
    value: 'exact',
    label: t('admin.groups.form.reasoningEffortMatchExact'),
  },
  {
    value: 'prefix',
    label: t('admin.groups.form.reasoningEffortMatchPrefix'),
  },
  {
    value: 'suffix',
    label: t('admin.groups.form.reasoningEffortMatchSuffix'),
  },
])
// validationErrors indexes shared validator results by stable group and pair IDs used by the
// corresponding mapping fields.
const validationErrors = computed(() => validateReasoningEffortMappings(props.mappings))

// asString converts selector values to text for the reasoning draft. Null becomes an empty
// string so clearing a selector follows the same path as an empty textual input.
const asString = (value: string | number | boolean | null): string =>
  value == null ? '' : String(value)

// groupErrors returns validation messages keyed by the selected mapping group identifier.
const groupErrors = (id: string) => validationErrors.value[id] ?? {}
// pairErrors returns validation messages keyed by the selected mapping pair identifier.
const pairErrors = (id: string) => validationErrors.value[id] ?? {}

// updateMaxEffort emits the selected reasoning ceiling as a draft string.
const updateMaxEffort = (value: string | number | boolean | null) => {
  emit('update:maxEffort', asString(value))
}

// updateOverLimit normalizes the over-limit action before emitting its selected value.
const updateOverLimit = (value: string | number | boolean | null) => {
  emit('update:overLimit', normalizeReasoningEffortOverLimit(asString(value)))
}

// updateGroup replaces the matching group's model or matching mode. Matching modes pass through
// the shared normalizer, and unaffected group objects retain their identity in the emitted
// array.
const updateGroup = (
  id: string,
  field: 'match_type' | 'model',
  value: string | number | boolean | null,
) => {
  const nextValue =
    field === 'match_type' ? normalizeReasoningEffortMatchType(asString(value)) : asString(value)
  emit(
    'update:mappings',
    props.mappings.map((group) => (group.id === id ? { ...group, [field]: nextValue } : group)),
  )
}

// onModelInput forwards the input value to the matching reasoning group draft.
const onModelInput = (id: string, event: Event) => {
  const target = event.target as HTMLInputElement | null
  updateGroup(id, 'model', target?.value ?? '')
}

// updatePair replaces one source or destination effort in the matching group.
const updatePair = (
  groupId: string,
  pairId: string,
  field: 'from' | 'to',
  value: string | number | boolean | null,
) => {
  emit(
    'update:mappings',
    props.mappings.map((group) =>
      group.id === groupId
        ? {
            ...group,
            pairs: group.pairs.map((pair) =>
              pair.id === pairId ? { ...pair, [field]: asString(value) } : pair,
            ),
          }
        : group,
    ),
  )
}

// addGroup emits the mapping list with a fresh blank group appended.
const addGroup = () => {
  emit('update:mappings', [...props.mappings, createReasoningEffortMappingRow()])
}

// removeGroup emits the mapping list after excluding the identified group.
const removeGroup = (id: string) => {
  emit(
    'update:mappings',
    props.mappings.filter((group) => group.id !== id),
  )
}

// addPair appends a fresh blank effort pair to the identified group.
const addPair = (groupId: string) => {
  emit(
    'update:mappings',
    props.mappings.map((group) =>
      group.id === groupId
        ? { ...group, pairs: [...group.pairs, createReasoningEffortMappingPair()] }
        : group,
    ),
  )
}

// removePair excludes the identified pair from its mapping group. When that removal leaves the
// group empty, the group is also omitted from the emitted parent-owned list.
const removePair = (groupId: string, pairId: string) => {
  emit(
    'update:mappings',
    props.mappings.flatMap((group) => {
      if (group.id !== groupId) return [group]
      const pairs = group.pairs.filter((pair) => pair.id !== pairId)
      return pairs.length > 0 ? [{ ...group, pairs }] : []
    }),
  )
}

// mappingErrorText resolves a mapping validation code to its translated message. Missing error
// codes return empty text so unaffected fields render no validation message in the editor.
const mappingErrorText = (code: ReasoningEffortMappingErrorCode | undefined): string =>
  code ? t(`admin.groups.form.${code}`) : ''

// validate exposes mapping errors and reports whether the current draft is valid.
const validate = (): boolean => {
  showValidation.value = true
  return Object.keys(validationErrors.value).length === 0
}

// resetValidation hides mapping errors until the next explicit validation attempt.
const resetValidation = () => {
  showValidation.value = false
}

defineExpose({ validate, resetValidation })
</script>
