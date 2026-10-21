<template>
  <div class="flex min-w-0 flex-nowrap items-center gap-2">
    <SearchInput
      :model-value="searchQuery"
      :placeholder="t('admin.providers.searchProviders')"
      class="min-w-0 flex-1 sm:flex-none sm:w-56 lg:w-52"
      @update:model-value="$emit('update:searchQuery', $event)"
      @search="$emit('change')"
    />

    <FilterDropdown
      :active-count="activeFilterCount"
      :columns="2"
      :description="t('admin.providers.filterHint')"
      @reset="clearFilters"
    >
      <FilterField :label="t('admin.providers.columns.platform')">
        <Select
          :model-value="filters.platform"
          :options="pOpts"
          @update:model-value="updatePlatform"
          @change="$emit('change')"
        />
      </FilterField>
      <FilterField :label="t('admin.providers.columns.type')">
        <Select
          :model-value="filters.type"
          :options="tOpts"
          @update:model-value="updateType"
          @change="$emit('change')"
        />
      </FilterField>
      <FilterField :label="t('admin.providers.columns.status')">
        <Select
          :model-value="filters.status"
          :options="sOpts"
          @update:model-value="updateStatus"
          @change="$emit('change')"
        />
      </FilterField>
      <FilterField :label="t('admin.providers.privacyFilter')">
        <Select
          :model-value="filters.privacy_mode"
          :options="privacyOpts"
          @update:model-value="updatePrivacyMode"
          @change="$emit('change')"
        />
      </FilterField>
      <FilterField :label="t('admin.providers.columns.groups')" full>
        <Select
          :model-value="filters.group"
          :options="gOpts"
          searchable
          @update:model-value="updateGroup"
          @change="$emit('change')"
        />
      </FilterField>
    </FilterDropdown>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import FilterDropdown from '@/components/common/FilterDropdown.vue'
import FilterField from '@/components/common/FilterField.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import type { AdminGroup } from '@/api/groups.types'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'

/**
 * ProviderTableFilters.vue renders provider search and five filter controls using parent-owned
 * values. Individual controls emit copied filter records, while clearing resets the recognized
 * keys and emits change. Group choices retain disabled groups with a status suffix so existing
 * provider assignments remain searchable.
 */

/**
 * props supplies controlled search and filter values, with optional groups used to build active
 * and disabled group choices.
 */
const props = defineProps<{
  searchQuery: string // parent-owned provider search text
  filters: Record<string, any> // parent-owned provider filter values
  groups?: AdminGroup[] // available administrator group records
}>()
/**
 * emit publishes search or copied filter updates, and change asks the parent to apply the
 * current filter selection.
 */
const emit = defineEmits(['update:searchQuery', 'update:filters', 'change'])
/**
 * The translator labels filter controls and empty choices, while filterKeys defines which
 * fields count toward the active-filter badge.
 */
const { t } = useI18n()

const filterKeys = ['platform', 'type', 'status', 'privacy_mode', 'group'] as const

/**
 * activeFilterCount counts nonempty normalized values among the five recognized filters,
 * excluding search text and unrelated parent filter fields.
 */
const activeFilterCount = computed(
  () => filterKeys.filter((key) => String(props.filters?.[key] ?? '').trim() !== '').length,
)

/**
 * updatePlatform emits a copied filter record with the selected provider platform.
 */
const updatePlatform = (value: string | number | boolean | null) => {
  emit('update:filters', { ...props.filters, platform: value })
}
/**
 * updateType emits a copied filter record with the selected authentication type.
 */
const updateType = (value: string | number | boolean | null) => {
  emit('update:filters', { ...props.filters, type: value })
}
/**
 * updateStatus emits a copied filter record with the selected provider status.
 */
const updateStatus = (value: string | number | boolean | null) => {
  emit('update:filters', { ...props.filters, status: value })
}
/**
 * updatePrivacyMode emits a copied filter record with the selected privacy state.
 */
const updatePrivacyMode = (value: string | number | boolean | null) => {
  emit('update:filters', { ...props.filters, privacy_mode: value })
}
/**
 * updateGroup emits a copied filter record with the selected group value.
 */
const updateGroup = (value: string | number | boolean | null) => {
  emit('update:filters', { ...props.filters, group: value })
}

/**
 * clearFilters clears the five recognized filter keys while retaining other parent fields. It
 * emits the replacement record before change so the parent can reload using the cleared values.
 */
const clearFilters = () => {
  const nextFilters = { ...props.filters }
  for (const key of filterKeys) nextFilters[key] = ''
  emit('update:filters', nextFilters)
  emit('change')
}

/**
 * Filter option lists include an empty all-values choice, platform or authentication values,
 * and status or privacy-specific sentinel selections.
 */
const pOpts = computed(() => [
  { value: '', label: t('admin.providers.allPlatforms') },
  ...CONCRETE_PLATFORM_OPTIONS,
])
const tOpts = computed(() => [
  { value: '', label: t('admin.providers.allTypes') },
  { value: 'oauth', label: t('admin.providers.oauthType') },
  { value: 'setup-token', label: t('admin.providers.setupToken') },
  { value: 'apikey', label: t('admin.providers.apiKey') },
  { value: 'service_account', label: t('admin.providers.serviceAccount') },
  { value: 'bedrock', label: 'AWS Bedrock' },
  { value: 'cosy', label: t('admin.providers.types.qoderCosy') },
])
const sOpts = computed(() => [
  { value: '', label: t('admin.providers.allStatus') },
  { value: 'active', label: t('admin.providers.status.active') },
  { value: 'inactive', label: t('admin.providers.status.inactive') },
  { value: 'error', label: t('admin.providers.status.error') },
  { value: 'rate_limited', label: t('admin.providers.status.rateLimited') },
  { value: 'temp_unschedulable', label: t('admin.providers.status.tempUnschedulable') },
  { value: 'unschedulable', label: t('admin.providers.status.unschedulable') },
])
const privacyOpts = computed(() => [
  { value: '', label: t('admin.providers.allPrivacyModes') },
  { value: '__unset__', label: t('admin.providers.privacyUnset') },
  { value: 'training_off', label: 'Privacy' },
  { value: 'training_set_cf_blocked', label: 'CF' },
  { value: 'training_set_failed', label: 'Fail' },
])
/**
 * gOpts includes all groups, the ungrouped sentinel, and stringified group identifiers,
 * retaining inactive groups with a translated status suffix.
 */
const gOpts = computed(() => [
  { value: '', label: t('admin.providers.allGroups') },
  { value: 'ungrouped', label: t('admin.providers.ungroupedGroup') },
  ...(props.groups || []).map((g) => ({
    value: String(g.id),
    // Keep inactive groups selectable so administrators can still find providers assigned to
    // those groups while seeing their disabled status.
    label: g.status === 'active' ? g.name : `${g.name} (${t('common.inactive')})`,
  })),
])
</script>
