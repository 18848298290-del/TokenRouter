<template>
  <div>
    <SettingRow
      id="announcement-targeting"
      :label="t('admin.announcements.form.targetingMode')"
      :hint="
        mode === 'all'
          ? t('admin.announcements.form.targetingAllHint')
          : t('admin.announcements.form.targetingCustomHint')
      "
    >
      <SettingsSegmented
        v-model="mode"
        :options="modeOptions"
        :ariaLabel="t('admin.announcements.form.targetingMode')"
      />
    </SettingRow>

    <!-- Collapse retains the outgoing condition rows during its closing transition. -->
    <Collapse :open="mode === 'custom'" unmount-on-hide>
      <div class="pt-4">
        <RuleListEditor
          :items="anyOf"
          :item-key="targetingRowKey"
          :add-label="t('admin.announcements.form.addOrGroup')"
          add-placement="footer"
          :max="50"
          :error="validationError"
          variant="card"
          :item-label="
            (index) => t('admin.announcements.form.conditionGroupItem', { index: index + 1 })
          "
          test-id="announcement-groups"
          @add="addOrGroup"
          @remove="removeOrGroup"
        >
          <template #row="{ item: group, index: groupIndex }">
            <RuleListEditor
              :items="group.all_of || []"
              :item-key="targetingRowKey"
              :add-label="t('admin.announcements.form.addAndCondition')"
              add-placement="footer"
              :max="50"
              :test-id="`announcement-conditions-${groupIndex}`"
              @add="addAndCondition(groupIndex)"
              @remove="removeAndCondition(groupIndex, $event)"
            >
              <!-- Each row places the condition type before its plan choices or balance threshold. -->
              <template #row="{ item: cond, index: condIndex }">
                <div class="flex flex-col gap-2 sm:flex-row sm:items-start">
                  <div class="w-full shrink-0 sm:w-36">
                    <Select
                      :model-value="cond.type"
                      :options="conditionTypeOptions"
                      :aria-label="t('admin.announcements.form.conditionType')"
                      @update:model-value="(v) => setConditionType(groupIndex, condIndex, v as any)"
                    />
                  </div>
                  <div
                    v-if="cond.type === 'subscription'"
                    class="flex min-w-0 flex-1 flex-wrap items-center gap-2 sm:min-h-9"
                    role="group"
                    :aria-label="t('admin.announcements.form.selectPackages')"
                  >
                    <button
                      v-for="plan in plans"
                      :key="plan.id"
                      type="button"
                      :aria-pressed="isPlanSelected(groupIndex, condIndex, plan.id)"
                      :class="[
                        'plan-chip',
                        isPlanSelected(groupIndex, condIndex, plan.id) && 'plan-chip-active',
                      ]"
                      @click="
                        togglePlanSelection(
                          groupIndex,
                          condIndex,
                          plan.id,
                          !isPlanSelected(groupIndex, condIndex, plan.id),
                        )
                      "
                    >
                      <Icon
                        v-if="isPlanSelected(groupIndex, condIndex, plan.id)"
                        name="check"
                        size="xs"
                        :stroke-width="2.5"
                        :animate-on-hover="false"
                      />
                      <span>{{ plan.name }}</span>
                      <span class="plan-chip-meta"
                        >{{ plan.validity_days }}{{ t('payment.days') }}</span
                      >
                    </button>
                    <span
                      v-if="plans.length === 0"
                      class="text-sm text-gray-500 dark:text-dark-400"
                    >
                      {{ t('admin.announcements.form.noPlans') }}
                    </span>
                  </div>
                  <div v-else class="flex min-w-0 flex-1 gap-2">
                    <div class="w-24 shrink-0">
                      <Select
                        :model-value="cond.operator"
                        :options="balanceOperatorOptions"
                        :aria-label="t('admin.announcements.form.operator')"
                        @update:model-value="(v) => setOperator(groupIndex, condIndex, v as any)"
                      />
                    </div>
                    <input
                      :value="String(cond.value ?? '')"
                      type="number"
                      step="any"
                      class="input min-w-0 flex-1"
                      :placeholder="t('admin.announcements.form.balanceValue')"
                      :aria-label="t('admin.announcements.form.balanceValue')"
                      @input="
                        (e) =>
                          setBalanceValue(
                            groupIndex,
                            condIndex,
                            (e.target as HTMLInputElement).value,
                          )
                      "
                    />
                  </div>
                </div>
              </template>
            </RuleListEditor>
          </template>
        </RuleListEditor>
      </div>
    </Collapse>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, toRaw, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type {
  AnnouncementTargeting,
  AnnouncementCondition,
  AnnouncementConditionGroup,
  AnnouncementConditionType,
  AnnouncementOperator,
} from '@/api/announcements.types'
import type { SubscriptionPlan } from '@/api/payment.types'

