<template>
  <div>
    <label class="input-label">
      {{ t('admin.users.groups') }}
      <span class="font-normal text-gray-400">{{
        t('common.selectedCount', { count: modelValue.length })
      }}</span>
    </label>
    <div
      v-if="isSearchable"
      class="flex items-center gap-2 rounded-t-control border border-b-0 border-gray-200 bg-gray-50 px-3 py-2 dark:border-dark-600 dark:bg-dark-800"
    >
      <Icon name="search" size="sm" class="shrink-0 text-gray-400" />
      <input
        v-model="searchText"
        type="text"
        :placeholder="t('common.searchPlaceholder')"
        class="flex-1 bg-transparent text-sm text-gray-900 placeholder:text-gray-400 focus:outline-none dark:text-gray-100 dark:placeholder:text-dark-400"
      />
    </div>
    <div
      :class="[
        'grid max-h-32 grid-cols-2 gap-1 overflow-y-auto p-2',
        isSearchable
          ? 'rounded-b-control border border-t-0 border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-800'
          : 'rounded-control border border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-800',
      ]"
    >
      <label
        v-for="group in filteredGroups"
        :key="group.id"
        class="flex cursor-pointer items-center gap-2 rounded-compact px-2 py-1.5 transition-colors hover:bg-white dark:hover:bg-dark-700"
        :title="
          t('admin.groups.rateAndProviders', {
            rate: group.rate_multiplier,
            count: group.provider_count || 0,
          })
        "
      >
        <input
          type="checkbox"
          :value="group.id"
          :checked="modelValue.includes(group.id)"
          @change="handleChange(group.id, ($event.target as HTMLInputElement).checked)"
          class="h-3.5 w-3.5 shrink-0 rounded-compact border-gray-300 text-primary-500 focus:ring-primary-500 dark:border-dark-500"
        />
        <GroupBadge
          :name="group.name"
          :display-brand="group.display_brand"
          :rate-multiplier="group.rate_multiplier"
          class="min-w-0 flex-1"
        />
        <span
          v-if="group.status !== 'active'"
          class="shrink-0 rounded-compact bg-gray-200 px-1.5 py-0.5 text-xs font-medium text-gray-600 dark:bg-dark-600 dark:text-dark-200"
        >
          {{ t('common.inactive') }}
        </span>
        <span class="shrink-0 text-xs text-gray-400">{{ group.provider_count || 0 }}</span>
      </label>
      <div
        v-if="filteredGroups.length === 0"
        class="col-span-2 py-2 text-center text-sm text-gray-500 dark:text-gray-400"
      >
        {{ t('common.noGroupsAvailable') }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from './GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import type { AdminGroup } from '@/api/groups.types'

/**
 * GroupSelector.vue edits a parent owned list of group identifiers through checkboxes.
 * Search filters names and descriptions when enabled, including inactive groups, while
 * selection emits a replacement array so the owning form controls when group
 * associations are saved.
 */

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()

/**
 * Props describes the caller supplied values consumed by GroupSelector.
 */
interface Props {
  /**
   * modelValue belongs to the parent form; checkbox changes emit a replacement array
   * rather than mutating the supplied selection.
   */
  modelValue: number[]
  groups: AdminGroup[] /* available group records */
  /**
   * searchable accepts a boolean override or auto mode, which enables the search input
   * only when more than five groups exist.
   */
  searchable?: boolean | 'auto'
}

/**
 * props pairs available group records with parent selected identifiers and an optional
 * override for automatic search visibility.
 */
const props = withDefaults(defineProps<Props>(), {
  searchable: 'auto',
})
/**
 * emit returns a replacement array of group identifiers after checkbox changes so the
 * owning form can apply the selection.
 */
const emit = defineEmits<{
  'update:modelValue': [value: number[]] /* replacement parent controlled value */
}>()

/**
 * searchText belongs to this selector instance and filters displayed groups while
 * leaving the parent selection unchanged.
 */
const searchText = ref('')

/**
 * isSearchable enables automatic search when more than five groups are supplied, or
 * follows the caller boolean override.
 */
const isSearchable = computed(() => {
  /**
   * Automatic search appears after five choices to keep short group lists directly
   * selectable in the compact grid.
   */
  if (props.searchable === 'auto') return props.groups.length > 5
  return props.searchable
})

/**
 * filteredGroups searches names and descriptions case insensitively when search is
 * enabled, retaining the supplied group order.
 */
const filteredGroups = computed(() => {
  let result: AdminGroup[] = props.groups
  if (isSearchable.value && searchText.value) {
    const q = searchText.value.toLowerCase()
    result = result.filter(
      (g) => g.name.toLowerCase().includes(q) || g.description?.toLowerCase().includes(q),
    )
  }
  return result
})

/**
 * handleChange emits a replacement selection array after a checkbox change. Checking
 * appends the identifier; unchecking removes matching identifiers, and the caller
 * receives the update to save in its form.
 */
const handleChange = (groupId: number, checked: boolean) => {
  const newValue = checked
    ? [...props.modelValue, groupId]
    : props.modelValue.filter((id) => id !== groupId)
  emit('update:modelValue', newValue)
}
</script>
