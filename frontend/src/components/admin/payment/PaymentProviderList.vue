<template>
  <SettingsCard
    :title="t('admin.settings.payment.providerManagement')"
    :description="t('admin.settings.payment.providerManagementDesc')"
  >
    <template #actions>
      <div class="flex items-center gap-2">
        <button
          type="button"
          @click="emit('refresh')"
          :disabled="loading"
          class="btn btn-secondary btn-icon"
          :title="t('common.refresh')"
          :aria-label="t('common.refresh')"
        >
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        </button>
        <button
          type="button"
          @click="emit('create')"
          :disabled="!canCreate"
          :class="
            canCreate
              ? 'btn btn-primary btn-sm h-9'
              : 'btn btn-secondary btn-sm h-9 cursor-not-allowed opacity-50'
          "
        >
          {{ t('admin.settings.payment.createProvider') }}
        </button>
      </div>
    </template>

    <div>
      <ContentSkeleton v-if="loading && !providers.length" variant="list" :rows="3" class="py-4" />

      <VueDraggable
        v-if="providers.length"
        v-model="localProviders"
        :animation="200"
        handle=".drag-handle"
        class="space-y-3"
        @end="onDragEnd"
      >
        <div v-for="p in localProviders" :key="p.id" class="flex items-start gap-2">
          <div
            class="drag-handle mt-3 flex cursor-grab items-center text-gray-300 hover:text-gray-500 active:cursor-grabbing dark:text-dark-600 dark:hover:text-dark-400"
          >
            <Icon name="grip" size="md" class="h-5 w-5" />
          </div>
          <div class="min-w-0 flex-1">
            <ProviderCard
              :provider="p"
              :enabled="isEnabled(p.provider_key)"
              :available-types="getTypes(p.provider_key)"
              :updating="isUpdating(p.id)"
              @toggle-field="(field) => emit('toggleField', p, field)"
              @toggle-type="(type) => emit('toggleType', p, type)"
              @edit="emit('edit', p)"
              @delete="emit('delete', p)"
            />
          </div>
        </div>
      </VueDraggable>

      <div v-else-if="!loading" class="py-6 text-center">
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{
            canCreate
              ? t('admin.settings.payment.noProviders')
              : t('admin.settings.payment.enableTypesFirst')
          }}
        </p>
        <button
          type="button"
          v-if="canCreate"
          @click="emit('create')"
          class="btn btn-primary btn-sm h-9 mt-2"
        >
          {{ t('admin.settings.payment.createProvider') }}
        </button>
      </div>
    </div>
  </SettingsCard>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { VueDraggable } from 'vue-draggable-plus'
import Icon from '@/components/icons/Icon.vue'
import SettingsCard from '@/components/common/settings/SettingsCard.vue'
import ProviderCard from './ProviderCard.vue'
import type { ProviderInstance } from '@/api/payment.types'
import type { TypeOption } from './providerConfig'
import { getAvailableTypes } from './providerConfig'

// PaymentProviderList.vue renders draggable provider cards and forwards administrative
// actions to its parent. A local array owns the current drag ordering, while provider
// updates remain parent-owned and completion is reflected through replacement props and
// per-provider busy identifiers.

// props supplies parent-owned records and presentation state consumed by this component
// and its event handlers.
const props = defineProps<{
  providers: ProviderInstance[] /* ordered payment provider instances */
  loading: boolean /* parent data loading flag */
  canCreate: boolean /* provider creation availability flag */
  enabledPaymentTypes: string[] /* globally enabled provider keys */
  allPaymentTypes: TypeOption[] /* available payment method options */
  redirectLabel: string /* redirect option display label */
  updatingProviderIds?: number[] /* provider instances currently updating */
}>()

// emit defines requests and completion signals that the parent uses to coordinate this
// component with persistence.
const emit = defineEmits<{
  refresh: [] /* provider refresh request event */
  create: [] /* provider creation request event */
  edit: [provider: ProviderInstance] /* provider edit request event */
  delete: [provider: ProviderInstance] /* provider deletion request event */
  toggleField: [
    provider: ProviderInstance,
    field: 'enabled' | 'refund_enabled' | 'allow_user_refund',
  ] /* provider boolean toggle request */
  toggleType: [provider: ProviderInstance, type: string] /* payment method toggle request */
  reorder: [
    providers: {
      id: number // persistent record identifier
      sort_order: number // persisted zero-based drag position
    }[],
  ] /* provider sort-order update request */
}>()

// t resolves the locale labels used by payment controls, validation messages, and
// displayed order information.
const { t } = useI18n()

// localProviders owns array ordering during drag operations and is rebuilt from normalized
// parent records whenever the providers prop changes.
const localProviders = ref<ProviderInstance[]>([])

watch(
  () => props.providers,
  (val) => {
    localProviders.value = val.map(normalizeProvider)
  },
  { immediate: true },
)

// normalizeProvider creates a shallow provider copy for list rendering. Null or invalid
// supported_types becomes an empty array; an existing array remains shared because cards
// request changes through emitted events.
function normalizeProvider(provider: ProviderInstance): ProviderInstance {
  return {
    ...provider,
    // API responses may contain null supported_types; an empty list lets cards render and
    // evaluate selection checks consistently for those records.
    supported_types: Array.isArray(provider.supported_types) ? provider.supported_types : [],
  }
}

// onDragEnd emits provider identifiers with positions from the local drag ordering. The
// parent persists these sort orders and supplies subsequent provider props; this handler
// does not await persistence.
function onDragEnd() {
  const updates = localProviders.value.map((p, idx) => ({
    id: p.id,
    sort_order: idx,
  }))
  emit('reorder', updates)
}

// isEnabled reports whether the parent enabled this provider key globally.
function isEnabled(providerKey: string): boolean {
  return props.enabledPaymentTypes.includes(providerKey)
}

// isUpdating reports whether the parent marks this provider instance as busy. An omitted
// updating-ID list means no per-provider update indicator, allowing callers that do not
// track concurrent updates.
function isUpdating(providerId: number): boolean {
  return props.updatingProviderIds?.includes(providerId) ?? false
}

// getTypes resolves provider method options and translates labels missing from the shared
// options. Unknown translations retain the raw method code so provider-specific choices
// remain identifiable in the list.
function getTypes(providerKey: string): TypeOption[] {
  return getAvailableTypes(providerKey, props.allPaymentTypes, props.redirectLabel).map((opt) =>
    // Provider-private method codes may lack shared display options; translation with a
    // raw-code default keeps each displayed toggle identifiable.
    opt.label === opt.value ? { ...opt, label: t(`payment.methods.${opt.value}`, opt.value) } : opt,
  )
}
</script>
