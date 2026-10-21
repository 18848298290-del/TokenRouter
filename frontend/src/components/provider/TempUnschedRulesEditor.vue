<template>
  <RuleListEditor
    :items="modelValue"
    :add-label="t('admin.providers.tempUnschedulable.addRule')"
    add-placement="footer"
    variant="card"
    :item-label="(index) => t('admin.providers.tempUnschedulable.ruleIndex', { index: index + 1 })"
    reorderable
    @add="appendRule(createEmptyRule())"
    @remove="removeRule"
    @move="moveRule"
  >
    <template #header-extra>
      <SettingsNotice>{{ t('admin.providers.tempUnschedulable.notice') }}</SettingsNotice>
      <div class="flex flex-wrap gap-2">
        <button
          v-for="preset in presets"
          :key="preset.label"
          type="button"
          class="rounded-control bg-gray-100 px-3 py-1.5 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-200 dark:bg-dark-700 dark:text-gray-300 dark:hover:bg-dark-600"
          @click="appendRule({ ...preset.rule })"
        >
          + {{ preset.label }}
        </button>
      </div>
    </template>
    <template #row="{ item: rule }">
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label class="input-label">{{ t('admin.providers.tempUnschedulable.errorCode') }}</label>
          <input
            v-model.number="rule.error_code"
            type="number"
            min="100"
            max="599"
            class="input"
            :placeholder="t('admin.providers.tempUnschedulable.errorCodePlaceholder')"
          />
        </div>
        <div>
          <label class="input-label">{{
            t('admin.providers.tempUnschedulable.durationMinutes')
          }}</label>
          <input
            v-model.number="rule.duration_minutes"
            type="number"
            min="1"
            class="input"
            :placeholder="t('admin.providers.tempUnschedulable.durationPlaceholder')"
          />
        </div>
        <div class="sm:col-span-2">
          <label class="input-label">{{ t('admin.providers.tempUnschedulable.keywords') }}</label>
          <input
            v-model="rule.keywords"
            type="text"
            class="input"
            :placeholder="t('admin.providers.tempUnschedulable.keywordsPlaceholder')"
          />
          <p class="input-hint">{{ t('admin.providers.tempUnschedulable.keywordsHint') }}</p>
        </div>
        <div class="sm:col-span-2">
          <label class="input-label">{{
            t('admin.providers.tempUnschedulable.description')
          }}</label>
          <input
            v-model="rule.description"
            type="text"
            class="input"
            :placeholder="t('admin.providers.tempUnschedulable.descriptionPlaceholder')"
          />
        </div>
      </div>
    </template>
  </RuleListEditor>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'

// TempUnschedRulesEditor.vue edits ordered temporary unschedulability rules using row
// drafts. Keywords stay comma-separated text until the parent builds the API payload, and
// preset or manual additions emit an updated array while existing row objects remain
// shared with the parent.

// TempUnschedRuleForm represents editable matching criteria and a temporary scheduling
// cooldown.
export interface TempUnschedRuleForm {
  // error_code and keywords describe the upstream response match, duration_minutes
  // supplies its cooldown, and description is explanatory text. The parent parses the
  // comma-separated keywords when constructing the persisted policy.
  error_code: number | null // Upstream HTTP status code.
  keywords: string // Comma-separated error matching words.
  duration_minutes: number | null // Cooldown duration in minutes.
  description: string // Explanatory text for display.
}

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  modelValue: TempUnschedRuleForm[] // Parent-owned selected value.
}>()

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  'update:modelValue': [rules: TempUnschedRuleForm[]] // Replacement model value draft value.
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()

// presets supplies localized overload, rate-limit, and unavailable-server rules with
// fixed status codes and editable cooldown durations.
const presets = computed(() => [
  {
    label: t('admin.providers.tempUnschedulable.presets.overloadLabel'),
    rule: {
      error_code: 529,
      keywords: 'overloaded, too many',
      duration_minutes: 60,
      description: t('admin.providers.tempUnschedulable.presets.overloadDesc'),
    },
  },
  {
    label: t('admin.providers.tempUnschedulable.presets.rateLimitLabel'),
    rule: {
      error_code: 429,
      keywords: 'rate limit, too many requests',
      duration_minutes: 10,
      description: t('admin.providers.tempUnschedulable.presets.rateLimitDesc'),
    },
  },
  {
    label: t('admin.providers.tempUnschedulable.presets.unavailableLabel'),
    rule: {
      error_code: 503,
      keywords: 'unavailable, maintenance',
      duration_minutes: 30,
      description: t('admin.providers.tempUnschedulable.presets.unavailableDesc'),
    },
  },
])

// createEmptyRule creates an empty rule draft with a thirty-minute duration. Status code
// remains null and keywords stay blank until the administrator supplies matching criteria
// in the row editor.
const createEmptyRule = (): TempUnschedRuleForm => ({
  error_code: null,
  keywords: '',
  duration_minutes: 30,
  description: '',
})

// appendRule emits a replacement array with the supplied rule appended at the end. The
// rule object is retained by reference, so callers needing independent preset drafts must
// pass a copy.
const appendRule = (rule: TempUnschedRuleForm) => {
  emit('update:modelValue', [...props.modelValue, rule])
}

// removeRule emits every rule except the one at the supplied index. Surviving rule
// objects retain their identities and relative order for the parent and row editor to
// reuse.
const removeRule = (index: number) => {
  emit(
    'update:modelValue',
    props.modelValue.filter((_, ruleIndex) => ruleIndex !== index),
  )
}

// moveRule swaps two rule positions in a copied array and emits the result. Callers must
// provide valid indices; the template uses adjacent positions and disables movement at
// either end.
const moveRule = (from: number, to: number) => {
  const rules = [...props.modelValue]
  const current = rules[from]
  rules[from] = rules[to]
  rules[to] = current
  emit('update:modelValue', rules)
}
</script>
