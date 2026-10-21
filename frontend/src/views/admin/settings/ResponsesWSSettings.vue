<template>
  <SettingsCard
    :title="t('admin.settings.responsesWS.title')"
    :description="t('admin.settings.responsesWS.description')"
    data-testid="responses-ws-settings"
  >
    <template #actions>
      <button
        type="button"
        class="btn btn-secondary btn-sm h-9 shrink-0"
        :disabled="!canRestore"
        @click="restore"
      >
        <Icon name="undo" size="sm" />
        {{ t('admin.settings.responsesWS.restore') }}
      </button>
    </template>
    <SettingsNotice v-if="error" tone="error">
      <p>{{ error }}</p>
      <button type="button" class="font-medium underline underline-offset-2" @click="load">
        {{ t('admin.settings.responsesWS.retry') }}
      </button>
    </SettingsNotice>
    <template v-if="effective">
      <SettingsNotice v-if="resetAll" tone="warning">{{
        t('admin.settings.responsesWS.restoring')
      }}</SettingsNotice>
      <SettingsNotice v-else tone="info">{{
        t('admin.settings.responsesWS.applies')
      }}</SettingsNotice>
      <SettingsSection
        v-for="group in basicGroups"
        :key="group.key"
        :title="t(`admin.settings.responsesWS.groups.${group.key}.title`)"
        :hint="group.hint ? t(`admin.settings.responsesWS.groups.${group.key}.hint`) : undefined"
      >
        <div :class="responsesWsGridClass(group.fields.length)">
          <ResponsesWSNumberField
            v-for="field in group.fields"
            :key="field.key"
            v-bind="numberFieldProps(field)"
            @update:model-value="setNumber(field, $event)"
            @reset="setValue(field.key, null)"
          />
        </div>
      </SettingsSection>
      <section class="settings-section">
        <Disclosure
          summary-class="flex w-full flex-wrap items-baseline gap-x-2 gap-y-1 rounded-compact text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
        >
          <template #summary>
            <span class="text-sm font-semibold text-primary-900 dark:text-dark-50">{{
              t('admin.settings.responsesWS.advanced')
            }}</span>
            <span class="text-xs text-primary-900/80 dark:text-dark-300">{{
              t('admin.settings.responsesWS.advancedHint')
            }}</span>
          </template>
          <div class="space-y-6 pt-6">
            <SettingsSection
              v-for="group in advancedGroups"
              :key="group.key"
              :title="t(`admin.settings.responsesWS.groups.${group.key}.title`)"
            >
              <SettingToggleRow
                v-if="group.toggle"
                :id="`responses-ws-${group.toggle}`"
                :model-value="booleanValue(group.toggle)"
                :label="t(`admin.settings.responsesWS.fields.${group.toggle}`)"
                :hint="t(`admin.settings.responsesWS.hints.${group.toggle}`)"
                :disabled="saving"
                @update:model-value="setValue(group.toggle, $event)"
              />
              <Collapse :open="!group.dependent || !group.toggle || booleanValue(group.toggle)">
                <component :is="group.dependent ? SettingsSubpanel : 'div'">
                  <div :class="responsesWsGridClass(group.fields.length)">
                    <ResponsesWSNumberField
                      v-for="field in group.fields"
                      :key="field.key"
                      v-bind="numberFieldProps(field)"
                      @update:model-value="setNumber(field, $event)"
                      @reset="setValue(field.key, null)"
                    />
                  </div>
                </component>
              </Collapse>
            </SettingsSection>
          </div>
        </Disclosure>
      </section>
    </template>
  </SettingsCard>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getSettings,
  updateSettings,
  type ResponsesWSParameters,
  type ResponsesWSOverrides,
} from '@/api/admin/settings'
import { useSettingsSaveTarget } from '@/composables/useSettingsSaveRegistry'
import { useDirtyTracker } from '@/composables/useDirtyTracker'
import { extractApiErrorMessage } from '@/utils/apiError'
import Collapse from '@/components/common/Collapse.vue'
import Disclosure from '@/components/common/Disclosure.vue'
import Icon from '@/components/icons/Icon.vue'
import SettingsCard from '@/components/common/settings/SettingsCard.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import ResponsesWSNumberField from './ResponsesWSNumberField.vue'
import {
  responsesWsGridClass,
  responsesWsGroups,
  responsesWsKeys,
  type ResponsesWSBooleanKey,
  type ResponsesWSNumberField as NumberField,
} from './responsesWsFields'

/**
 * ResponsesWSSettings.vue edits runtime WebSocket overrides alongside deployment
 * defaults. The server returns saved overrides and effective values separately: blank
 * numeric inputs represent absent overrides, while their placeholders show the effective
 * value. A local patch keeps individual null removals distinct from untouched
 * properties, and the dirty snapshot compares the resulting override set.
 *
 * Restoring every default sends a null override object. Editing after that action
 * expands it into individual null entries before applying the new value, so other saved
 * overrides remain cleared. Successful writes reload effective values before marking the
 * editor clean; a failed read keeps the save target unsuccessful.
 */

