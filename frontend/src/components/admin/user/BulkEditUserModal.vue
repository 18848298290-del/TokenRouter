<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.bulkLimits.title')"
    width="normal"
    @close="emit('close')"
  >
    <form id="bulk-edit-user-limits-form" class="space-y-5" @submit.prevent="handleSubmit">
      <p class="text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('admin.users.bulkLimits.selectedCount', { count: selectedIds.length }) }}
      </p>

      <div
        class="divide-y divide-gray-200 border-y border-gray-200 dark:divide-dark-700 dark:border-dark-700"
      >
        <div class="space-y-3 py-4">
          <div class="flex items-center justify-between gap-4">
            <label for="bulk-concurrency" class="input-label mb-0">
              {{ t('admin.users.columns.concurrency') }}
            </label>
            <Toggle
              v-model="enableConcurrency"
              :aria-label="t('admin.users.bulkLimits.enableConcurrency')"
              data-test="enable-concurrency"
            />
          </div>
          <input
            v-if="enableConcurrency"
            id="bulk-concurrency"
            v-model="concurrencyValue"
            type="number"
            min="0"
            step="1"
            class="input"
            data-test="concurrency-input"
          />
        </div>

        <div class="space-y-3 py-4">
          <div class="flex items-center justify-between gap-4">
            <label for="bulk-rpm-limit" class="input-label mb-0">
              {{ t('admin.users.form.rpmLimit') }}
            </label>
            <Toggle
              v-model="enableRPMLimit"
              :aria-label="t('admin.users.bulkLimits.enableRPMLimit')"
              data-test="enable-rpm-limit"
            />
          </div>
          <div v-if="enableRPMLimit">
            <input
              id="bulk-rpm-limit"
              v-model="rpmLimitValue"
              type="number"
              min="0"
              step="1"
              class="input"
              data-test="rpm-limit-input"
            />
            <p v-if="parsedRPMLimit === 0" class="input-hint">
              {{ t('admin.users.bulkLimits.unlimited') }}
            </p>
          </div>
        </div>
      </div>

      <p v-if="hasInvalidValue" class="text-sm text-red-600 dark:text-red-400">
        {{ t('admin.users.bulkLimits.nonNegativeInteger') }}
      </p>
      <p v-if="selectionTooLarge" class="text-sm text-red-600 dark:text-red-400">
        {{ t('admin.users.bulkLimits.selectionLimit', { max: MAX_BATCH_USER_IDS }) }}
      </p>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="bulk-edit-user-limits-form"
          class="btn btn-primary"
          :disabled="!canSubmit"
          data-test="submit"
        >
          {{
            submitting ? t('admin.users.bulkLimits.applying') : t('admin.users.bulkLimits.apply')
          }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { BatchUpdateUserLimitsRequest } from '@/api/admin/users'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'

// BulkEditUserModal.vue edits concurrency and RPM limits for selected users. Each field
// has a separate enable switch, so untouched limits are omitted from the request.
// Submission validates nonnegative integers, enforces the selection cap, and asks for
// overwrite confirmation before calling the batch endpoint.
//
// The parser distinguishes omitted, invalid, and numeric values before the
// enabled-field switches participate in the submission decision. An empty enabled field
// contributes no update, an invalid enabled field blocks submission, and explicit zero
// remains a valid value. The final payload copies the selected IDs and includes only
// fields whose parsed values survived those checks.

// props supplies dialog visibility and the selected user IDs that become the explicit
// targets of the batch update.
const props = defineProps<{
  show: boolean // Parent-controlled dialog visibility.
  selectedIds: number[] // Selected user identifiers.
}>()

// emit reports the affected-user count after a successful batch update and asks the
// parent to close the dialog afterward.
const emit = defineEmits<{
  close: [] // Parent closure request.
  success: [affected: number] // Affected user count.
}>()

// The translation and notification services provide confirmation details, validation
// labels, and success or error feedback for batch limit updates.
const { t } = useI18n()
const appStore = useAppStore()
// enableConcurrency and enableRPMLimit decide which draft fields participate in
// validation and submission; the draft values stay local until the confirmed request.
const enableConcurrency = ref(false)
const enableRPMLimit = ref(false)
const concurrencyValue = ref<string | number>('')
const rpmLimitValue = ref<string | number>('')
// submitting blocks repeated form submission while the confirmed batch request is
// pending and is cleared after success or failure.
const submitting = ref(false)
// MAX_BATCH_USER_IDS caps the selected user IDs accepted by this dialog before any
// overwrite confirmation or API request occurs.
const MAX_BATCH_USER_IDS = 500

// parseLimit converts a draft value into a nonnegative integer. Blank text returns
// undefined for omission, invalid numbers return null for validation, and explicit zero
// remains an included value.
const parseLimit = (value: string | number): number | null | undefined => {
  const trimmed = String(value).trim()
  if (!trimmed) return undefined
  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed) || parsed < 0) return null
  return parsed
}

