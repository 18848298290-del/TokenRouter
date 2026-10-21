<template>
  <!-- @project-doc docs/interfaces/tf_cli_web_import.md#user_confirmation -->
  <BaseDialog :show="show" :title="t('keys.tfImport.title')" width="narrow" @close="requestClose">
    <div class="min-w-0 space-y-5" data-test="tf-cli-import-dialog">
      <div
        class="flex min-w-0 items-center gap-3 border-b border-gray-100 pb-4 dark:border-dark-700"
      >
        <div
          class="flex h-9 w-9 shrink-0 items-center justify-center rounded-control bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200"
        >
          <Icon name="terminal" size="md" />
        </div>
        <div class="min-w-0">
          <p class="truncate text-sm font-medium text-gray-900 dark:text-white">
            {{ keyLabel }}
          </p>
          <p class="truncate text-xs text-gray-500 dark:text-dark-400">
            {{ pageOrigin }}
          </p>
        </div>
      </div>

      <div
        v-if="phase === 'discovering'"
        class="flex min-h-36 flex-col items-center justify-center gap-3 py-4 text-center"
        role="status"
      >
        <Icon
          name="refresh"
          size="lg"
          class="animate-spin text-primary-500"
          :animate-on-hover="false"
        />
        <div>
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('keys.tfImport.discoveringTitle') }}
          </p>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            {{ t('keys.tfImport.discoveringDescription') }}
          </p>
        </div>
      </div>

      <template v-else-if="phase === 'ready' && target">
        <div
          v-if="target.verified"
          class="flex gap-3 rounded-control border border-emerald-200 bg-emerald-50 p-3 text-emerald-800 dark:border-emerald-800/60 dark:bg-emerald-950/30 dark:text-emerald-300"
          data-test="tf-cli-verified"
          role="status"
        >
          <Icon name="shield" size="md" class="mt-0.5 shrink-0" />
          <div class="min-w-0">
            <p class="text-sm font-medium">{{ t('keys.tfImport.verifiedTitle') }}</p>
            <p class="mt-1 text-xs leading-5">{{ t('keys.tfImport.verifiedDescription') }}</p>
          </div>
        </div>
        <div
          v-else
          class="flex gap-3 rounded-control border border-amber-200 bg-amber-50 p-3 text-amber-900 dark:border-amber-700/60 dark:bg-amber-950/30 dark:text-amber-200"
          data-test="tf-cli-unverified"
          role="alert"
        >
          <Icon name="exclamationTriangle" size="md" class="mt-0.5 shrink-0" />
          <div class="min-w-0">
            <p class="text-sm font-medium">{{ t('keys.tfImport.unverifiedTitle') }}</p>
            <p class="mt-1 text-xs leading-5">{{ t('keys.tfImport.unverifiedDescription') }}</p>
          </div>
        </div>

        <dl
          class="divide-y divide-gray-100 border-y border-gray-100 text-sm dark:divide-dark-700 dark:border-dark-700"
        >
          <div class="flex min-w-0 items-center justify-between gap-4 py-3">
            <dt class="shrink-0 text-gray-500 dark:text-dark-400">
              {{ t('keys.tfImport.destination') }}
            </dt>
            <dd class="truncate font-mono text-xs text-gray-900 dark:text-dark-100">
              127.0.0.1:{{ target.port }}
            </dd>
          </div>
          <div v-if="groupLabel" class="flex min-w-0 items-center justify-between gap-4 py-3">
            <dt class="shrink-0 text-gray-500 dark:text-dark-400">
              {{ t('keys.tfImport.group') }}
            </dt>
            <dd class="truncate text-gray-900 dark:text-dark-100">{{ groupLabel }}</dd>
          </div>
        </dl>

        <p class="text-xs leading-5 text-gray-500 dark:text-dark-400">
          {{ t('keys.tfImport.confirmDescription') }}
        </p>
      </template>

      <div
        v-else-if="phase === 'sending'"
        class="flex min-h-36 flex-col items-center justify-center gap-3 py-4 text-center"
        role="status"
      >
        <Icon name="terminal" size="lg" class="text-primary-500" />
        <div>
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('keys.tfImport.waitingTitle') }}
          </p>
          <p class="mt-1 max-w-sm text-xs leading-5 text-gray-500 dark:text-dark-400">
            {{ t('keys.tfImport.waitingDescription') }}
          </p>
        </div>
      </div>

      <div
        v-else-if="phase === 'accepted'"
        class="flex gap-3 rounded-control border border-emerald-200 bg-emerald-50 p-4 text-emerald-800 dark:border-emerald-800/60 dark:bg-emerald-950/30 dark:text-emerald-300"
        data-test="tf-cli-accepted"
        role="status"
      >
        <Icon name="checkCircle" size="lg" class="shrink-0" :animate-on-hover="false" />
        <div class="min-w-0">
          <p class="text-sm font-medium">{{ t('keys.tfImport.acceptedTitle') }}</p>
          <p class="mt-1 text-xs leading-5">{{ t('keys.tfImport.acceptedDescription') }}</p>
        </div>
      </div>

      <div
        v-else-if="phase === 'notFound'"
        class="flex gap-3 rounded-control border border-gray-200 bg-gray-50 p-4 text-gray-700 dark:border-dark-700 dark:bg-dark-800 dark:text-dark-200"
        data-test="tf-cli-not-found"
        role="alert"
      >
        <Icon
          name="xCircle"
          size="lg"
          class="shrink-0 text-gray-500 dark:text-dark-400"
          :animate-on-hover="false"
        />
        <div class="min-w-0">
          <p class="text-sm font-medium">{{ t('keys.tfImport.notFoundTitle') }}</p>
          <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-dark-400">
            {{ t('keys.tfImport.notFoundDescription') }}
          </p>
        </div>
      </div>

      <div
        v-else-if="phase === 'error'"
        class="flex gap-3 rounded-control border border-red-200 bg-red-50 p-4 text-red-800 dark:border-red-800/60 dark:bg-red-950/30 dark:text-red-300"
        data-test="tf-cli-import-error"
        role="alert"
      >
        <Icon name="exclamationCircle" size="lg" class="shrink-0" />
        <div class="min-w-0">
          <p class="text-sm font-medium">{{ t('keys.tfImport.errorTitle') }}</p>
          <p class="mt-1 text-xs leading-5">{{ importErrorMessage }}</p>
        </div>
      </div>
    </div>

    <template #footer>
      <template v-if="phase === 'ready'">
        <button type="button" class="btn btn-secondary" @click="requestClose">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary inline-flex items-center gap-2"
          data-test="tf-cli-send"
          @click="sendKey"
        >
          <Icon name="upload" size="sm" />
          {{ t('keys.tfImport.send') }}
        </button>
      </template>
      <template v-else-if="phase === 'discovering'">
        <button type="button" class="btn btn-secondary" @click="requestClose">
          {{ t('common.cancel') }}
        </button>
      </template>
      <template v-else-if="phase === 'sending'">
        <button
          type="button"
          class="btn btn-primary inline-flex cursor-wait items-center gap-2"
          disabled
        >
          <Icon name="refresh" size="sm" class="animate-spin" :animate-on-hover="false" />
          {{ t('keys.tfImport.waiting') }}
        </button>
      </template>
      <template v-else-if="phase === 'accepted'">
        <button type="button" class="btn btn-primary" @click="requestClose">
          {{ t('common.close') }}
        </button>
      </template>
      <template v-else>
        <button type="button" class="btn btn-secondary" @click="requestClose">
          {{ t('common.close') }}
        </button>
        <button
          type="button"
          class="btn btn-primary inline-flex items-center gap-2"
          data-test="tf-cli-retry"
          @click="startDiscovery"
        >
          <Icon name="refresh" size="sm" />
          {{ t('keys.tfImport.retry') }}
        </button>
      </template>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import type { ApiKey } from '@/api/keys.types'
