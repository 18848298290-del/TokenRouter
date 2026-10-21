<template>
  <BaseDialog
    :show="show"
    :title="t('admin.errorPassthrough.title')"
    width="extra-wide"
    @close="$emit('close')"
  >
    <div class="space-y-4">
      <!-- Header -->
      <div class="flex items-center justify-between">
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.errorPassthrough.description') }}
        </p>
        <button @click="showCreateModal = true" class="btn btn-primary btn-sm">
          <Icon name="plus" size="sm" class="mr-1" />
          {{ t('admin.errorPassthrough.createRule') }}
        </button>
      </div>

      <!-- Rules Table -->

      <EmptyState
        v-if="!loading && rules.length === 0"
        :title="t('admin.errorPassthrough.noRules')"
        :description="t('admin.errorPassthrough.createFirstRule')"
      >
        <template #icon>
          <Icon name="shield" class="empty-state-icon h-10 w-10" />
        </template>
      </EmptyState>

      <div
        v-else
        class="max-h-96 overflow-auto rounded-control border border-gray-200 dark:border-dark-600"
      >
        <table
          :aria-busy="loading"
          class="min-w-full divide-y divide-gray-200 dark:divide-dark-700"
        >
          <thead class="sticky top-0 bg-gray-50 dark:bg-dark-700">
            <tr>
              <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.errorPassthrough.columns.priority') }}
              </th>
              <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.errorPassthrough.columns.name') }}
              </th>
              <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.errorPassthrough.columns.conditions') }}
              </th>
              <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.errorPassthrough.columns.platforms') }}
              </th>
              <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.errorPassthrough.columns.behavior') }}
              </th>
              <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.errorPassthrough.columns.status') }}
              </th>
              <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.errorPassthrough.columns.actions') }}
              </th>
            </tr>
          </thead>
          <TableSkeletonBody v-if="loading" :columns="7" cell-class="px-3 py-2" />
          <tbody
            v-else
            class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-800"
          >
            <tr
              v-for="rule in rules"
              :key="rule.id"
              class="hover:bg-gray-50 dark:hover:bg-dark-700"
            >
              <td class="whitespace-nowrap px-3 py-2">
                <span
                  class="inline-flex h-5 w-5 items-center justify-center rounded-compact bg-gray-100 text-xs font-medium text-gray-700 dark:bg-dark-600 dark:text-gray-300"
                >
                  {{ rule.priority }}
                </span>
              </td>
              <td class="px-3 py-2">
                <div class="font-medium text-gray-900 dark:text-white text-sm">{{ rule.name }}</div>
                <div
                  v-if="rule.description"
                  class="mt-0.5 text-xs text-gray-500 dark:text-gray-400 max-w-xs truncate"
                >
                  {{ rule.description }}
                </div>
              </td>
              <td class="px-3 py-2">
                <div class="flex flex-wrap gap-1 max-w-48">
                  <span
                    v-for="code in rule.error_codes.slice(0, 3)"
                    :key="code"
                    class="badge badge-danger text-xs"
                  >
                    {{ code }}
                  </span>
                  <span v-if="rule.error_codes.length > 3" class="text-xs text-gray-500">
                    +{{ rule.error_codes.length - 3 }}
                  </span>
                  <span
                    v-for="keyword in rule.keywords.slice(0, 1)"
                    :key="keyword"
                    class="badge badge-gray text-xs"
                  >
                    "{{ keyword.length > 10 ? keyword.substring(0, 10) + '...' : keyword }}"
                  </span>
                  <span v-if="rule.keywords.length > 1" class="text-xs text-gray-500">
                    +{{ rule.keywords.length - 1 }}
                  </span>
                </div>
                <div class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.errorPassthrough.matchMode.' + rule.match_mode) }}
                </div>
              </td>
              <td class="px-3 py-2">
                <div
                  v-if="rule.platforms.length === 0"
                  class="text-xs text-gray-500 dark:text-gray-400"
                >
                  {{ t('admin.errorPassthrough.allPlatforms') }}
                </div>
                <div v-else class="flex flex-wrap gap-1">
                  <span
                    v-for="platform in rule.platforms.slice(0, 2)"
                    :key="platform"
                    class="badge badge-primary text-xs"
                  >
                    {{ platform }}
                  </span>
                  <span v-if="rule.platforms.length > 2" class="text-xs text-gray-500">
                    +{{ rule.platforms.length - 2 }}
                  </span>
                </div>
              </td>
              <td class="px-3 py-2">
                <div class="text-xs space-y-0.5">
                  <div class="flex items-center gap-1">
                    <Icon
                      :name="rule.passthrough_code ? 'checkCircle' : 'xCircle'"
                      size="xs"
                      :class="rule.passthrough_code ? 'text-green-500' : 'text-gray-400'"
                    />
                    <span class="text-gray-600 dark:text-gray-400">
                      {{ t('admin.errorPassthrough.code') }}:
                      {{
                        rule.passthrough_code
                          ? t('admin.errorPassthrough.passthrough')
                          : rule.response_code || '-'
                      }}
                    </span>
                  </div>
                  <div class="flex items-center gap-1">
                    <Icon
                      :name="rule.passthrough_body ? 'checkCircle' : 'xCircle'"
                      size="xs"
                      :class="rule.passthrough_body ? 'text-green-500' : 'text-gray-400'"
                    />
                    <span class="text-gray-600 dark:text-gray-400">
                      {{ t('admin.errorPassthrough.body') }}:
                      {{
                        rule.passthrough_body
                          ? t('admin.errorPassthrough.passthrough')
                          : t('admin.errorPassthrough.custom')
                      }}
                    </span>
                  </div>
                  <div v-if="rule.skip_monitoring" class="flex items-center gap-1">
                    <Icon
                      name="checkCircle"
                      size="xs"
                      class="text-yellow-500"
                      :animate-on-hover="false"
                    />
                    <span class="text-gray-600 dark:text-gray-400">
                      {{ t('admin.errorPassthrough.skipMonitoring') }}
                    </span>
                  </div>
                </div>
              </td>
              <td class="px-3 py-2">
                <!-- The toggle waits for persistence through toggleEnabled before changing the rule value, keeping rejected writes from appearing saved in the list. -->
                <Toggle
                  :model-value="rule.enabled"
                  size="sm"
                  off-tone="soft"
                  @update:model-value="toggleEnabled(rule)"
                />
              </td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1">
                  <button
                    @click="handleEdit(rule)"
                    class="p-1 text-gray-500 hover:text-primary-600 dark:hover:text-primary-400"
                    :title="t('common.edit')"
                  >
                    <Icon name="edit" size="sm" />
                  </button>
                  <button
                    @click="handleDelete(rule)"
                    class="p-1 text-gray-500 hover:text-red-600 dark:hover:text-red-400"
                    :title="t('common.delete')"
                  >
                    <Icon name="trash" size="sm" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button @click="$emit('close')" class="btn btn-secondary">
          {{ t('common.close') }}
        </button>
      </div>
    </template>

    <!-- Create/Edit Modal -->
    <BaseDialog
      :show="showCreateModal || showEditModal"
      :title="
        showEditModal
          ? t('admin.errorPassthrough.editRule')
          : t('admin.errorPassthrough.createRule')
      "
      width="wide"
      @close="closeFormModal"
    >
      <form @submit.prevent="handleSubmit" class="space-y-4">
        <!-- Basic Info -->
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="input-label">{{ t('admin.errorPassthrough.form.name') }}</label>
            <input
              v-model="form.name"
              type="text"
              required
              class="input"
              :placeholder="t('admin.errorPassthrough.form.namePlaceholder')"
            />
          </div>
          <div>
            <label class="input-label">{{ t('admin.errorPassthrough.form.priority') }}</label>
            <input v-model.number="form.priority" type="number" min="0" class="input" />
            <p class="input-hint">{{ t('admin.errorPassthrough.form.priorityHint') }}</p>
          </div>
        </div>

        <div>
          <label class="input-label">{{ t('admin.errorPassthrough.form.description') }}</label>
          <input
            v-model="form.description"
            type="text"
            class="input"
            :placeholder="t('admin.errorPassthrough.form.descriptionPlaceholder')"
          />
        </div>

        <!-- Match Conditions -->
        <div class="rounded-control border border-gray-200 p-3 dark:border-dark-600">
          <h4 class="mb-2 text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.errorPassthrough.form.matchConditions') }}
          </h4>

          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="input-label text-xs">{{
                t('admin.errorPassthrough.form.errorCodes')
              }}</label>
              <input
                v-model="errorCodesInput"
                type="text"
                class="input text-sm"
                :placeholder="t('admin.errorPassthrough.form.errorCodesPlaceholder')"
              />
              <p class="input-hint text-xs">
                {{ t('admin.errorPassthrough.form.errorCodesHint') }}
              </p>
            </div>
            <div>
              <label class="input-label text-xs">{{
                t('admin.errorPassthrough.form.keywords')
              }}</label>
              <textarea
                v-model="keywordsInput"
                rows="2"
                class="input font-mono text-xs"
                :placeholder="t('admin.errorPassthrough.form.keywordsPlaceholder')"
              />
              <p class="input-hint text-xs">{{ t('admin.errorPassthrough.form.keywordsHint') }}</p>
            </div>
          </div>

          <div class="mt-3">
            <label class="input-label text-xs">{{
              t('admin.errorPassthrough.form.matchMode')
            }}</label>
            <div class="mt-1 space-y-2">
              <label
                v-for="option in matchModeOptions"
                :key="option.value"
                class="flex items-start gap-2 cursor-pointer"
              >
                <input
                  type="radio"
                  :value="option.value"
                  v-model="form.match_mode"
                  class="mt-0.5 h-3.5 w-3.5 border-gray-300 text-primary-600 focus:ring-primary-500"
                />
                <div class="flex-1">
                  <span class="text-xs font-medium text-gray-700 dark:text-gray-300">{{
                    option.label
                  }}</span>
                  <p class="text-xs text-gray-500 dark:text-gray-400">{{ option.description }}</p>
                </div>
              </label>
            </div>
          </div>

          <div class="mt-3">
            <label class="input-label text-xs">{{
              t('admin.errorPassthrough.form.platforms')
            }}</label>
            <div class="flex flex-wrap gap-3">
              <label
                v-for="platform in platformOptions"
                :key="platform.value"
                class="inline-flex items-center gap-1.5"
              >
                <input
                  type="checkbox"
                  :value="platform.value"
                  v-model="form.platforms"
                  class="h-3.5 w-3.5 rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500"
                />
                <span class="text-xs text-gray-700 dark:text-gray-300">{{ platform.label }}</span>
              </label>
            </div>
            <p class="input-hint text-xs mt-1">
              {{ t('admin.errorPassthrough.form.platformsHint') }}
            </p>
          </div>
        </div>

        <!-- Response Behavior -->
        <div class="rounded-control border border-gray-200 p-3 dark:border-dark-600">
          <h4 class="mb-2 text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.errorPassthrough.form.responseBehavior') }}
          </h4>

          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="flex items-center gap-1.5">
                <input
                  type="checkbox"
                  v-model="form.passthrough_code"
                  class="h-3.5 w-3.5 rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500"
                />
                <span class="text-xs font-medium text-gray-700 dark:text-gray-300">
                  {{ t('admin.errorPassthrough.form.passthroughCode') }}
                </span>
              </label>
              <div v-if="!form.passthrough_code" class="mt-2">
                <label class="input-label text-xs">{{
                  t('admin.errorPassthrough.form.responseCode')
                }}</label>
                <input
                  v-model.number="form.response_code"
                  type="number"
                  min="100"
                  max="599"
                  class="input text-sm"
                  placeholder="422"
                />
              </div>
            </div>
            <div>
              <label class="flex items-center gap-1.5">
                <input
                  type="checkbox"
                  v-model="form.passthrough_body"
                  class="h-3.5 w-3.5 rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500"
                />
                <span class="text-xs font-medium text-gray-700 dark:text-gray-300">
                  {{ t('admin.errorPassthrough.form.passthroughBody') }}
                </span>
              </label>
              <div v-if="!form.passthrough_body" class="mt-2">
                <label class="input-label text-xs">{{
                  t('admin.errorPassthrough.form.customMessage')
                }}</label>
                <input
                  v-model="form.custom_message"
                  type="text"
                  class="input text-sm"
                  :placeholder="t('admin.errorPassthrough.form.customMessagePlaceholder')"
                />
              </div>
            </div>
          </div>
        </div>

        <!-- Skip Monitoring -->
        <div class="flex items-center gap-1.5">
          <input
            type="checkbox"
            v-model="form.skip_monitoring"
            class="h-3.5 w-3.5 rounded-compact border-gray-300 text-yellow-600 focus:ring-yellow-500"
          />
          <span class="text-xs font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.errorPassthrough.form.skipMonitoring') }}
          </span>
        </div>
        <p class="input-hint text-xs -mt-3">
          {{ t('admin.errorPassthrough.form.skipMonitoringHint') }}
        </p>

        <!-- Enabled -->
        <div class="flex items-center gap-1.5">
          <input
            type="checkbox"
            v-model="form.enabled"
            class="h-3.5 w-3.5 rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500"
          />
          <span class="text-xs font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.errorPassthrough.form.enabled') }}
          </span>
        </div>
      </form>

      <template #footer>
        <div class="flex justify-end gap-3">
          <button @click="closeFormModal" type="button" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button @click="handleSubmit" :disabled="submitting" class="btn btn-primary">
            <Icon
              v-if="submitting"
              name="refresh"
              size="sm"
              class="mr-1 animate-spin"
              :animate-on-hover="false"
            />
            {{ showEditModal ? t('common.update') : t('common.create') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Delete Confirmation -->
    <ConfirmDialog
      :show="showDeleteDialog"
      :title="t('admin.errorPassthrough.deleteRule')"
      :message="t('admin.errorPassthrough.deleteConfirm', { name: deletingRule?.name })"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmDelete"
      @cancel="showDeleteDialog = false"
    />
  </BaseDialog>
</template>

<script setup lang="ts">
import TableSkeletonBody from '@/components/common/TableSkeletonBody.vue'
import { ref, reactive, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import { adminAPI } from '@/api/admin'
import type { ErrorPassthroughRule } from '@/api/admin/errorPassthrough'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Toggle from '@/components/common/Toggle.vue'

/**
 * ErrorPassthroughRulesModal.vue manages upstream-error passthrough rules through list, create,
 * edit, toggle, and delete operations. Form text becomes error-code and keyword arrays before
 * submission. Passthrough switches clear the corresponding replacement code or message in the
 * submitted payload, while failed writes keep editable input available for correction.
 */

/**
 * props controls the outer rule list dialog, and opening refreshes its records while nested
 * create, edit, and delete dialogs remain local state.
 */
const props = defineProps<{
  show: boolean // parent-controlled rule dialog visibility
}>()

/**
 * emit delegates outer-dialog closure to the parent; the template invokes that event while this
 * script owns nested form state.
 */
const emit = defineEmits<{
  close: [] // request parent dialog closure
}>()

void emit // suppress unused warning - emit is used via $emit in template

/**
 * Translation and application toasts provide matching-mode descriptions, condition validation,
 * and persistence feedback for the rule-management dialogs.
 */
const { t } = useI18n()
const appStore = useAppStore()

/**
 * Rule records, request flags, nested-dialog visibility, and selected edit or delete targets
 * are owned by this mounted rule-management component.
 */
const rules = ref<ErrorPassthroughRule[]>([])
const loading = ref(false)
const submitting = ref(false)
const showCreateModal = ref(false)
const showEditModal = ref(false)
const showDeleteDialog = ref(false)
const editingRule = ref<ErrorPassthroughRule | null>(null)
const deletingRule = ref<ErrorPassthroughRule | null>(null)

/**
 * Error-code and keyword text inputs retain the editable representation while form stores rule
 * matching, passthrough, monitoring, and description fields.
 */
const errorCodesInput = ref('')
const keywordsInput = ref('')

/**
 * form owns the complete rule draft; passthrough flags determine whether response_code and
 * custom_message are submitted as null or configured replacements.
 */
const form = reactive({
  name: '', // editable rule display name
  enabled: true, // rule matching enabled flag
  /**
   * Enabled rules run in ascending priority order; the first matching rule determines the
   * response behavior.
   */
  priority: 0,
  /**
   * match_mode combines configured error-code and keyword dimensions with any or all; a single
   * configured dimension decides the match, while platforms remains a separate prerequisite.
   */
  match_mode: 'any' as 'any' | 'all',
  platforms: [] as string[], // empty selects all platforms
  /**
   * When true, submission clears response_code because the upstream response status is passed
   * through unchanged.
   */
  passthrough_code: true,
  response_code: null as number | null, // optional replacement response status
  /**
   * When true, submission clears custom_message and the gateway uses the extracted upstream
   * error message, wrapped in the response format for the current client protocol.
   */
  passthrough_body: true,
  custom_message: null as string | null, // optional replacement response message
  /**
   * A matching final or intermediate upstream error marks the request to skip ordinary Ops
   * error records; access, usage, audit, and security records retain their usual handling.
   */
  skip_monitoring: false,
  description: null as string | null, // optional rule explanatory text
})

/**
 * Match-mode and platform options provide the supported condition combination and concrete
 * provider platforms used by the rule editor.
 */
const matchModeOptions = computed(() => [
  {
    value: 'any',
    label: t('admin.errorPassthrough.matchMode.any'),
    description: t('admin.errorPassthrough.matchMode.anyHint'),
  },
  {
    value: 'all',
    label: t('admin.errorPassthrough.matchMode.all'),
    description: t('admin.errorPassthrough.matchMode.allHint'),
  },
])

const platformOptions = CONCRETE_PLATFORM_OPTIONS

// Refresh the rule list when the outer dialog opens so newly opened editors start from the
// latest returned server records.
watch(
  () => props.show,
  (newVal) => {
    if (newVal) {
      loadRules()
    }
  },
)

/**
 * loadRules refreshes the rule list through the administrative API. Failure reports a toast and
 * retains the previous list, while loading feedback clears after the request settles.
 */
const loadRules = async () => {
  loading.value = true
  try {
    rules.value = await adminAPI.errorPassthrough.list()
  } catch (error) {
    appStore.showError(t('admin.errorPassthrough.failedToLoad'))
    console.error('Error loading rules:', error)
  } finally {
    loading.value = false
  }
}

/**
 * resetForm restores passthrough defaults and clears editable matching-condition text.
 */
const resetForm = () => {
  form.name = ''
  form.enabled = true
  form.priority = 0
  form.match_mode = 'any'
  form.platforms = []
  form.passthrough_code = true
  form.response_code = null
  form.passthrough_body = true
  form.custom_message = null
  form.skip_monitoring = false
  form.description = null
  errorCodesInput.value = ''
  keywordsInput.value = ''
}

/**
 * closeFormModal closes create and edit dialogs, clears the selected rule, and resets their
 * shared draft. Callers use it after successful persistence or explicit form dismissal.
 */
const closeFormModal = () => {
  showCreateModal.value = false
  showEditModal.value = false
  editingRule.value = null
  resetForm()
}

/**
 * handleEdit copies the selected rule into the shared draft and opens editing. Platform
 * selection is copied into a new array, while error codes and keywords become editable text.
 */
const handleEdit = (rule: ErrorPassthroughRule) => {
  editingRule.value = rule
  form.name = rule.name
  form.enabled = rule.enabled
  form.priority = rule.priority
  form.match_mode = rule.match_mode
  form.platforms = [...rule.platforms]
  form.passthrough_code = rule.passthrough_code
  form.response_code = rule.response_code
  form.passthrough_body = rule.passthrough_body
  form.custom_message = rule.custom_message
  form.skip_monitoring = rule.skip_monitoring
  form.description = rule.description
  errorCodesInput.value = rule.error_codes.join(', ')
  keywordsInput.value = rule.keywords.join('\n')
  showEditModal.value = true
}

/**
 * handleDelete selects a rule and opens its deletion confirmation dialog.
 */
const handleDelete = (rule: ErrorPassthroughRule) => {
  deletingRule.value = rule
  showDeleteDialog.value = true
}

/**
 * parseErrorCodes splits comma or whitespace-separated input into positive parsed integers.
 * Blank input yields an empty list, and invalid or nonpositive entries are discarded before
 * condition validation.
 */
const parseErrorCodes = (): number[] => {
  if (!errorCodesInput.value.trim()) return []
  return errorCodesInput.value
    .split(/[,\s]+/)
    .map((s) => parseInt(s.trim(), 10))
    .filter((n) => !isNaN(n) && n > 0)
}

/**
 * parseKeywords splits the draft into trimmed nonempty keyword lines. Blank input yields an
 * empty array, preserving line boundaries as the editor representation for independent match
 * keywords.
 */
const parseKeywords = (): string[] => {
  if (!keywordsInput.value.trim()) return []
  return keywordsInput.value
    .split('\n')
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
}

/**
 * handleSubmit requires a trimmed name and at least one valid matching condition before saving.
 * It clears replacement fields governed by passthrough switches, then closes and reloads after
 * a successful create or update.
 */
const handleSubmit = async () => {
  if (!form.name.trim()) {
    appStore.showError(t('admin.errorPassthrough.nameRequired'))
    return
  }

  const errorCodes = parseErrorCodes()
  const keywords = parseKeywords()

  if (errorCodes.length === 0 && keywords.length === 0) {
    appStore.showError(t('admin.errorPassthrough.conditionsRequired'))
    return
  }

  submitting.value = true
  try {
    const data = {
      name: form.name.trim(),
      enabled: form.enabled,
      priority: form.priority,
      error_codes: errorCodes,
      keywords: keywords,
      match_mode: form.match_mode,
      platforms: form.platforms,
      passthrough_code: form.passthrough_code,
      response_code: form.passthrough_code ? null : form.response_code,
      passthrough_body: form.passthrough_body,
      custom_message: form.passthrough_body ? null : form.custom_message,
      skip_monitoring: form.skip_monitoring,
      description: form.description?.trim() || null,
    }

    if (showEditModal.value && editingRule.value) {
      await adminAPI.errorPassthrough.update(editingRule.value.id, data)
      appStore.showSuccess(t('admin.errorPassthrough.ruleUpdated'))
    } else {
      await adminAPI.errorPassthrough.create(data)
      appStore.showSuccess(t('admin.errorPassthrough.ruleCreated'))
    }

    // Close and discard the draft only after persistence succeeds; reload then replaces the
    // list with the saved server representation.
    closeFormModal()
    loadRules()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.errorPassthrough.failedToSave'))
    console.error('Error saving rule:', error)
  } finally {
    submitting.value = false
  }
}

/**
 * toggleEnabled persists the inverse enabled state and mutates the row after success. A
 * rejected request leaves the visible enabled value unchanged and reports the server detail or
 * generic failure.
 */
const toggleEnabled = async (rule: ErrorPassthroughRule) => {
  try {
    await adminAPI.errorPassthrough.toggleEnabled(rule.id, !rule.enabled)
    // Publish the toggle after the endpoint accepts it so a rejected write leaves the row
    // showing its previous saved state.
    rule.enabled = !rule.enabled
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.errorPassthrough.failedToToggle'))
    console.error('Error toggling rule:', error)
  }
}

/**
 * confirmDelete deletes the selected rule before closing confirmation and reloading the list.
 * Failure retains the selected rule and confirmation state so the administrator can inspect
 * feedback or retry.
 */
const confirmDelete = async () => {
  if (!deletingRule.value) return

  try {
    await adminAPI.errorPassthrough.delete(deletingRule.value.id)
    appStore.showSuccess(t('admin.errorPassthrough.ruleDeleted'))
    showDeleteDialog.value = false
    deletingRule.value = null
    loadRules()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.errorPassthrough.failedToDelete'))
    console.error('Error deleting rule:', error)
  }
}
</script>