// Translation lookup and stable group lists drive the labels and section order in the editor.
const { t } = useI18n()
const basicGroups = responsesWsGroups.filter((group) => !group.advanced)
const advancedGroups = responsesWsGroups.filter((group) => group.advanced)
// effective supplies server-computed placeholders; saved holds persisted overrides, and patch
// records local edits. A null patch entry removes an override when the save target submits.
const effective = ref<ResponsesWSParameters>()
const saved = ref<ResponsesWSOverrides>({})
const patch = ref<ResponsesWSOverrides>({})
// resetAll chooses whole-object removal; saving disables controls, while error displays load,
// validation, or runtime-application failures without discarding the administrator's editable patch.
const resetAll = ref(false)
const saving = ref(false)
const error = ref('')
// The dirty snapshot describes the resulting override set: sorting keys makes edit reversal
// compare consistently, and removed properties disappear from the proposed saved configuration.
const proposedOverrides = computed(() => {
  if (resetAll.value) return {}
  const next = { ...saved.value, ...patch.value }
  return Object.fromEntries(
    Object.entries(next)
      .filter(([, value]) => value != null)
      .sort(([left], [right]) => left.localeCompare(right)),
  )
})
// The save registry reads dirty while successful loads establish the baseline for subsequent edits.
const { dirty, markClean } = useDirtyTracker({ responsesWS: () => proposedOverrides.value })
// Restore is available after loading effective settings whenever the proposed configuration contains overrides
// and a save is not already in progress for this editor.
const canRestore = computed(
  () =>
    Boolean(effective.value) && !saving.value && Object.keys(proposedOverrides.value).length > 0,
)

/**
 * setValue records one override edit while honoring a pending restoration of defaults.
 * Callers pass API units, and null removes a saved field. When a whole-object reset
 * precedes an edit, the reset becomes individual null entries for every managed key
 * before that edit is added. This preserves the reset intent for other parameters even
 * though the outgoing request becomes an object patch.
 */
function setValue(key: keyof ResponsesWSParameters, value: number | boolean | null) {
  // A reset followed by one edit must still remove the other saved overrides;
  // enumerate null removals before replacing the edited key with its new value.
  if (resetAll.value) {
    patch.value = Object.fromEntries(responsesWsKeys.map((item) => [item, null]))
    resetAll.value = false
  }
  patch.value = { ...patch.value, [key]: value }
}
/**
 * setNumber converts a numeric input into an API override. An empty string removes the
 * override; other input is converted with the field scale supplied by responsesWsFields
 * before entering the patch.
 */
function setNumber(field: NumberField, text: string) {
  // The metadata scale bridges display units and API units, including MiB inputs whose persisted values are bytes.
  setValue(field.key, text === '' ? null : Number(text) * field.scale)
}
/**
 * overrideValue resolves one editable override from reset intent, local patch
 * membership, and saved values. A pending full reset returns null before considering
 * either object. Otherwise an own property in the patch takes precedence even when
 * its value is null, because that null removes a saved override. An untouched key
 * reads the saved object, and absent values become null so the number editor can
 * display its effective-value placeholder.
 */
function overrideValue(key: keyof ResponsesWSParameters) {
  if (resetAll.value) return null
  const value = Object.prototype.hasOwnProperty.call(patch.value, key)
    ? patch.value[key]
    : saved.value[key]
  return value ?? null
}
/**
 * numberFieldProps builds display values and state for one numeric editor. Stored
 * overrides and effective placeholders are divided by the same field scale; missing
 * overrides produce an empty input and hide its reset button.
 */
function numberFieldProps(field: NumberField) {
  const value = overrideValue(field.key)
  // Both the editable value and effective placeholder use display units; dividing by
  // the same metadata scale prevents byte-backed limits from displaying as raw byte counts.
  return {
    field,
    modelValue: value == null ? ('' as const) : Number(value) / field.scale,
    placeholder: String(Number(effective.value?.[field.key]) / field.scale),
    overridden: value != null,
    disabled: saving.value,
  }
}
/**
 * booleanValue resolves a toggle override before the server effective value. Null
 * overrides use the loaded effective setting, and Boolean converts a missing value to
 * false for the toggle component.
 */
function booleanValue(key: ResponsesWSBooleanKey) {
  return Boolean(overrideValue(key) ?? effective.value?.[key])
}
/**
 * restore stages removal of all overrides and clears individual edits. The save target
 * later submits null for the entire override object, while the dirty tracker compares
 * the proposed empty configuration.
 */
function restore() {
  resetAll.value = true
  patch.value = {}
}
/**
 * load replaces the editor state with saved overrides and effective server parameters.
 * Call it for initial setup, discard, or post-save refresh. Success clears the patch and
 * marks its snapshot clean; failure displays the translated load error and leaves the
 * existing draft available.
 */
async function load() {
  try {
    const value = await getSettings()
    effective.value = value.responses_ws_effective
    saved.value = value.responses_ws ?? {}
    patch.value = {}
    resetAll.value = false
    error.value = ''
    markClean('responsesWS')
  } catch {
    error.value = t('admin.settings.responsesWS.loadError')
  }
}
/**
 * save writes either the pending full reset or the field patch, then reloads server
 * values. The returned boolean is consumed by the page save registry, so a successful
 * write followed by a failed refresh still returns false. A write failure retains the
 * patch, including when persistence succeeded but applying runtime settings failed.
 * Controls remain disabled until the update and follow-up read finish.
 */
async function save() {
  saving.value = true
  error.value = ''
  try {
    // Read back effective values after persistence because deployment defaults and runtime application
    // determine placeholders; marking the patch clean before that read would conceal refresh failure.
    await updateSettings({ responses_ws: resetAll.value ? null : patch.value })
    await load()
    return !error.value
  } catch (cause) {
    error.value = extractApiErrorMessage(cause, t('admin.settings.responsesWS.saveError'), {
      SETTINGS_APPLY_FAILED: t('admin.settings.responsesWS.applyError'),
    })
    return false
  } finally {
    saving.value = false
  }
}
// Registration runs during setup so this target retains its page-wide save order even while
// its category is hidden; mounting starts the initial read after registration completes.
useSettingsSaveTarget('responses-ws', { dirty, save, discard: load })
onMounted(load)
</script>