import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import Collapse from '@/components/common/Collapse.vue'
import SettingRow from '@/components/common/settings/SettingRow.vue'
import SettingsSegmented, {
  type SettingsSegmentedOption,
} from '@/components/common/settings/SettingsSegmented.vue'
import { createStableObjectKeyResolver } from '@/utils/stableObjectKey'

// AnnouncementTargetingEditor.vue edits announcement eligibility as OR groups
// containing AND conditions. An empty group collection selects all users. Subscription
// conditions choose plan IDs, while balance conditions store an operator and numeric
// threshold. Updates clone the payload and emit a replacement so the parent controls
// persistence.
//
// Cloning also carries row identities through a WeakMap, keeping focus attached to
// surviving inputs without adding presentation keys to submitted data. A separate
// reactive selection map mirrors subscription choices by group and condition index.
// Watchers synchronize those choices with the payload through a deferred comparison;
// the map and pending timer remain component-local resources.

// The translation service supplies targeting labels and validation messages for mode,
// condition, plan, and comparison controls.
const { t } = useI18n()

// props supplies the parent-owned targeting payload and available subscription plans
// used by the condition editor and plan chips.
const props = defineProps<{
  // modelValue belongs to the parent; this editor emits replacements when local edits
  // need to update that parent form.
  modelValue: AnnouncementTargeting
  plans: SubscriptionPlan[] // Available subscription plans.
}>()

// emit publishes replacement targeting payloads for the parent to retain and eventually
// submit with the announcement form.
const emit = defineEmits<{
  (e: 'update:modelValue', value: AnnouncementTargeting): void // Publishes the edited parent model.
}>()

// anyOf exposes the current OR groups and substitutes an empty collection when the
// model omits them.
const anyOf = computed(() => props.modelValue?.any_of ?? [])

// Mode distinguishes unrestricted targeting from an editable collection of eligibility
// conditions.
type Mode = 'all' | 'custom'

// mode derives unrestricted targeting from an empty any_of collection and routes writes
// through setMode to emit the corresponding payload.
const mode = computed<Mode>({
  get: () => (anyOf.value.length === 0 ? 'all' : 'custom'), // Reads the derived mode.
  set: setMode, // Applies requested mode changes.
})

// modeOptions supplies the translated unrestricted and custom choices used by the
// segmented targeting control in the editor.
const modeOptions = computed<SettingsSegmentedOption<Mode>[]>(() => [
  {
    value: 'all', // Stored option value.
    label: t('admin.announcements.form.targetingAll'), // Displayed option label.
    icon: 'users', // Displayed control icon.
  },
  {
    value: 'custom', // Stored option value.
    label: t('admin.announcements.form.targetingCustom'), // Displayed option label.
    icon: 'filter', // Displayed control icon.
  },
])

// conditionTypeOptions offers subscription membership and balance comparison as the two
// condition editors available within each AND group.
const conditionTypeOptions = computed(() => [
  {
    value: 'subscription', // Stored option value.
    label: t('admin.announcements.form.conditionSubscription'), // Displayed option label.
  },
  {
    value: 'balance', // Stored option value.
    label: t('admin.announcements.form.conditionBalance'), // Displayed option label.
  },
])

// balanceOperatorOptions supplies the server comparison operators displayed beside
// numeric balance thresholds in the balance condition editor.
const balanceOperatorOptions = computed(() => [
  {
    value: 'gt', // Stored option value.
    label: t('admin.announcements.operators.gt'), // Displayed option label.
  },
  {
    value: 'gte', // Stored option value.
    label: t('admin.announcements.operators.gte'), // Displayed option label.
  },
  {
    value: 'lt', // Stored option value.
    label: t('admin.announcements.operators.lt'), // Displayed option label.
  },
  {
    value: 'lte', // Stored option value.
    label: t('admin.announcements.operators.lte'), // Displayed option label.
  },
  {
    value: 'eq', // Stored option value.
    label: t('admin.announcements.operators.eq'), // Displayed option label.
  },
])

