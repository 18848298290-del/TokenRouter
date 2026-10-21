<template>
  <SettingsSection :title="t('admin.protocols.groupTitle')" :hint="t('admin.protocols.groupHint')">
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
    <p v-else-if="!protocolCatalog" class="input-hint">
      {{ t('common.loading') }}
    </p>
    <div data-testid="client-protocol-list" class="divide-y divide-gray-100 dark:divide-dark-700">
      <div v-for="protocol in protocols" :key="protocol.id" class="py-2.5">
        <div class="flex items-center gap-3">
          <div class="flex min-w-0 flex-1 items-baseline gap-x-2">
            <span
              class="shrink-0 text-sm font-medium"
              :class="
                isEnabled(protocol.id)
                  ? 'text-primary-900 dark:text-dark-50'
                  : 'text-gray-400 dark:text-dark-400'
              "
              >{{ protocol.name }}</span
            ><code
              class="hidden min-w-0 truncate text-xs text-gray-400 dark:text-dark-500 sm:inline"
              :data-protocol-endpoint="protocol.id"
              >{{ protocol.endpoint }}</code
            >
          </div>
          <template v-if="isEnabled(protocol.id) && hasFallbackTargets(protocol.id)">
            <span class="hidden shrink-0 text-xs text-gray-400 dark:text-dark-500 md:inline">{{
              t('admin.protocols.fallbackWhen')
            }}</span>
            <Select
              class="protocol-mode-select w-32 shrink-0 sm:w-40"
              :aria-label="`${protocol.name}: ${t('admin.protocols.fallback')}`"
              :model-value="fallbackMode(protocol.id)"
              :options="modeOptions"
              @update:model-value="setMode(protocol.id, String($event))"
            />
          </template>
          <Toggle
            size="sm"
            class="shrink-0"
            :model-value="modelValue.includes(protocol.id)"
            :data-protocol="protocol.id"
            :aria-label="protocol.name"
            @update:model-value="toggle(protocol.id)"
          />
        </div>
        <p
          v-if="
            protocol.id === 'openai_responses_websocket' &&
            isEnabled(protocol.id) &&
            fallbackMode(protocol.id) !== 'native'
          "
          class="mt-2 input-hint"
        >
          {{ t('admin.protocols.responsesWebSocketHTTPHint') }}
        </p>
        <!-- Conversion targets can exchange positions, so rows use index keys and disable movement animation to keep the edited slot stable. -->
        <RuleListEditor
          v-if="
            isEnabled(protocol.id) &&
            fallbackMode(protocol.id) === 'restricted' &&
            hasFallbackTargets(protocol.id)
          "
          class="mt-2 rounded-compact border border-gray-100 bg-gray-50/50 p-3 dark:border-dark-700 dark:bg-dark-800/40"
          :items="fallbacks?.[protocol.id] ?? []"
          :animated="false"
          add-placement="footer"
          :add-disabled="!remainingTarget(protocol.id)"
          @add="addTarget(protocol.id)"
          @remove="(index) => removeTarget(protocol.id, index)"
        >
          <template #row="{ item: target, index }">
            <div class="flex items-center gap-2">
              <span
                class="w-4 shrink-0 text-right text-xs tabular-nums text-gray-400 dark:text-dark-500"
                >{{ index + 1 }}</span
              >
              <Select
                class="min-w-0 flex-1"
                :aria-label="`${protocol.name}: ${t('admin.protocols.fallback')} ${index + 1}`"
                :model-value="target"
                :options="targetOptions(protocol.id)"
                @update:model-value="setTarget(protocol.id, index, String($event) as ProtocolID)"
              />
            </div>
          </template>
        </RuleListEditor>
      </div>
    </div>
    <div class="space-y-2 border-t border-gray-200 pt-6 dark:border-dark-600">
      <label :for="`${idPrefix}-image-policy`" class="input-label">{{
        t('admin.protocols.imagePolicy')
      }}</label>
      <p :id="`${idPrefix}-image-policy-scope`" class="input-hint">
        {{ t('admin.protocols.imagePolicyHint') }}
      </p>
      <Select
        :aria-label="t('admin.protocols.imagePolicy')"
        :aria-describedby="`${idPrefix}-image-policy-scope ${idPrefix}-image-policy-description`"
        :id="`${idPrefix}-image-policy`"
        :model-value="imagePolicy ?? 'inherit'"
        :options="imageOptions"
        @update:model-value="emit('update:imagePolicy', String($event) as CodexImageToolMode)"
      />
      <p :id="`${idPrefix}-image-policy-description`" class="input-hint">
        {{ t(`admin.protocols.imagePolicyOptions.${imagePolicy ?? 'inherit'}.description`) }}
      </p>
    </div>
  </SettingsSection>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import Select from '@/components/common/Select.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import {
  loadProtocolCatalog,
  protocolCatalog,
  protocolCatalogError,
  protocolCatalogLoading,
} from '@/api/admin/protocolCapabilities'
import type { ProtocolID } from '@/api/protocols.types'
import type { CodexImageToolMode } from '@/utils/codexImageToolMode'
import { setGroupClientProtocol } from '@/utils/groupClientProtocols'