import { findTfCli, importKeyToTf, type TfCliTarget } from '@/utils/tfCliImport'

// TfCliImportDialog.vue coordinates local TokenFlux CLI discovery and confirmed API-key
// import. The current page origin accompanies the key, and a single-group label is attached
// only when the selected key has a noncomposite group binding. Phase state separates
// discovery, confirmation, transmission, acceptance, and user-visible errors.
//
// Discovery and sending share one owned AbortController. Reopening for another key, closing,
// or unmounting cancels current work, and completion additionally checks controller identity
// before publishing results. Session-proof failures retain the discovered destination but
// remove its verified status, allowing the dialog to return to confirmation while other
// protocol errors use their localized failure messages.

// Phase identifies discovery, user confirmation, sending, acceptance, and recoverable import
// results.
type Phase = 'discovering' | 'ready' | 'sending' | 'accepted' | 'notFound' | 'error'

// Props supplies the dialog visibility and API-key record selected for import.
interface Props {
  show: boolean // Import dialog visibility.
  apiKey: ApiKey | null // Selected key including secret.
}

// Emits lets the owning key page close the import dialog after dismissal.
interface Emits {
  (event: 'close'): void // Parent dismissal request.
}

// Dialog dependencies capture page origin and the selected key used for local CLI discovery
// and import confirmation.
const props = defineProps<Props>()
const emit = defineEmits<Emits>()
const { t } = useI18n()
const pageOrigin = window.location.origin

// phase and target retain the current import session; importError supplies feedback while
// operationController owns cancellation for discovery or sending.
const phase = ref<Phase>('discovering')
const target = shallowRef<TfCliTarget | null>(null)
const importError = ref('')
let operationController: AbortController | null = null

