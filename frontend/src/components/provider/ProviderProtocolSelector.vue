<template>
  <SettingsSection
    :title="hideTitle ? undefined : t('admin.protocols.nativeTitle')"
    :hint="t('admin.protocols.nativeHint')"
  >
    <div
      v-if="protocolCatalogError"
      class="flex items-center gap-2 text-sm text-red-500"
      role="alert"
    >
      <span>{{ t('admin.protocols.loadError') }}</span>
      <button
        type="button"
        class="btn btn-secondary"
        data-testid="protocol-catalog-retry"
        :disabled="protocolCatalogLoading"
        @click="retryCatalog"
      >
        {{ t('common.retry') }}
      </button>
    </div>
    <p v-else-if="!protocolCatalog" class="input-hint">{{ t('common.loading') }}</p>
    <!-- Each native protocol uses a row switch with its endpoint beside the name, matching the group protocol controls. -->
    <div v-else class="divide-y divide-gray-100 dark:divide-dark-700">
      <div v-for="id in options" :key="id" class="flex items-center gap-3 py-2.5">
        <div class="flex min-w-0 flex-1 items-baseline gap-x-2">
          <span
            class="shrink-0 text-sm font-medium"
            :class="
              modelValue?.includes(id)
                ? 'text-primary-900 dark:text-dark-50'
                : 'text-gray-400 dark:text-dark-400'
            "
            >{{ protocolName(id) }}</span
          >
          <code
            v-if="protocolEndpoint(id)"
            class="hidden min-w-0 truncate text-xs text-gray-400 dark:text-dark-500 sm:inline"
            >{{ protocolEndpoint(id) }}</code
          >
        </div>
        <Toggle
          size="sm"
          class="shrink-0"
          :model-value="!!modelValue?.includes(id)"
          :data-native-protocol="id"
          :aria-label="protocolName(id)"
          @update:model-value="toggle(id)"
        />
      </div>
    </div>
  </SettingsSection>
</template>
<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ProtocolID } from '@/api/protocols.types'
import Toggle from '@/components/common/Toggle.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import {
  loadProtocolCatalog,
  nativeProtocolOptions,
  protocolCatalog,
  protocolCatalogError,
  protocolCatalogLoading,
} from '@/api/admin/protocolCapabilities'

// ProviderProtocolSelector.vue edits a provider native protocol set using the shared
// backend capability catalog. Options depend on platform, provider type, and
// authentication mode. Loading and retry state are shared with other protocol controls,
// and missing catalog data postpones default selection.
//
// An undefined selection receives all current native options when the catalog becomes
// available. An explicit empty selection remains empty during ordinary display. Changing
// platform, type, or authentication mode replaces the selection with the new native set,
// while toggling an item emits selected options in catalog order.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  // platform, type, and authMode select one native catalog profile together. modelValue
  // stores the administrator selection for that profile and retains an explicitly empty
  // set until an identity input changes.
  // Undefined initializes all native options after catalog loading; an explicit empty
  // array keeps every option disabled.
  modelValue?: ProtocolID[]
  // Platform used with type and authMode to choose the catalog profile and reset
  // selection on changes.
  platform: string
  // Provider type used with platform and authMode to choose native options and reset
  // selection on changes.
  type: string
  // Authentication variant used to choose native protocol options for the current
  // provider profile.
  authMode?: string
  // Hides the section title when an enclosing bulk application row already provides it.
  hideTitle?: boolean
}>()
// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  'update:modelValue': [value: ProtocolID[]] // Replacement model value draft value.
}>()
// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// options derives native protocols from the shared catalog using platform, provider type,
// and authentication mode together.
const options = computed(() => nativeProtocolOptions(props.platform, props.type, props.authMode))

// retryCatalog starts or joins the shared protocol catalog load for this selector.
// Rejection is consumed here because shared catalog error state drives the retry button
// and error message.
function retryCatalog() {
  // The shared loader records failure for every selector, so this consumed rejection
  // leaves error presentation and retry available through shared reactive state.
  void loadProtocolCatalog().catch(() => {})
}
retryCatalog()

// Catalog arrival initializes undefined selections, while identity changes select a new
// native set; ordinary catalog refreshes preserve an explicitly empty selection.
watch(
  () => [props.platform, props.type, props.authMode, protocolCatalog.value],
  (_, previous) => {
    if (!protocolCatalog.value) return
    const changed =
      previous &&
      (previous[0] !== props.platform ||
        previous[1] !== props.type ||
        previous[2] !== props.authMode)
    if (props.modelValue === undefined || changed) emit('update:modelValue', [...options.value])
  },
  { immediate: true },
)
// protocolName returns the localized WebSocket name or the catalog protocol name. When a
// protocol has no catalog entry, its exact identifier remains visible as the display
// label.
function protocolName(id: ProtocolID) {
  if (id === 'openai_responses_websocket') return t('admin.protocols.responsesWebSocket')
  // A missing catalog label falls back to the exact protocol identifier so the switch
  // remains identifiable while catalog data is incomplete.
  return protocolCatalog.value?.protocols.find((item) => item.id === id)?.name ?? id
}
// protocolEndpoint returns the catalog endpoint associated with the supplied protocol
// identifier. Missing entries return undefined so the template omits endpoint text while
// retaining the protocol label and switch.
function protocolEndpoint(id: ProtocolID) {
  return protocolCatalog.value?.protocols.find((item) => item.id === id)?.endpoint
}
// toggle emits the toggled selection restricted to the current native protocol options.
// Undefined input starts empty, and filtering through options removes stale IDs while
// preserving the catalog order.
function toggle(id: ProtocolID) {
  const selected = new Set(props.modelValue ?? [])
  if (selected.has(id)) selected.delete(id)
  else selected.add(id)
  emit(
    'update:modelValue',
    options.value.filter((item) => selected.has(item)),
  )
}
</script>