// parsedConcurrency and parsedRPMLimit preserve the parser's omitted, invalid, and
// numeric states only for enabled fields when deriving validation and request data.
const parsedConcurrency = computed(() =>
  enableConcurrency.value ? parseLimit(concurrencyValue.value) : undefined,
)
const parsedRPMLimit = computed(() =>
  enableRPMLimit.value ? parseLimit(rpmLimitValue.value) : undefined,
)
// hasInvalidValue rejects enabled fields parsed as null, while hasUpdate requires at
// least one enabled numeric value for the outgoing batch request.
const hasInvalidValue = computed(
  () => parsedConcurrency.value === null || parsedRPMLimit.value === null,
)
const hasUpdate = computed(
  () =>
    (parsedConcurrency.value !== undefined && parsedConcurrency.value !== null) ||
    (parsedRPMLimit.value !== undefined && parsedRPMLimit.value !== null),
)
// selectionTooLarge checks the selected ID count against the batch limit before
// canSubmit permits confirmation and submission of the enabled values.
const selectionTooLarge = computed(() => props.selectedIds.length > MAX_BATCH_USER_IDS)
// canSubmit combines the selected IDs, enabled fields, parsing results, and pending
// state to decide whether submission is available. A blank enabled field contributes no
// update, an invalid enabled field blocks the form, and numeric zero remains a valid
// update. The same computed decision gates both the button and the submit handler, so
// direct form submission follows the visible validation state.
const canSubmit = computed(
  () =>
    props.selectedIds.length > 0 &&
    !selectionTooLarge.value &&
    hasUpdate.value &&
    !hasInvalidValue.value &&
    !submitting.value,
)

// reset clears both enabled switches, draft values, and pending state when the modal
// opens.
const reset = () => {
  enableConcurrency.value = false
  enableRPMLimit.value = false
  concurrencyValue.value = ''
  rpmLimitValue.value = ''
  submitting.value = false
}

// Reset draft switches and values on opening so a previous batch's enabled fields do
// not silently carry into the next selection.
watch(
  () => props.show,
  (show) => {
    if (show) reset()
  },
)

// handleSubmit builds a request from enabled, valid limits and the current selected
// IDs. It asks for overwrite confirmation before setting the pending flag and calling
// the batch endpoint. Cancellation leaves the draft open, while a successful response
// emits the affected count followed by close. The selected IDs are copied before
// confirmation so the request retains the targets used to build its confirmation
// details.
const handleSubmit = async () => {
  if (!canSubmit.value) return

  const request: BatchUpdateUserLimitsRequest = {
    user_ids: [...props.selectedIds], // Explicit update targets.
    all: false, // All-user update flag.
  }
  const fields: string[] = []
  if (parsedConcurrency.value !== undefined && parsedConcurrency.value !== null) {
    request.concurrency = parsedConcurrency.value
    fields.push(
      t('admin.users.bulkLimits.concurrencyValue', {
        value: parsedConcurrency.value, // Stored option value.
      }),
    )
  }
  if (parsedRPMLimit.value !== undefined && parsedRPMLimit.value !== null) {
    request.rpm_limit = parsedRPMLimit.value
    fields.push(
      parsedRPMLimit.value === 0
        ? t('admin.users.bulkLimits.rpmUnlimitedValue')
        : t('admin.users.bulkLimits.rpmValue', {
            value: parsedRPMLimit.value, // Stored option value.
          }),
    )
  }

  const confirmed = window.confirm(
    t('admin.users.bulkLimits.confirm', {
      count: props.selectedIds.length, // Displayed affected-record count.
      fields: fields.join(', '), // Confirmed limit-change descriptions.
    }),
  )
  if (!confirmed) return

  submitting.value = true
  try {
    // Wait for the mutation before reporting affected users and closing, so the parent
    // refresh follows the server's completed batch response.
    const result = await adminAPI.users.batchUpdateLimits(request)
    appStore.showSuccess(
      t('admin.users.bulkLimits.success', {
        count: result.affected, // Displayed affected-record count.
      }),
    )
    emit('success', result.affected)
    emit('close')
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('admin.users.bulkLimits.failed')))
  } finally {
    submitting.value = false
  }
}
</script>