// setMode emits an empty OR collection for unrestricted targeting. Entering custom mode
// creates one subscription condition only when no group exists, retaining an existing
// custom payload otherwise.
function setMode(next: Mode) {
  if (next === 'all') {
    emit('update:modelValue', {
      any_of: [], // Alternative eligibility groups.
    })
    return
  }
  if (anyOf.value.length === 0) {
    emit('update:modelValue', {
      any_of: [
        {
          all_of: [defaultSubscriptionCondition()], // Jointly required conditions.
        },
      ], // Alternative eligibility groups.
    })
  }
}

// defaultSubscriptionCondition creates an unselected subscription membership condition
// for a new row.
function defaultSubscriptionCondition(): AnnouncementCondition {
  return {
    type: 'subscription' as AnnouncementConditionType, // Eligibility condition kind.
    operator: 'in' as AnnouncementOperator, // Eligibility comparison operator.
    plan_ids: [], // Eligible subscription identifiers.
  }
}

// defaultBalanceCondition creates a greater-than-or-equal balance condition with zero
// threshold.
function defaultBalanceCondition(): AnnouncementCondition {
  return {
    type: 'balance' as AnnouncementConditionType, // Eligibility condition kind.
    operator: 'gte' as AnnouncementOperator, // Eligibility comparison operator.
    value: 0, // Initial balance threshold.
  }
}

// TargetingDraft contains the mutable cloned OR groups passed to edit callbacks.
type TargetingDraft = {
  any_of: AnnouncementConditionGroup[] // Alternative eligibility groups.
}

// inheritedRowKeys carries presentation identities across JSON clones while keeping
// those keys outside the announcement submission payload.
const inheritedRowKeys = new WeakMap<object, string>()
// newRowKey assigns stable presentation identities to previously unseen targeting
// objects before later clones inherit the same identity.
const newRowKey = createStableObjectKeyResolver<object>('announcement-targeting')

// targetingRowKey resolves the raw object's inherited or newly assigned presentation
// key. Unwrapping Vue proxies lets the WeakMap recognize cloned objects regardless of
// the reactive wrapper used by the template.
function targetingRowKey(row: object): string {
  const raw = toRaw(row)
  return inheritedRowKeys.get(raw) ?? newRowKey(raw)
}

// cloneTargeting creates a JSON copy of the current targeting payload with an any_of
// array available for mutation. The returned groups and conditions receive the
// presentation keys of their corresponding source objects.
//
// Identity transfer happens before edits splice or replace entries, so surviving rows
// keep their input elements when indices move. Keys stay in the WeakMap rather than the
// cloned payload, allowing callers to emit the draft directly as announcement targeting
// data.
function cloneTargeting(): TargetingDraft {
  const current = props.modelValue ?? {
    any_of: [], // Alternative eligibility groups.
  }
  // Copy serializable targeting fields before editing, then transfer presentation
  // identities separately so emitted payloads remain suitable for the announcement
  // endpoint.
  const draft: TargetingDraft = JSON.parse(JSON.stringify(current))
  draft.any_of ??= []
  draft.any_of.forEach((group, groupIndex) => {
    const previous = current.any_of?.[groupIndex]
    if (!previous) return
    inheritedRowKeys.set(group, targetingRowKey(previous))
    group.all_of?.forEach((condition, condIndex) => {
      const previousCondition = previous.all_of?.[condIndex]
      if (previousCondition) inheritedRowKeys.set(condition, targetingRowKey(previousCondition))
    })
  })
  return draft
}

// updateTargeting gives the callback a cloned targeting draft and emits it afterward.
// Callbacks mutate that draft synchronously so the emitted replacement includes the
// complete edit and inherited row identities.
function updateTargeting(mutator: (draft: TargetingDraft) => void) {
  const draft = cloneTargeting()
  mutator(draft)
  emit('update:modelValue', draft)
}

// addOrGroup appends a group containing one unselected subscription condition. It stops
// at fifty groups, matching the editor's add-button limit while retaining the existing
// payload when the limit is reached.
function addOrGroup() {
  updateTargeting((draft) => {
    // Match the OR editor's maximum allowed row count.
    if (draft.any_of.length >= 50) return
    draft.any_of.push({
      all_of: [defaultSubscriptionCondition()], // Jointly required conditions.
    })
  })
}

// removeOrGroup removes the indexed OR group from a cloned targeting payload. Callers
// supply the row index from the current editor so splice removes the intended
// eligibility group.
function removeOrGroup(groupIndex: number) {
  updateTargeting((draft) => {
    draft.any_of.splice(groupIndex, 1)
  })
}