// GroupClientProtocolSelector.vue edits a group's enabled client protocols, conversion targets,
// and image policy. Available protocols and target relationships come from the shared protocol
// catalog. Catalog failures remain visible through the shared error state, and retrying updates
// every consumer of that catalog.
//
// Conversion policy has three representations: an absent source key permits automatic
// selection, an empty target array requests native handling, and a populated array specifies
// target order. Editing copies the parent's arrays and records before emitting updates.
// Selecting a target already used by another row swaps those positions, retaining each target
// once in the ordered list.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = withDefaults(
  defineProps<{
    idPrefix?: string // field identifier prefix
    // modelValue contains enabled client protocol IDs. An empty array is a configured choice
    // that leaves every protocol disabled.
    modelValue: ProtocolID[]
    // fallbacks maps enabled sources to ordered conversion targets. Missing keys use automatic
    // conversion; empty arrays request native handling.
    fallbacks?: Partial<Record<ProtocolID, ProtocolID[]>>
    // imagePolicy selects the group Responses image handling rule. An omitted value displays
    // the inherited policy in this selector.
    imagePolicy?: CodexImageToolMode
  }>(),
  { idPrefix: 'group-protocol' },
)
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  'update:modelValue': [
    value: ProtocolID[], // replacement field value
  ] // replacement parent-owned field value
  'update:fallbacks': [
    value: Partial<Record<ProtocolID, ProtocolID[]>>, // replacement field value
  ] // replacement parent-owned field value
  'update:imagePolicy': [
    value: CodexImageToolMode, // replacement field value
  ] // replacement parent-owned field value
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()

// imageModes and imageOptions expose inheritance, enabled, disabled, and block choices with
// their translated image-policy labels.
const imageModes = ['inherit', 'enabled', 'disabled', 'block'] as const
const imageOptions = computed(() =>
  imageModes.map((value) => ({
    value,
    label: t(`admin.protocols.imagePolicyOptions.${value}.label`),
  })),
)

// retryCatalog requests the shared protocol catalog and consumes its rejection. The shared
// catalog error drives the retry interface, so this event handler does not create a second
// error display.
function retryCatalog() {
  // The shared loader publishes its own error state; consume this rejection so the selector
  // can render its retry control without an unhandled promise.
  void loadProtocolCatalog().catch(() => {})
}
retryCatalog()
// profile and protocols derive the group-supported protocol rows from the shared catalog,
// including the dedicated WebSocket label.
const profile = computed(() => protocolCatalog.value?.groups[0])
const protocols = computed(
  () =>
    protocolCatalog.value?.protocols
      .filter((protocol) => profile.value?.protocols.includes(protocol.id))
      .map((protocol) => ({
        ...protocol,
        name:
          protocol.id === 'openai_responses_websocket'
            ? t('admin.protocols.responsesWebSocket')
            : protocol.name,
      })) ?? [],
)
// isEnabled reports whether the parent selected the requested client protocol.
function isEnabled(id: ProtocolID) {
  return props.modelValue.includes(id)
}

