<template>
  <SettingsSection>
    <SettingToggleRow
      :id="`${idPrefix}-models-list`"
      :model-value="state.enabled"
      :label="t('admin.groups.modelsList.title', { endpoint: '/v1/models' })"
      :hint="t('admin.groups.modelsList.hint', { endpoint: '/v1/models' })"
      setting="enabled"
      @update:model-value="emit('enabled', $event)"
    />
    <div
      v-if="state.enabled"
      class="overflow-hidden rounded-surface border border-gray-200 dark:border-dark-600"
    >
      <div
        v-if="!loading && state.items.length"
        class="flex flex-wrap items-center justify-between gap-2 border-b border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800"
      >
        <span class="text-xs text-gray-500 dark:text-gray-400">{{
          t('admin.groups.settings.selectedModels', {
            selected: selectedCount,
            total: state.items.length,
          })
        }}</span>
        <div class="flex gap-2">
          <button type="button" class="btn btn-secondary" @click="emit('selectAll')">
            {{ t('admin.groups.settings.selectAll') }}
          </button>
          <button type="button" class="btn btn-secondary" @click="emit('invert')">
            {{ t('admin.groups.settings.invertSelection') }}
          </button>
        </div>
      </div>
      <div class="max-h-64 divide-y divide-gray-200 overflow-y-auto dark:divide-dark-600">
        <p v-if="loading" class="input-hint p-4">
          {{ t('admin.groups.modelsList.loading') }}
        </p>
        <p v-else-if="!state.items.length" class="input-hint p-4">
          {{ t('admin.groups.modelsList.empty') }}
        </p>
        <div
          v-for="(item, index) in state.items"
          :key="item.id"
          class="flex items-center gap-2 px-4 py-3"
        >
          <span class="min-w-0 flex-1 break-all text-sm text-primary-900 dark:text-dark-50">{{
            item.id
          }}</span>
          <Toggle
            :model-value="item.selected"
            :aria-label="item.id"
            :data-model-visibility="item.id"
            size="md"
            class="shrink-0"
            @update:model-value="emit('select', item.id, $event)"
          />
          <div class="flex shrink-0">
            <button
              type="button"
              class="btn btn-ghost btn-icon"
              :disabled="index === 0"
              :aria-label="t('admin.groups.settings.moveUp', { name: item.id })"
              @click="emit('move', index, index - 1)"
            >
              <Icon name="arrowUp" size="sm" />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-icon"
              :disabled="index === state.items.length - 1"
              :aria-label="t('admin.groups.settings.moveDown', { name: item.id })"
              @click="emit('move', index, index + 1)"
            >
              <Icon name="arrowDown" size="sm" />
            </button>
          </div>
        </div>
      </div>
    </div>
  </SettingsSection>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import type { ModelsListState } from '@/components/admin/group/groupsModelsList'

// GroupModelsListFields.vue displays the group's custom model list, selection controls, and
// ordering actions. The page owns candidate reconciliation and persistence, while this
// component reads the supplied state and emits selection or movement requests for that draft.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  idPrefix: string // field identifier prefix
  // state belongs to the page and contains saved, candidate, and editable model selections.
  // Controls emit requests for the page to update it.
  state: ModelsListState
  loading: boolean // candidate loading flag
}>()

// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  enabled: [
    value: boolean, // replacement field value
  ] // enablement change notification
  select: [
    id: string, // selected model identifier
    value: boolean, // replacement field value
  ] // model selection change
  selectAll: [] // select every model
  invert: [] // invert model selections
  move: [
    from: number, // source list index
    to: number, // destination list index
  ] // source and destination indexes
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
// selectedCount derives the number of checked draft rows for the custom model list selection
// summary.
const selectedCount = computed(() => props.state.items.filter((item) => item.selected).length)
</script>