// addAndCondition appends a subscription condition to the indexed existing group. It
// initializes missing all_of storage and stops at fifty conditions, matching the nested
// editor's add-button limit.
function addAndCondition(groupIndex: number) {
  updateTargeting((draft) => {
    const group = draft.any_of[groupIndex]
    if (!group.all_of) group.all_of = []
    // Match the AND editor's maximum allowed row count.
    if (group.all_of.length >= 50) return
    group.all_of.push(defaultSubscriptionCondition())
  })
}

// removeAndCondition removes an indexed condition when its group has all_of storage.
// Empty groups remain in the payload so validation can display the missing-condition
// message beside the editor.
function removeAndCondition(groupIndex: number, condIndex: number) {
  updateTargeting((draft) => {
    const group = draft.any_of[groupIndex]
    if (!group?.all_of) return
    group.all_of.splice(condIndex, 1)
  })
}

// setConditionType replaces an existing group's condition with the selected type's
// defaults. Changing type discards that condition's previous operator and values; a
// missing all_of collection leaves the draft unchanged.
function setConditionType(
  groupIndex: number,
  condIndex: number,
  nextType: AnnouncementConditionType,
) {
  updateTargeting((draft) => {
    const group = draft.any_of[groupIndex]
    if (!group?.all_of) return

    if (nextType === 'subscription') {
      group.all_of[condIndex] = defaultSubscriptionCondition()
    } else {
      group.all_of[condIndex] = defaultBalanceCondition()
    }
  })
}

// setOperator updates the indexed condition's comparison operator in a cloned payload.
// Missing groups, missing all_of collections, and missing conditions leave the
// replacement draft unchanged for the parent.
function setOperator(groupIndex: number, condIndex: number, op: AnnouncementOperator) {
  updateTargeting((draft) => {
    const group = draft.any_of[groupIndex]
    if (!group?.all_of) return

    const cond = group.all_of[condIndex]
    if (!cond) return

    cond.operator = op
  })
}

// setBalanceValue converts the input text into a finite balance threshold. Empty text
// and nonfinite numbers become zero, while a missing target condition leaves the cloned
// targeting payload unchanged.
function setBalanceValue(groupIndex: number, condIndex: number, raw: string) {
  // The number input emits text while targeting stores numbers; empty or nonfinite
  // input becomes zero so the draft remains serializable.
  const n = raw === '' ? 0 : Number(raw)
  updateTargeting((draft) => {
    const group = draft.any_of[groupIndex]
    if (!group?.all_of) return

    const cond = group.all_of[condIndex]
    if (!cond) return

    cond.value = Number.isFinite(n) ? n : 0
  })
}

// subscriptionSelections stores plan ID arrays by group and condition index, with
// watchers copying changes between this map and the targeting payload.
const subscriptionSelections = reactive<Record<number, Record<number, number[]>>>({})

// ensureSelectionPath initializes the group map and condition arrays required for local
// plan selections.
function ensureSelectionPath(groupIndex: number, condIndex: number) {
  if (!subscriptionSelections[groupIndex]) subscriptionSelections[groupIndex] = {}
  if (!subscriptionSelections[groupIndex][condIndex])
    subscriptionSelections[groupIndex][condIndex] = []
}

// isPlanSelected reports whether the indexed subscription selection contains the plan.
// Missing group or condition entries produce false, allowing plan chips to render
// before their local selection path exists.
function isPlanSelected(groupIndex: number, condIndex: number, planID: number): boolean {
  return subscriptionSelections[groupIndex]?.[condIndex]?.includes(planID) ?? false
}

// togglePlanSelection adds or removes the plan ID in the local selection map. The
// selection watcher emits the corresponding targeting payload later; callers pass the
// intended checked state from the chip.
function togglePlanSelection(
  groupIndex: number,
  condIndex: number,
  planID: number,
  checked: boolean,
) {
  ensureSelectionPath(groupIndex, condIndex)
  const current = subscriptionSelections[groupIndex][condIndex] ?? []
  subscriptionSelections[groupIndex][condIndex] = checked
    ? [...current, planID]
    : current.filter((id) => id !== planID)
}