// hasFallbackTargets reports whether the catalog offers conversion targets for this protocol.
function hasFallbackTargets(id: ProtocolID) {
  return !!profile.value?.fallback_targets[id]?.length
}
// targetOptions builds labels for the source protocol's allowed conversion targets. Missing
// catalog names display their protocol identifiers, and WebSocket-to-HTTP conversion uses the
// dedicated HTTP label.
function targetOptions(source: ProtocolID) {
  return (profile.value?.fallback_targets[source] ?? []).map((id) => ({
    value: id,
    label:
      source === 'openai_responses_websocket' && id === 'openai_responses'
        ? t('admin.protocols.responsesWebSocketHTTP')
        : (protocolCatalog.value?.protocols.find((protocol) => protocol.id === id)?.name ?? id),
  }))
}
// toggle emits the selected protocol list after applying the shared enablement rules.
function toggle(id: ProtocolID) {
  emit(
    'update:modelValue',
    setGroupClientProtocol(props.modelValue, id, !props.modelValue.includes(id)),
  )
}

// modeOptions represents absent, empty, and ordered target configurations as automatic, native,
// and restricted choices for each protocol.
const modeOptions = computed(() => [
  { value: 'auto', label: t('admin.protocols.auto') },
  { value: 'native', label: t('admin.protocols.nativeOnly') },
  { value: 'restricted', label: t('admin.protocols.restricted') },
])
// fallbackMode interprets one source protocol's conversion configuration for the selector. An
// absent key means automatic conversion, an empty list means native handling, and targets mean
// restricted conversion.
function fallbackMode(source: ProtocolID) {
  const targets = props.fallbacks?.[source]
  return targets === undefined ? 'auto' : targets.length ? 'restricted' : 'native'
}
// setMode emits a copied conversion record for the selected policy. Automatic mode removes the
// source key; restricted mode starts with its first catalog target, and native mode stores an
// empty array.
function setMode(source: ProtocolID, mode: string) {
  const next = { ...props.fallbacks }
  if (mode === 'auto') delete next[source]
  else
    next[source] =
      mode === 'restricted' ? (profile.value?.fallback_targets[source] ?? []).slice(0, 1) : []
  emit('update:fallbacks', next)
}
// remainingTarget returns the first catalog target missing from the source list. Undefined
// means that all available targets are selected or the catalog has no targets for this source.
function remainingTarget(source: ProtocolID) {
  return profile.value?.fallback_targets[source]?.find(
    (target) => !props.fallbacks?.[source]?.includes(target),
  )
}
// addTarget appends the first unselected conversion target and emits a copied record. Exhausted
// catalog targets leave the existing configuration unchanged, allowing the add action to be
// safely repeated.
function addTarget(source: ProtocolID) {
  const target = remainingTarget(source)
  if (target)
    emit('update:fallbacks', {
      ...props.fallbacks,
      [source]: [...(props.fallbacks?.[source] ?? []), target],
    })
}
// setTarget changes one conversion row while preserving the uniqueness of ordered targets. It
// copies the selected source list before modifying either position. When the new target already
// occupies another row, that row receives the replaced target so both remain represented.
//
// The emitted record contains the reordered list for this source and the parent's other source
// policies. Callers supply a valid row index from the rendered conversion editor.
function setTarget(source: ProtocolID, index: number, target: ProtocolID) {
  const targets = [...(props.fallbacks?.[source] ?? [])]
  const existing = targets.indexOf(target)
  // Swap the replaced value into the existing target slot so changing one row does not
  // introduce duplicate conversion targets.
  if (existing >= 0 && existing !== index) targets[existing] = targets[index]
  targets[index] = target
  emit('update:fallbacks', { ...props.fallbacks, [source]: targets })
}
// removeTarget emits the source target list after excluding the selected row.
function removeTarget(source: ProtocolID, index: number) {
  emit('update:fallbacks', {
    ...props.fallbacks,
    [source]: props.fallbacks?.[source]?.filter((_, i) => i !== index) ?? [],
  })
}
</script>

<style scoped>
/* Conversion selectors use 32px height to match the compact protocol rows containing their controls. */
.protocol-mode-select :deep(.input-trigger) {
  @apply min-h-8 px-3 py-1 text-xs;
}
</style>