// keyLabel and groupLabel show the selected import identity; composite or ungrouped keys
// omit the single-group summary.
const keyLabel = computed(() => props.apiKey?.name || `#${props.apiKey?.id ?? ''}`)
const groupLabel = computed(() => {
  const key = props.apiKey
  if (!key || key.is_composite || key.group_id == null) return ''
  return key.group?.display_name || key.group?.name || String(key.group_id)
})

// importErrorMessage maps local CLI response categories to user-facing error messages,
// grouping origin and malformed-payload failures by their corrective context.
const importErrorMessage = computed(() => {
  switch (importError.value) {
    case 'rejected':
      return t('keys.tfImport.errors.rejected')
    case 'cancelled':
      return t('keys.tfImport.errors.cancelled')
    case 'busy':
      return t('keys.tfImport.errors.busy')
    case 'origin_not_allowed':
    case 'host_mismatch':
      return t('keys.tfImport.errors.originNotAllowed')
    case 'invalid_key':
    case 'invalid_metadata':
    case 'invalid_json':
    case 'content_type':
      return t('keys.tfImport.errors.invalidPayload')
    case 'unsupported_protocol':
      return t('keys.tfImport.errors.unsupportedVersion')
    case 'network_error':
      return t('keys.tfImport.errors.network')
    default:
      return t('keys.tfImport.errors.unexpected')
  }
})

// cancelOperation aborts the currently owned discovery or send operation and releases its
// controller. Calling it repeatedly is safe when closing, changing keys, starting a
// replacement, or disposing the dialog.
function cancelOperation(): void {
  operationController?.abort()
  operationController = null
}

// startDiscovery locates a CLI listener for the visible selected key and clears the previous
// target and error. It cancels existing work before assigning a fresh controller. After
// discovery, the response can publish only if that controller still owns the operation,
// remains un-aborted, and the dialog is visible. These checks prevent closed dialogs or a
// different selected key from receiving an obsolete discovery result.
async function startDiscovery(): Promise<void> {
  if (!props.show || !props.apiKey) return

  // Abort and release the previous operation before assigning this controller, so its late
  // completion cannot retain ownership while the replacement starts updating dialog state.
  cancelOperation()
  const controller = new AbortController()
  operationController = controller
  phase.value = 'discovering'
  target.value = null
  importError.value = ''

  const discovered = await findTfCli(controller.signal)
  // Closing, replacement, or cancellation can occur during the await. Reject those results
  // before they replace the destination and discovery phase for the current selected key.
  if (controller.signal.aborted || operationController !== controller || !props.show) return

  operationController = null
  target.value = discovered
  phase.value = discovered ? 'ready' : 'notFound'
}

// sendKey submits the selected key and page origin to the discovered CLI target when
// confirmation is ready. A new controller replaces previous work, and completion checks
// ownership, abortion, and visibility before updating phase. Single-group metadata is
// included only for noncomposite grouped keys. Expired or unavailable session proof marks
// the retained destination unverified and returns to ready, while other failures select
// localized error feedback and successful imports show acceptance.
async function sendKey(): Promise<void> {
  const key = props.apiKey
  const destination = target.value
  if (!key || !destination || phase.value !== 'ready') return

  // Abort and release the previous operation before assigning this controller, so its late
  // completion cannot retain ownership while the replacement starts updating dialog state.
  cancelOperation()
  const controller = new AbortController()
  operationController = controller
  phase.value = 'sending'

  const payload = {
    key: key.key,
    host: pageOrigin,
    key_name: key.name,
    ...(!key.is_composite && key.group_id != null
      ? { group_id: key.group_id, group_name: key.group?.display_name || key.group?.name }
      : {}),
  }
  const result = await importKeyToTf(destination, payload, controller.signal)
  // Closing, replacement, or cancellation can occur during the await. Reject those results
  // before they publish acceptance, an error, or another confirmation state for this import.
  if (controller.signal.aborted || operationController !== controller || !props.show) return

  operationController = null
  if (result.ok) {
    phase.value = 'accepted'
    return
  }

  // An expired proof can be retried against the discovered endpoint after trust is re-
  // established, so retain its destination while removing the verified-session flag.
  if (result.error === 'session_expired' || result.error === 'session_proof_unavailable') {
    target.value = { ...destination, verified: false }
    phase.value = 'ready'
    return
  }

  importError.value = result.error
  phase.value = 'error'
}

// requestClose cancels the owned local CLI operation and then asks the parent to dismiss
// the dialog. The close event follows cancellation so its consumer receives a dismissal
// request after the controller has been aborted and released.
function requestClose(): void {
  cancelOperation()
  emit('close')
}

watch(
  () => [props.show, props.apiKey?.id] as const,
  ([show]) => {
    if (show) void startDiscovery()
    else cancelOperation()
  },
  { immediate: true },
)

onUnmounted(cancelOperation)
</script>