// Copy model plan choices into index-addressed local arrays before chip edits,
// comparing sorted IDs to suppress an unnecessary replacement of equivalent selections.
watch(
  () => props.modelValue,
  (v) => {
    const groups = v?.any_of ?? []
    for (let gi = 0; gi < groups.length; gi++) {
      const allOf = groups[gi]?.all_of ?? []
      for (let ci = 0; ci < allOf.length; ci++) {
        const c = allOf[ci]
        if (c?.type === 'subscription') {
          ensureSelectionPath(gi, ci)
          const newIds = (c.plan_ids ?? []).slice()
          const currentIds = subscriptionSelections[gi]?.[ci] ?? []
          if (JSON.stringify(newIds.sort()) !== JSON.stringify(currentIds.sort())) {
            subscriptionSelections[gi][ci] = newIds
          }
        }
      }
    }
  },
  {
    immediate: true, // Runs on watcher registration.
  },
)

// syncTimeout holds the deferred selection-to-payload update, which subsequent
// selection changes replace before comparing and emitting another targeting draft.
let syncTimeout: ReturnType<typeof setTimeout> | null = null
// Local plan changes schedule a deferred payload flush; the complete-payload comparison
// prevents an equivalent emitted model from restarting the same synchronization cycle.
watch(
  () => subscriptionSelections,
  () => {
    // Replace the pending selection flush so consecutive reactive changes share one
    // payload comparison and emit the latest local plan choices.
    if (syncTimeout) clearTimeout(syncTimeout)

    syncTimeout = setTimeout(() => {
      // The deferred flush clones the current model before writing plan choices,
      // retaining other condition edits made since the timer was scheduled.
      const newTargeting = cloneTargeting()

      const groups = newTargeting.any_of ?? []
      for (let gi = 0; gi < groups.length; gi++) {
        const allOf = groups[gi]?.all_of ?? []
        for (let ci = 0; ci < allOf.length; ci++) {
          const c = allOf[ci]
          if (c?.type === 'subscription') {
            ensureSelectionPath(gi, ci)
            c.operator = 'in' as AnnouncementOperator
            c.plan_ids = (subscriptionSelections[gi]?.[ci] ?? []).slice()
          }
        }
      }

      // Compare complete serialized payloads before emitting to stop a model watcher
      // update from producing another identical parent update.
      if (JSON.stringify(props.modelValue) !== JSON.stringify(newTargeting)) {
        emit('update:modelValue', newTargeting)
      }
    }, 0)
  },
  {
    deep: true, // Observes nested reactive changes.
  },
)

// validationError derives the first custom-targeting problem, checking group sizes and
// required subscription plans while unrestricted mode displays no validation message.
// Empty groups need an AND condition, subscription conditions need at least one plan,
// and both nesting levels enforce fifty entries. The first failing group determines the
// displayed message, while balance thresholds are handled by the numeric editor rather
// than this computed validation.
const validationError = computed(() => {
  if (mode.value !== 'custom') return ''

  const groups = anyOf.value
  if (groups.length === 0) return t('admin.announcements.form.addOrGroup')

  // Reject oversized OR collections supplied by external models.
  if (groups.length > 50) return 'any_of > 50'

  for (const g of groups) {
    const allOf = g?.all_of ?? []
    if (allOf.length === 0) return t('admin.announcements.form.addAndCondition')
    // Reject oversized AND collections supplied by external models.
    if (allOf.length > 50) return 'all_of > 50'

    for (const c of allOf) {
      if (c.type === 'subscription') {
        if (!c.plan_ids || c.plan_ids.length === 0)
          return t('admin.announcements.form.packagesRequired')
      }
    }
  }

  return ''
})
</script>

<style scoped>
/* Plan chips support multiple selections, with a tinted background and brand-colored border marking selected plans. */
.plan-chip {
  @apply inline-flex h-8 items-center gap-1.5 rounded-full border px-3 text-sm;
  @apply border-primary-900/10 bg-white text-gray-700 hover:border-black/20;
  @apply dark:border-dark-600 dark:bg-dark-950 dark:text-dark-100 dark:hover:border-dark-500;
  @apply transition-colors duration-fast;
}

.plan-chip-meta {
  @apply text-xs text-gray-400 dark:text-dark-400;
}

.plan-chip.plan-chip-active {
  @apply border-primary-500 bg-primary-500/8 text-primary-700;
  @apply dark:border-primary-500 dark:bg-primary-500/15 dark:text-primary-400;
}

.plan-chip-active .plan-chip-meta {
  @apply text-primary-600/70 dark:text-primary-400/70;
}
</style>
