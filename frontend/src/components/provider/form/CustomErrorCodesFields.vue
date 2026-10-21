<template>
  <SettingsSection>
    <SettingToggleRow
      v-if="!hideToggle"
      :id="`${uid}-enabled`"
      v-model="enabled"
      :label="t('admin.providers.customErrorCodes')"
      :hint="t('admin.providers.customErrorCodesHint')"
      testid="custom-error-codes-toggle"
    />
    <Collapse :open="hideToggle || enabled" unmount-on-hide>
      <SettingsSubpanel>
        <SettingsNotice tone="warning">{{
          t('admin.providers.customErrorCodesWarning')
        }}</SettingsNotice>

        <!-- Preset HTTP error-code toggles. -->
        <div class="flex flex-wrap gap-2">
          <button
            v-for="code in commonErrorCodes"
            :key="code.value"
            type="button"
            :aria-pressed="codes.includes(code.value)"
            :class="[
              'rounded-control px-3 py-1.5 text-sm font-medium transition-colors',
              codes.includes(code.value)
                ? 'bg-red-100 text-red-700 ring-1 ring-red-500 dark:bg-red-900/30 dark:text-red-400'
                : 'bg-white text-gray-600 ring-1 ring-gray-200 hover:bg-gray-100 dark:bg-dark-700 dark:text-gray-400 dark:ring-dark-600 dark:hover:bg-dark-600',
            ]"
            @click="toggleCode(code.value)"
          >
            {{ code.value }} {{ code.label }}
          </button>
        </div>

        <!-- Manual HTTP error-code entry. -->
        <div class="flex items-center gap-2">
          <input
            v-model.number="draftCode"
            type="number"
            min="100"
            max="599"
            class="input flex-1"
            :aria-label="t('admin.providers.enterErrorCode')"
            :placeholder="t('admin.providers.enterErrorCode')"
            @keydown.enter.prevent="addDraftCode"
          />
          <button
            type="button"
            class="btn btn-secondary btn-icon shrink-0 px-0"
            :aria-label="t('common.add')"
            @click="addDraftCode"
          >
            <Icon name="plus" size="sm" />
          </button>
        </div>

        <!-- Selected HTTP error-code summary. -->
        <div class="flex flex-wrap gap-2">
          <span
            v-for="code in sortedCodes"
            :key="code"
            class="inline-flex items-center gap-1 rounded-full bg-red-100 px-2.5 py-0.5 text-sm font-medium text-red-700 dark:bg-red-900/30 dark:text-red-400"
          >
            {{ code }}
            <button
              type="button"
              class="hover:text-red-900 dark:hover:text-red-300"
              :aria-label="`${t('common.delete')} ${code}`"
              @click="removeCode(code)"
            >
              <Icon name="x" size="sm" :stroke-width="2" />
            </button>
          </span>
          <span v-if="codes.length === 0" class="input-hint mt-0">
            {{ t('admin.providers.noneSelectedUsesDefault') }}
          </span>
        </div>
      </SettingsSubpanel>
    </Collapse>
  </SettingsSection>
</template>

<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Collapse from '@/components/common/Collapse.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import Icon from '@/components/icons/Icon.vue'
import { commonErrorCodes } from '@/composables/useModelWhitelist'
import { useAppStore } from '@/stores/app'

// CustomErrorCodesFields.vue edits provider-specific error status codes through presets
// and manual input. The parent owns the enabled flag and code array, and adding
// rate-limit or overload codes requests browser confirmation before changing how those
// responses are classified.
defineProps<{
  // The enclosing bulk application field supplies enablement when the local toggle is
  // hidden.
  hideToggle?: boolean
}>()
// enabled is the parent-owned feature switch whose value controls the visibility and
// application of these settings.
const enabled = defineModel<boolean>('enabled', { default: false })
// codes binds the parent-owned selected HTTP error code array to the shared controls
// through its model update event.
const codes = defineModel<number[]>('codes', { required: true })

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// appStore displays operation feedback through the shared notification area used by the
// provider administration screens.
const appStore = useAppStore()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
// draftCode holds a manual status-code input until validation and confirmation append it
// to the selected codes.
const draftCode = ref<number | null>(null)
// sortedCodes gives the display an ascending copy while retaining the original order of
// the parent-owned code array.
const sortedCodes = computed(() => [...codes.value].sort((a, b) => a - b))

// confirmRiskyCode requests confirmation before adding rate-limit or overload status
// codes. Other codes are accepted immediately, and a rejected browser confirmation leaves
// the selected code array unchanged for the caller.
function confirmRiskyCode(code: number) {
  if (code === 429) return confirm(t('admin.providers.customErrorCodes429Warning'))
  if (code === 529) return confirm(t('admin.providers.customErrorCodes529Warning'))
  return true
}

// toggleCode removes an existing code or appends a newly confirmed code. Confirmation
// applies only to additions, allowing administrators to remove risky classifications
// directly from the preset controls.
function toggleCode(code: number) {
  if (codes.value.includes(code)) {
    removeCode(code)
    return
  }
  if (!confirmRiskyCode(code)) return
  codes.value = [...codes.value, code]
}

// addDraftCode validates the manual numeric draft and appends it after any required
// confirmation. Out-of-range and duplicate values show feedback, while a successful
// addition clears the draft input.
function addDraftCode() {
  const code = draftCode.value
  // Manual input accepts the HTTP status range from one hundred through five hundred
  // ninety-nine; values outside it show validation feedback.
  if (code === null || typeof code !== 'number' || code < 100 || code > 599) {
    appStore.showError(t('admin.providers.invalidErrorCode'))
    return
  }
  if (codes.value.includes(code)) {
    appStore.showInfo(t('admin.providers.errorCodeExists'))
    return
  }
  if (!confirmRiskyCode(code)) return
  codes.value = [...codes.value, code]
  draftCode.value = null
}

// removeCode removes all occurrences of a code from the selected array.
function removeCode(code: number) {
  codes.value = codes.value.filter((item) => item !== code)
}
</script>
