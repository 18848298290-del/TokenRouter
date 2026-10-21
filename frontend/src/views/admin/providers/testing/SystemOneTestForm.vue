<template>
  <div class="space-y-4" data-testid="systemone-test-form">
    <div class="space-y-2">
      <div class="flex items-center justify-between gap-2">
        <label :for="stateFieldId" class="input-label mb-0">{{
          t('admin.providers.decisionTest.state')
        }}</label>
        <div
          v-segmented
          class="segmented"
          role="radiogroup"
          :aria-label="t('admin.providers.decisionTest.stateFormat')"
        >
          <button
            v-for="format in stateFormats"
            :key="format.value"
            type="button"
            role="radio"
            :aria-checked="stateFormat === format.value"
            :disabled="disabled"
            :class="[
              'segmented-item px-2.5 py-1 text-xs',
              { 'segmented-item-active': stateFormat === format.value },
            ]"
            @click="stateFormat = format.value"
          >
            {{ format.label }}
          </button>
        </div>
      </div>
      <TextArea
        :id="stateFieldId"
        v-model="stateText"
        :hint="stateFormat === 'json' ? t('admin.providers.decisionTest.jsonHint') : undefined"
        :disabled="disabled"
        rows="4"
        data-testid="systemone-state"
      />
    </div>
    <RuleListEditor
      :items="questions"
      :title="t('admin.providers.decisionTest.questions')"
      :add-label="t('admin.providers.decisionTest.addQuestion')"
      :item-label="
        (index) => t('admin.providers.decisionTest.questionNumber', { number: index + 1 })
      "
      :disabled="disabled"
      :min="1"
      variant="line"
      test-id="systemone-questions"
      @add="addQuestion"
      @remove="questions.splice($event, 1)"
    >
      <template #row="{ item, index }">
        <div class="space-y-3">
          <Select
            v-model="item.type"
            :options="questionTypes"
            :disabled="disabled"
            :aria-label="t('admin.providers.decisionTest.questionType')"
            :data-testid="`decision-type-${index}`"
          />
          <TextArea
            v-model="item.instructions"
            :label="t('admin.providers.decisionTest.instructions')"
            :disabled="disabled"
            rows="2"
            :data-testid="`decision-instructions-${index}`"
          />
          <RuleListEditor
            v-if="item.type !== 'noul'"
            :items="item.options"
            :title="
              t(
                item.type === 'score'
                  ? 'admin.providers.decisionTest.levels'
                  : 'admin.providers.decisionTest.options',
              )
            "
            :hint="item.type === 'score' ? t('admin.providers.decisionTest.levelsHint') : undefined"
            :disabled="disabled"
            :min="item.type === 'score' ? 2 : 1"
            :max="item.type === 'score' ? 10 : 255"
            :reorderable="item.type === 'score'"
            :item-label="
              item.type === 'score'
                ? (level) => t('admin.providers.decisionTest.levelNumber', { number: level })
                : undefined
            "
            :test-id="`decision-options-${index}`"
            @add="item.options.push({ id: '', description: '' })"
            @remove="item.options.splice($event, 1)"
            @move="(from, to) => moveOption(item, from, to)"
          >
            <template #row="{ item: option, index: optionIndex }">
              <div class="space-y-2">
                <input
                  v-if="item.type === 'choice'"
                  v-model="option.id"
                  class="input"
                  :disabled="disabled"
                  :placeholder="t('admin.providers.decisionTest.optionId')"
                  :aria-label="t('admin.providers.decisionTest.optionId')"
                />
                <label
                  v-if="item.type === 'score'"
                  :for="`${stateFieldId}-option-${index}-${optionIndex}`"
                  class="sr-only"
                >
                  {{ t('admin.providers.decisionTest.levelNumber', { number: optionIndex }) }}
                </label>
                <TextArea
                  :id="`${stateFieldId}-option-${index}-${optionIndex}`"
                  v-model="option.description"
                  :label="
                    item.type === 'score'
                      ? undefined
                      : t('admin.providers.decisionTest.optionDescription')
                  "
                  :disabled="disabled"
                  rows="2"
                />
              </div>
            </template>
          </RuleListEditor>
          <details class="space-y-3">
            <summary class="cursor-pointer text-xs text-gray-500 dark:text-dark-400">
              {{
                t(
                  item.type === 'noul'
                    ? 'admin.providers.decisionTest.questionSettings'
                    : 'admin.providers.decisionTest.questionId',
                )
              }}
            </summary>
            <div class="space-y-1">
              <label class="input-label" :for="`decision-id-${index}`">{{
                t('admin.providers.decisionTest.questionId')
              }}</label>
              <input
                :id="`decision-id-${index}`"
                v-model="item.id"
                class="input"
                :disabled="disabled"
                :data-testid="`decision-id-${index}`"
              />
            </div>
            <template v-if="item.type === 'noul'">
              <TextArea
                v-model="item.trueDescription"
                :label="t('admin.providers.decisionTest.trueDescription')"
                :disabled="disabled"
                rows="2"
              />
              <TextArea
                v-model="item.falseDescription"
                :label="t('admin.providers.decisionTest.falseDescription')"
                :disabled="disabled"
                rows="2"
              />
            </template>
          </details>
        </div>
      </template>
    </RuleListEditor>
    <p v-if="validation.error" class="text-xs text-red-500" role="alert">
      {{ validation.error }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import Select from '@/components/common/Select.vue'
import TextArea from '@/components/common/TextArea.vue'
import { vSegmented } from '@/directives/segmented'
import type { SystemOneTestPayload } from './systemOneTest'

/**
 * SystemOneTestForm.vue edits state and typed questions for decision connection tests and emits
 * a validated request or null. Text and JSON state retain their submitted representation, while
 * question identifiers and option order determine how the response view can explain answers.
 *
 * Choice criteria use unique trimmed option identifiers; score criteria preserve the displayed
 * level order; noul criteria include nonempty true and false descriptions. Validation stops at
 * the first error and withholds the complete payload, so the parent disables single and batch
 * execution for an invalid draft. Adding a question searches unused generated identifiers to
 * keep a deleted question from causing a later generated collision.
 */

defineProps<{
  disabled: boolean // disable question form controls
}>()
/**
 * emit publishes validation results to the owning test dialog, where null disables execution
 * until the form becomes valid.
 */
const emit = defineEmits<{
  /**
   * Emits the complete valid payload or null whenever validation changes, including the initial
   * form state.
   */
  change: [payload: SystemOneTestPayload | null]
}>()
/**
 * Translation, field identity, and state-format controls initialize a localized text-state
 * draft that may be edited as JSON.
 */
const { t } = useI18n()
const stateFieldId = useId()
const stateFormat = ref('text')
const stateText = ref(t('admin.providers.decisionTest.defaultState'))
/**
 * State-format and question-type choices determine validation and the representation used when
 * building the emitted decision request payload.
 */
const stateFormats = computed(() => [
  { value: 'text', label: t('admin.providers.decisionTest.text') },
  { value: 'json', label: 'JSON' },
])
const questionTypes = computed(() =>
  ['noul', 'choice', 'score'].map((value) => ({
    value,
    label: t(`admin.providers.decisionTest.${value}`),
  })),
)
/**
 * QuestionDraft retains editable values for every question kind while the selected type
 * determines submitted criteria.
 */
type QuestionDraft = {
  id: string // editable question or option identifier
  type: 'noul' | 'choice' | 'score' // selected decision question category
  instructions: string // submitted question instruction text
  trueDescription: string // optional true outcome description
  falseDescription: string // optional false outcome description
  /**
   * Editable choice options and score levels share this array, and score submission preserves
   * its current order.
   */
  options: Array<{
    id: string // editable question or option identifier
    description: string // editable option explanation text
  }>
}
/**
 * questions owns editable drafts for the mounted form, starting with one localized noul
 * question and two optional choice or score entries.
 */
const questions = ref<QuestionDraft[]>([
  {
    id: 'available',
    type: 'noul',
    instructions: t('admin.providers.decisionTest.defaultQuestion'),
    trueDescription: '',
    falseDescription: '',
    options: [
      { id: 'a', description: '' },
      { id: 'b', description: '' },
    ],
  },
])

/**
 * addQuestion appends a new noul question with an unused generated identifier. Existing
 * identifiers are checked after deletions or manual edits, and new questions start with two
 * editable option drafts.
 */
function addQuestion() {
  let number = questions.value.length + 1
  while (questions.value.some((item) => item.id === `question_${number}`)) number++
  questions.value.push({
    id: `question_${number}`,
    type: 'noul',
    instructions: '',
    trueDescription: '',
    falseDescription: '',
    options: [
      { id: 'a', description: '' },
      { id: 'b', description: '' },
    ],
  })
}

/**
 * moveOption moves an option within the supplied question draft in place. Callers supply valid
 * positions; score requests use the resulting array order as the meaning of their levels.
 */
function moveOption(question: QuestionDraft, from: number, to: number) {
  const [option] = question.options.splice(from, 1)
  question.options.splice(to, 0, option)
}

/**
 * validation checks state parsing, unique question identifiers, instructions, and type-specific
 * criteria before exposing the complete request to the parent.
 */
const validation = computed<{
  payload: SystemOneTestPayload | null // validated request or null
  error: string // localized validation failure message
}>(() => {
  /**
   * invalid creates the empty payload and translated message for one validation failure.
   */
  const invalid = (key: string) => ({
    payload: null,
    error: t(`admin.providers.decisionTest.${key}`),
  })
  let state: string | object = stateText.value
  if (stateFormat.value === 'json') {
    try {
      state = JSON.parse(stateText.value)
    } catch {
      // Invalid JSON prevents publishing any payload so the parent cannot submit the textual
      // draft as structured state.
      return invalid('invalidState')
    }
    if (state === null || (typeof state !== 'string' && typeof state !== 'object'))
      // Invalid JSON prevents publishing any payload so the parent cannot submit the textual
      // draft as structured state.
      return invalid('invalidState')
  }
  const entries: Array<[string, SystemOneTestPayload['questions'][string]]> = []
  const ids = new Set<string>()
  for (const item of questions.value) {
    const id = item.id.trim()
    if (!id || ids.has(id)) return invalid('invalidIds')
    ids.add(id)
    if (!item.instructions.trim()) return invalid('missingInstructions')
    const question: SystemOneTestPayload['questions'][string] = {
      type: item.type,
      instructions: item.instructions,
    }
    if (item.type === 'choice') {
      const keys = item.options.map((option) => option.id.trim())
      if (keys.some((key) => !key) || new Set(keys).size !== keys.length)
        return invalid('invalidOptions')
      question.criteria = Object.fromEntries(
        item.options.map((option, index) => [keys[index], option.description]),
      )
    } else if (item.type === 'score') {
      // Score criteria require between two and ten ordered levels.
      if (item.options.length < 2 || item.options.length > 10) return invalid('invalidLevels')
      question.criteria = item.options.map((option) => option.description)
    } else {
      const criteria = Object.fromEntries(
        [
          ['true', item.trueDescription],
          ['false', item.falseDescription],
        ].filter(([, value]) => value.trim()),
      )
      if (Object.keys(criteria).length) question.criteria = criteria
    }
    entries.push([id, question])
  }
  return { payload: { state, questions: Object.fromEntries(entries) }, error: '' }
})
watch(validation, (result) => emit('change', result.payload), { immediate: true })
</script>
