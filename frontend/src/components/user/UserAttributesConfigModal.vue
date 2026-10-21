<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.attributes.title')"
    width="wide"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <!-- Header with Add Button -->
      <div class="flex items-center justify-between">
        <p class="text-sm text-gray-500 dark:text-dark-400">
          {{ t('admin.users.attributes.description') }}
        </p>
        <button @click="openCreateModal" class="btn btn-primary btn-sm">
          <Icon name="plus" size="sm" class="mr-1.5" :stroke-width="2" />
          {{ t('admin.users.attributes.addAttribute') }}
        </button>
      </div>

      <!-- Loading State -->
      <ContentSkeleton v-if="loading" variant="list" :rows="3" class="py-4" />

      <!-- Empty State -->
      <div v-else-if="attributes.length === 0" class="py-12 text-center">
        <Icon name="tag" size="md" class="mx-auto h-12 w-12 text-gray-400" />
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
          {{ t('admin.users.attributes.noAttributes') }}
        </p>
        <p class="text-xs text-gray-400 dark:text-dark-500">
          {{ t('admin.users.attributes.noAttributesHint') }}
        </p>
      </div>

      <!-- Attributes List -->
      <div v-else class="max-h-96 space-y-2 overflow-y-auto">
        <div
          v-for="attr in attributes"
          :key="attr.id"
          class="flex items-center gap-3 rounded-surface border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-800"
        >
          <!-- Reorder handle appearance; this template registers no drag handlers. -->
          <div
            class="cursor-move text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
            :title="t('admin.users.attributes.dragToReorder')"
          >
            <Icon name="menu" size="md" />
          </div>

          <!-- Attribute Info -->
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="font-medium text-gray-900 dark:text-white">{{ attr.name }}</span>
              <span
                class="rounded-compact bg-gray-100 px-1.5 py-0.5 font-mono text-xs text-gray-500 dark:bg-dark-700 dark:text-dark-400"
              >
                {{ attr.key }}
              </span>
              <span v-if="attr.required" class="badge badge-danger text-xs">
                {{ t('admin.users.attributes.required') }}
              </span>
              <span v-if="!attr.enabled" class="badge badge-gray text-xs">
                {{ t('common.disabled') }}
              </span>
            </div>
            <div class="mt-0.5 flex items-center gap-2 text-xs text-gray-500 dark:text-dark-400">
              <span class="badge badge-gray">{{
                t(`admin.users.attributes.types.${attr.type}`)
              }}</span>
              <span v-if="attr.description" class="truncate">{{ attr.description }}</span>
            </div>
          </div>

          <!-- Actions -->
          <div class="flex items-center gap-1">
            <button
              @click="openEditModal(attr)"
              class="rounded-control p-1.5 text-gray-500 hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
              :title="t('common.edit')"
            >
              <Icon name="edit" size="sm" />
            </button>
            <button
              @click="confirmDelete(attr)"
              class="rounded-control p-1.5 text-gray-500 hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
              :title="t('common.delete')"
            >
              <Icon name="trash" size="sm" />
            </button>
          </div>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button @click="emit('close')" class="btn btn-secondary">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <!-- Create/Edit Attribute Modal -->
  <BaseDialog
    :show="showEditModal"
    :title="
      editingAttribute
        ? t('admin.users.attributes.editAttribute')
        : t('admin.users.attributes.addAttribute')
    "
    width="normal"
    @close="closeEditModal"
  >
    <form id="attribute-form" @submit.prevent="handleSave" class="space-y-4">
      <!-- Key -->
      <div>
        <label class="input-label">{{ t('admin.users.attributes.key') }}</label>
        <input
          v-model="form.key"
          type="text"
          required
          pattern="^[a-zA-Z][a-zA-Z0-9_]*$"
          class="input font-mono"
          :placeholder="t('admin.users.attributes.keyHint')"
          :disabled="!!editingAttribute"
        />
        <p class="input-hint">{{ t('admin.users.attributes.keyHint') }}</p>
      </div>

      <!-- Name -->
      <div>
        <label class="input-label">{{ t('admin.users.attributes.name') }}</label>
        <input
          v-model="form.name"
          type="text"
          required
          class="input"
          :placeholder="t('admin.users.attributes.nameHint')"
        />
      </div>

      <!-- Type -->
      <div>
        <label class="input-label">{{ t('admin.users.attributes.type') }}</label>
        <Select
          v-model="form.type"
          :options="
            attributeTypes.map((type) => ({
              value: type,
              label: t(`admin.users.attributes.types.${type}`),
            }))
          "
        />
      </div>

      <!-- Options (for select/multi_select) -->
      <RuleListEditor
        v-if="form.type === 'select' || form.type === 'multi_select'"
        :items="form.options"
        :title="t('admin.users.attributes.options')"
        :add-label="t('admin.users.attributes.addOption')"
        test-id="user-attribute-options"
        @add="addOption"
        @remove="removeOption"
      >
        <template #row="{ item: option }">
          <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
            <input
              v-model="option.value"
              type="text"
              class="input min-w-0 font-mono text-sm"
              :placeholder="t('admin.users.attributes.optionValue')"
              required
            />
            <input
              v-model="option.label"
              type="text"
              class="input min-w-0 text-sm"
              :placeholder="t('admin.users.attributes.optionLabel')"
              required
            />
          </div>
        </template>
      </RuleListEditor>

      <!-- Description -->
      <div>
        <label class="input-label">{{ t('admin.users.attributes.fieldDescription') }}</label>
        <input
          v-model="form.description"
          type="text"
          class="input"
          :placeholder="t('admin.users.attributes.fieldDescriptionHint')"
        />
      </div>

      <!-- Placeholder -->
      <div>
        <label class="input-label">{{ t('admin.users.attributes.placeholder') }}</label>
        <input
          v-model="form.placeholder"
          type="text"
          class="input"
          :placeholder="t('admin.users.attributes.placeholderHint')"
        />
      </div>

      <!-- Required & Enabled -->
      <div class="flex items-center gap-6">
        <label class="flex items-center gap-2">
          <input
            v-model="form.required"
            type="checkbox"
            class="h-4 w-4 rounded-compact border-gray-300 text-primary-600"
          />
          <span class="text-sm text-gray-700 dark:text-gray-300">{{
            t('admin.users.attributes.required')
          }}</span>
        </label>
        <label class="flex items-center gap-2">
          <input
            v-model="form.enabled"
            type="checkbox"
            class="h-4 w-4 rounded-compact border-gray-300 text-primary-600"
          />
          <span class="text-sm text-gray-700 dark:text-gray-300">{{
            t('admin.users.attributes.enabled')
          }}</span>
        </label>
      </div>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="closeEditModal" type="button" class="btn btn-secondary">
          {{ t('common.cancel') }}
        </button>
        <button type="submit" form="attribute-form" :disabled="saving" class="btn btn-primary">
          <Icon
            name="loader"
            size="sm"
            :animate-on-hover="false"
            v-if="saving"
            class="-ml-1 mr-2 h-4 w-4 animate-spin"
          />
          {{
            saving ? t('common.saving') : editingAttribute ? t('common.update') : t('common.create')
          }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <!-- Delete Confirmation -->
  <ConfirmDialog
    :show="showDeleteDialog"
    :title="t('admin.users.attributes.deleteAttribute')"
    :message="t('admin.users.attributes.deleteConfirm', { name: deletingAttribute?.name })"
    :confirm-text="t('common.delete')"
    :cancel-text="t('common.cancel')"
    :danger="true"
    @confirm="handleDelete"
    @cancel="showDeleteDialog = false"
  />
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { ref, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type {
  UserAttributeDefinition,
  UserAttributeType,
  UserAttributeOption,
} from '@/api/admin/userAttributes.types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'

// UserAttributesConfigModal.vue manages administrator-defined user attributes and their
// selectable options. A separate edit draft holds create or update values, and select
// types require at least one option. Saving or deleting refreshes the definition list
// after the corresponding server mutation succeeds.

// The translation and notification services provide attribute configuration labels and
// validation or persistence feedback for the definition editor.
const { t } = useI18n()
const appStore = useAppStore()

// Props supplies the parent-controlled visibility of the attribute configuration
// dialog.
interface Props {
  show: boolean // Parent-controlled dialog visibility.
}

// Emits lets the parent close the attribute configuration dialog on request.
interface Emits {
  (e: 'close'): void // Requests parent-owned dialog closure.
}

// props and emit connect parent-controlled visibility with the close request from the
// attribute configuration dialog's footer and dismissal handling.
const props = defineProps<Props>()
const emit = defineEmits<Emits>()

// attributeTypes lists the supported definition input kinds used by the attribute-type
// selector when creating or editing a definition.
const attributeTypes: UserAttributeType[] = [
  'text',
  'textarea',
  'number',
  'email',
  'url',
  'date',
  'select',
  'multi_select',
]

// loading, saving, and attributes hold list feedback, mutation feedback, and the loaded
// definitions displayed by the configuration dialog.
const loading = ref(false)
const saving = ref(false)
const attributes = ref<UserAttributeDefinition[]>([])
// showEditModal and editingAttribute identify the create or update draft, while
// showDeleteDialog and deletingAttribute hold the separate deletion confirmation
// target.
const showEditModal = ref(false)
const showDeleteDialog = ref(false)
const editingAttribute = ref<UserAttributeDefinition | null>(null)
const deletingAttribute = ref<UserAttributeDefinition | null>(null)

// form contains an editable definition and option rows; openEditModal copies each
// option so typing in the draft leaves listed option objects untouched.
const form = reactive({
  key: '', // Stable attribute identifier.
  name: '', // Displayed attribute name.
  type: 'text' as UserAttributeType, // Attribute input kind.
  description: '', // Attribute help text.
  placeholder: '', // Empty-input prompt text.
  required: false, // Mandatory attribute flag.
  enabled: true, // Attribute availability flag.
  options: [] as UserAttributeOption[], // Selectable attribute choices.
})

// loadAttributes retrieves the complete attribute-definition list for configuration.
// The existing list remains available when loading fails, and a server detail or
// localized error explains the failure before feedback clears.
const loadAttributes = async () => {
  loading.value = true
  try {
    attributes.value = await adminAPI.userAttributes.listDefinitions()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.attributes.failedToLoad'))
  } finally {
    loading.value = false
  }
}

// openCreateModal resets the definition draft and opens the attribute creation editor.
const openCreateModal = () => {
  editingAttribute.value = null
  form.key = ''
  form.name = ''
  form.type = 'text'
  form.description = ''
  form.placeholder = ''
  form.required = false
  form.enabled = true
  form.options = []
  showEditModal.value = true
}

// openEditModal fills the draft from an existing definition and copies each option
// object. Optional description, placeholder, and options become empty editor values so
// local edits remain usable for incomplete definitions.
const openEditModal = (attr: UserAttributeDefinition) => {
  editingAttribute.value = attr
  form.key = attr.key
  form.name = attr.name
  form.type = attr.type
  form.description = attr.description || ''
  form.placeholder = attr.placeholder || ''
  form.required = attr.required
  form.enabled = attr.enabled
  form.options = attr.options ? attr.options.map((opt) => ({ ...opt })) : []
  showEditModal.value = true
}

// closeEditModal hides the definition editor and clears its existing-definition
// selection.
const closeEditModal = () => {
  showEditModal.value = false
  editingAttribute.value = null
}

// addOption appends an empty value-label pair to the definition's option draft.
const addOption = () => {
  form.options.push({
    value: '', // Stored option value.
    label: '', // Displayed option label.
  })
}

// removeOption removes the indexed selectable option from the definition draft.
const removeOption = (index: number) => {
  form.options.splice(index, 1)
}

// handleSave validates the definition key, name, and required select options before
// creating or updating the definition. Empty description and placeholder values are
// omitted, and option arrays are sent only for select types. Success closes the editor
// and starts a list refresh; failures retain the draft for correction.
const handleSave = async () => {
  if (!form.key.trim()) {
    appStore.showError(t('admin.users.attributes.keyRequired'))
    return
  }
  if (!form.name.trim()) {
    appStore.showError(t('admin.users.attributes.nameRequired'))
    return
  }
  if ((form.type === 'select' || form.type === 'multi_select') && form.options.length === 0) {
    appStore.showError(t('admin.users.attributes.optionsRequired'))
    return
  }
  saving.value = true
  try {
    const data = {
      key: form.key, // Stable attribute identifier.
      name: form.name, // Displayed attribute name.
      type: form.type, // Attribute input kind.
      description: form.description || undefined, // Attribute help text.
      placeholder: form.placeholder || undefined, // Empty-input prompt text.
      required: form.required, // Mandatory attribute flag.
      enabled: form.enabled, // Attribute availability flag.
      options: form.type === 'select' || form.type === 'multi_select' ? form.options : undefined, // Selectable attribute choices.
    }

    if (editingAttribute.value) {
      await adminAPI.userAttributes.updateDefinition(editingAttribute.value.id, data)
      appStore.showSuccess(t('admin.users.attributes.updated'))
    } else {
      await adminAPI.userAttributes.createDefinition(data)
      appStore.showSuccess(t('admin.users.attributes.created'))
    }

    // Close the completed editor and start reloading definitions after the mutation
    // succeeds, so the list can reflect the stored create or update result.
    closeEditModal()
    loadAttributes()
  } catch (error: any) {
    const msg = editingAttribute.value
      ? t('admin.users.attributes.failedToUpdate')
      : t('admin.users.attributes.failedToCreate')
    appStore.showError(error.response?.data?.detail || msg)
  } finally {
    saving.value = false
  }
}

// confirmDelete selects the attribute definition and opens its deletion confirmation
// dialog.
const confirmDelete = (attr: UserAttributeDefinition) => {
  deletingAttribute.value = attr
  showDeleteDialog.value = true
}

// handleDelete deletes the selected definition and starts a refreshed list after
// success. Missing selection stops the operation; failure reports a server detail or
// localized error and leaves the confirmation target available.
const handleDelete = async () => {
  if (!deletingAttribute.value) return

  try {
    // Await deletion before closing confirmation and refreshing definitions, so a
    // failed request retains its selected target for another administrator attempt.
    await adminAPI.userAttributes.deleteDefinition(deletingAttribute.value.id)
    appStore.showSuccess(t('admin.users.attributes.deleted'))
    showDeleteDialog.value = false
    deletingAttribute.value = null
    loadAttributes()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.attributes.failedToDelete'))
  }
}

// Reload definitions each time the configuration dialog opens so edits made elsewhere
// can appear when the administrator returns to this panel.
watch(
  () => props.show,
  (isShow) => {
    if (isShow) {
      loadAttributes()
    }
  },
)
</script>
