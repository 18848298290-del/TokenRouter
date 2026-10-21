<template>
  <SettingsCard
    :title="t('admin.settings.preAggregation.title')"
    :description="t('admin.settings.preAggregation.description')"
  >
    <template #actions>
      <button
        type="button"
        class="btn btn-secondary btn-icon shrink-0"
        :disabled="loading"
        :title="t('common.refresh')"
        :aria-label="t('common.refresh')"
        @click="loadSettings"
      >
        <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
      </button>
    </template>

    <ContentSkeleton v-if="loading && !state" variant="form" :rows="4" />

    <template v-else-if="state">
      <SettingsSection>
        <SettingToggleRow
          :id="`${uid}-usage-enabled`"
          v-model="form.usage.enabled"
          :label="t('admin.settings.preAggregation.usage')"
          :disabled="!state.availability.usage_available || saving"
        >
          <template v-if="!state.availability.usage_available" #hint>
            <SettingsNotice tone="warning" class="mt-2">
              {{ t('admin.settings.preAggregation.unavailable') }}
            </SettingsNotice>
          </template>
        </SettingToggleRow>
        <SettingRow
          :id="`${uid}-usage-interval`"
          field
          :label-for="`${uid}-usage-interval`"
          :label="t('admin.settings.preAggregation.interval')"
        >
          <input
            :id="`${uid}-usage-interval`"
            v-model.number="form.usage.interval_seconds"
            type="number"
            min="30"
            max="3600"
            step="30"
            class="input"
            :disabled="saving"
          />
        </SettingRow>
        <dl class="grid grid-cols-2 gap-x-6 gap-y-4 text-sm xl:grid-cols-4">
          <StatusItem :label="t('admin.settings.preAggregation.phase')">
            <span :class="phaseClass(state.usage_status.phase)">{{
              phaseLabel(state.usage_status.phase)
            }}</span>
          </StatusItem>
          <StatusItem
            :label="t('admin.settings.preAggregation.coverage')"
            class="col-span-2 xl:col-span-1"
          >
            {{ usageCoverage }}
          </StatusItem>
          <StatusItem :label="t('admin.settings.preAggregation.lastSuccess')">
            {{ formatDate(state.usage_status.last_success_at) }}
          </StatusItem>
          <!--
            Lag arrives in seconds while the shared duration formatter accepts
            milliseconds, so convert here before choosing the display unit.
          -->
          <StatusItem :label="t('admin.settings.preAggregation.lag')">
            {{ formatDuration(state.usage_status.lag_seconds * 1000) }}
          </StatusItem>
          <StatusItem :label="t('admin.settings.preAggregation.lastDuration')">
            {{ formatDuration(state.usage_status.last_duration_ms) }}
          </StatusItem>
          <StatusItem
            v-if="state.usage_status.last_error"
            :label="t('admin.settings.preAggregation.lastError')"
            class="col-span-2 text-red-600 dark:text-red-400 xl:col-span-3"
          >
            {{ state.usage_status.last_error }}
          </StatusItem>
        </dl>
      </SettingsSection>

      <SettingsSection>
        <SettingToggleRow
          :id="`${uid}-ops-enabled`"
          v-model="form.ops.enabled"
          :label="t('admin.settings.preAggregation.ops')"
          :disabled="!state.availability.ops_available || saving"
        >
          <template v-if="!state.availability.ops_available" #hint>
            <SettingsNotice tone="warning" class="mt-2">
              {{ t('admin.settings.preAggregation.unavailable') }}
            </SettingsNotice>
          </template>
        </SettingToggleRow>
        <dl class="grid grid-cols-2 gap-x-6 gap-y-4 text-sm xl:grid-cols-4">
          <StatusItem :label="t('admin.settings.preAggregation.phase')">
            <span :class="phaseClass(state.ops_status.phase)">{{
              phaseLabel(state.ops_status.phase)
            }}</span>
          </StatusItem>
          <StatusItem :label="t('admin.settings.preAggregation.lastSuccess')">
            {{ formatDate(state.ops_status.last_success_at) }}
          </StatusItem>
          <StatusItem :label="t('admin.settings.preAggregation.lastDuration')">
            {{ formatDuration(state.ops_status.last_duration_ms) }}
          </StatusItem>
          <StatusItem
            v-if="state.ops_status.last_error"
            :label="t('admin.settings.preAggregation.lastError')"
            class="col-span-2 text-red-600 dark:text-red-400 xl:col-span-1"
          >
            {{ state.ops_status.last_error }}
          </StatusItem>
        </dl>
      </SettingsSection>

      <SettingsSection>
        <SettingRow
          :id="`${uid}-backfill-days`"
          field
          :label-for="`${uid}-backfill-days`"
          :label="t('admin.settings.preAggregation.backfillDays')"
        >
          <div class="flex gap-2">
            <input
              :id="`${uid}-backfill-days`"
              v-model.number="backfillDays"
              type="number"
              min="1"
              :max="state.availability.manual_backfill_max_days"
              class="input"
              :disabled="backfilling || !canBackfill"
            />
            <button
              type="button"
              class="btn btn-secondary shrink-0"
              :disabled="backfilling || !canBackfill"
              @click="startBackfill"
            >
              <Icon
                :name="backfilling ? 'refresh' : 'play'"
                size="sm"
                :class="backfilling ? 'animate-spin' : ''"
              />
              {{ t('admin.settings.preAggregation.startBackfill') }}
            </button>
          </div>
        </SettingRow>
      </SettingsSection>
    </template>
  </SettingsCard>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { computed, defineComponent, h, nextTick, onMounted, reactive, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import type { PreAggregationSettingsResponse } from '@/api/admin/settings'
import SettingRow from '@/components/common/settings/SettingRow.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import SettingsCard from '@/components/common/settings/SettingsCard.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import Icon from '@/components/icons/Icon.vue'
import { useDirtyTracker } from '@/composables/useDirtyTracker'
import { useSettingsSaveTarget } from '@/composables/useSettingsSaveRegistry'
import { useAppStore } from '@/stores'
import { extractApiErrorMessage } from '@/utils/apiError'

/**
 * PreAggregationSettings.vue edits Usage and Ops aggregation settings and displays their
 * runtime status. It registers an independent save target with the enclosing settings page
 * because its API returns configuration, deployment capabilities, and runtime observations
 * together. A dirty tracker compares only editable switches and the Usage interval,
 * leaving status refreshes outside the saved snapshot.
 *
 * Responses always update runtime observations, but editable fields are copied only while
 * the draft is clean. Save and discard deliberately clear that guard before applying
 * server settings, then record the resulting baseline after Vue updates. Manual backfill
 * uses its own request and refreshes status after acceptance; the displayed availability
 * limits and draft Usage switch govern its controls.
 */

/**
 * StatusItem renders one status label and its slotted value inside the card's definition
 * grid. Passed attributes remain on the wrapper so coverage and error entries can set
 * their column span.
 */
const StatusItem = defineComponent({
  // label names the status value; each caller supplies translated text while
  // the default slot supplies the corresponding observation or formatted duration.
  props: { label: { type: String, required: true } },
  /**
   * setup returns the status item's render function using its required label and optional
   * default slot. Attributes are forwarded onto the wrapper so the parent grid retains its
   * chosen layout classes.
   */
  setup(props, { slots, attrs }) {
    return () =>
      h('div', attrs, [
        h('dt', { class: 'text-xs text-gray-500 dark:text-dark-400' }, props.label),
        h(
          'dd',
          { class: 'mt-1 break-words font-medium text-gray-800 dark:text-dark-100' },
          slots.default?.(),
        ),
      ])
  },
})

// Translation and component identity bindings provide localized status output,
// unique input IDs, and toast feedback for this independently saved card.
const { t, locale } = useI18n()
const uid = useId()
const appStore = useAppStore()
// Request indicators disable the corresponding controls while reads, settings
// writes, and backfill submissions progress independently within the mounted card.
const loading = ref(false)
const saving = ref(false)
const backfilling = ref(false)
// The latest response supplies capabilities and observations even when editable
// settings are dirty; backfillDays remains a separate immediate-action input.
const state = ref<PreAggregationSettingsResponse | null>(null)
const backfillDays = ref(7)
// usage and ops are edited separately and sent together in one replacement
// request. The dirty tracker snapshots both groups, excluding runtime observations
// and the separate immediate-action backfill input.
const form = reactive({
  // usage includes its interval because the API replaces enabled state and
  // cadence together whenever this card saves its complete settings draft.
  usage: {
    enabled: false, // periodic Usage aggregation
    interval_seconds: 60, // aggregation interval in seconds
  },
  // ops carries the runtime switch; deployment availability separately controls
  // whether the panel lets the administrator change this aggregation setting.
  ops: {
    enabled: false, // periodic Ops aggregation
  },
})

// The shared save bar compares these editable fields against the baseline
// recorded after loading, accepted saving, or the registered discard operation.
const { dirty, markClean } = useDirtyTracker({ form: () => form })
useSettingsSaveTarget('preAggregation', {
  dirty,
  save: saveSettings,
  // Clearing the dirty guard before reloading allows applyResponse to replace
  // edited values with server settings during the registered discard action.
  discard: async () => {
    markClean()
    await loadSettings()
  },
})

// Backfill controls require both deployed support and the current Usage draft
// switch; coverage text uses the latest server observation independently of edits.
const canBackfill = computed(() =>
  Boolean(state.value?.availability.manual_backfill_available && form.usage.enabled),
)

const usageCoverage = computed(() => {
  const status = state.value?.usage_status
  if (!status?.coverage_start || !status.live_watermark) {
    return t('admin.settings.preAggregation.noData')
  }
  return `${formatDate(status.coverage_start)} - ${formatDate(status.live_watermark)}`
})

/**
 * applyResponse publishes runtime observations and clamps the backfill input to the
 * returned deployment limit. Editable aggregation values are copied only when the dirty
 * tracker currently reports clean; dirty drafts keep their switches and interval even as
 * status changes. Callers that intend to discard or replace edits clear the dirty baseline
 * before invoking this function.
 *
 * The response remains available as the status source regardless of draft ownership. This
 * keeps a manual status refresh from overwriting edits that were already dirty when its
 * response is applied, while accepted saves can adopt normalized server values.
 */
function applyResponse(response: PreAggregationSettingsResponse) {
  state.value = response
  // Deployment limits can change between observations; constrain the separate
  // backfill input before rendering controls with the new maximum day count.
  backfillDays.value = Math.min(
    Math.max(backfillDays.value, 1),
    response.availability.manual_backfill_max_days,
  )
  if (dirty.value) return
  form.usage.enabled = response.settings.usage.enabled
  form.usage.interval_seconds = response.settings.usage.interval_seconds
  form.ops.enabled = response.settings.ops.enabled
}

/**
 * loadSettings fetches configuration and status, reporting read failures through the page
 * toast store. It captures the dirty flag before the request and lets applyResponse decide
 * whether the response may replace editable settings. When the request began clean, it
 * records a clean baseline after Vue's next update, including when the request fails.
 *
 * This entry-time snapshot differs from the response-time guard: an edit made while a
 * clean-started read is pending can remain in the draft and still become the new clean
 * baseline. Callers use this operation for mount, refresh, discard, and post-backfill
 * observation; it does not cancel or sequence concurrent reads.
 */
async function loadSettings() {
  loading.value = true
  const wasDirty = dirty.value
  try {
    applyResponse(await adminAPI.settings.getPreAggregationSettings())
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.settings.preAggregation.loadFailed')))
  } finally {
    loading.value = false
  }
  // Reads that began clean record their baseline after Vue applies response writes.
  // The entry flag remains decisive even when edits occurred during the request.
  if (!wasDirty) {
    await nextTick()
    markClean()
  }
}

/**
 * saveSettings submits the complete Usage and Ops draft and returns whether the update
 * succeeds. The save registry uses that boolean to combine this card with other dirty
 * targets. On success, it clears the dirty guard before applying the returned
 * configuration, waits for Vue to settle, and records the accepted baseline.
 *
 * A failed update reports an error and leaves the edited values available for another
 * attempt. The saving indicator disables normal controls during the request, but this
 * function does not version programmatic edits or competing status reads; response
 * application follows the same dirty guard as other reads.
 */
async function saveSettings(): Promise<boolean> {
  saving.value = true
  try {
    const response = await adminAPI.settings.updatePreAggregationSettings({
      usage: {
        enabled: form.usage.enabled,
        interval_seconds: Number(form.usage.interval_seconds),
      },
      ops: { enabled: form.ops.enabled },
    })
    // The accepted server response may normalize editable settings. Clear the
    // dirty guard first so applyResponse can publish those values before snapshotting.
    markClean()
    applyResponse(response)
    await nextTick()
    markClean()
    return true
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.settings.preAggregation.saveFailed')))
    return false
  } finally {
    saving.value = false
  }
}

/**
 * startBackfill submits the selected day count when a server response is available. The
 * panel controls enforce availability; success reports acceptance and reloads
 * observations, while request failure reports an error and clears its busy indicator.
 */
async function startBackfill() {
  if (!state.value) return
  backfilling.value = true
  try {
    await adminAPI.settings.backfillPreAggregation(Number(backfillDays.value))
    appStore.showSuccess(t('admin.settings.preAggregation.backfillAccepted'))
    await loadSettings()
  } catch (error) {
    appStore.showError(
      extractApiErrorMessage(error, t('admin.settings.preAggregation.backfillFailed')),
    )
  } finally {
    backfilling.value = false
  }
}

/**
 * phaseLabel resolves a translated runtime phase label, using unavailable for an empty
 * phase. If the translation lookup returns its key, the raw phase is displayed so
 * unfamiliar server states remain identifiable.
 */
function phaseLabel(phase: string): string {
  const key = `admin.settings.preAggregation.phases.${phase || 'unavailable'}`
  const translated = t(key)
  // Unknown server phases keep their readable raw value when no catalog entry
  // exists, rather than exposing the internal translation key to the administrator.
  return translated === key ? phase : translated
}

/**
 * phaseClass selects status text colors for errors, active phases, and inactivity.
 */
function phaseClass(phase: string): string {
  if (phase === 'error') return 'text-red-600 dark:text-red-400'
  if (phase === 'live' || phase === 'backfill') return 'text-emerald-600 dark:text-emerald-400'
  if (phase === 'disabled' || phase === 'unavailable') return 'text-gray-500 dark:text-gray-400'
  return 'text-gray-800 dark:text-gray-200'
}

/**
 * formatDate formats a supplied timestamp in the current interface locale. Missing values
 * display the no-data label, while invalid date text is returned unchanged so the server
 * value remains inspectable.
 */
function formatDate(value?: string): string {
  if (!value) return t('admin.settings.preAggregation.noData')
  const date = new Date(value)
  // Invalid server timestamps remain visible as supplied; hiding the value would
  // make a malformed observation indistinguishable from an absent status timestamp.
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(date)
}

/**
 * formatDuration formats milliseconds using milliseconds, fractional seconds, or whole
 * minutes and seconds. Nonfinite and nonpositive values display zero seconds; long
 * durations discard subsecond precision while shorter values use the displayed unit's
 * rounding.
 */
function formatDuration(milliseconds: number): string {
  // Missing or unusable durations display a neutral zero in status cells,
  // allowing the rest of the latest observation to remain readable.
  if (!Number.isFinite(milliseconds) || milliseconds <= 0) return '0s'
  // Subsecond values round to the nearest millisecond.
  if (milliseconds < 1000) return `${Math.round(milliseconds)}ms`
  // Seconds retain one decimal before minute formatting applies.
  if (milliseconds < 60_000) return `${(milliseconds / 1000).toFixed(1)}s`
  // Longer durations omit fractions from both displayed units.
  const minutes = Math.floor(milliseconds / 60_000)
  const seconds = Math.floor((milliseconds % 60_000) / 1000)
  return `${minutes}m ${seconds}s`
}

// Mounting loads this card even when its surrounding settings tab is hidden;
// later refreshes and backfill observations use the same draft-protection logic.
onMounted(loadSettings)
</script>
