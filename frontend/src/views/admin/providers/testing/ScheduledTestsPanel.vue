<template>
  <BaseDialog
    :show="show"
    :title="t('admin.scheduledTests.title')"
    width="wide"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <!-- Add Plan Button -->
      <div class="flex items-center justify-between">
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.scheduledTests.title') }}
        </p>
        <button
          @click="showAddForm = !showAddForm"
          class="btn btn-primary flex items-center gap-1.5 text-sm"
        >
          <Icon name="plus" size="sm" :stroke-width="2" />
          {{ t('admin.scheduledTests.addPlan') }}
        </button>
      </div>

      <!-- Add Plan Form -->
      <Collapse :open="showAddForm" unmount-on-hide>
        <div
          class="rounded-surface border border-primary-200 bg-primary-50/50 p-4 dark:border-primary-800 dark:bg-primary-900/20"
        >
          <div class="mb-3 text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.scheduledTests.addPlan') }}
          </div>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
                {{ t('admin.scheduledTests.model') }}
              </label>
              <Select
                v-model="newPlan.model_id"
                :options="modelOptions"
                :placeholder="t('admin.scheduledTests.model')"
                :searchable="modelOptions.length > 5"
              />
            </div>
            <div>
              <label
                class="mb-1 flex items-center gap-1 text-xs font-medium text-gray-600 dark:text-gray-400"
              >
                {{ t('admin.scheduledTests.cronExpression') }}
                <HelpTooltip>
                  <div class="space-y-1.5">
                    <p class="font-medium">{{ t('admin.scheduledTests.cronTooltipTitle') }}</p>
                    <p>{{ t('admin.scheduledTests.cronTooltipMeaning') }}</p>
                    <p>{{ t('admin.scheduledTests.cronTooltipExampleEvery30Min') }}</p>
                    <p>{{ t('admin.scheduledTests.cronTooltipExampleHourly') }}</p>
                    <p>{{ t('admin.scheduledTests.cronTooltipExampleDaily') }}</p>
                    <p>{{ t('admin.scheduledTests.cronTooltipExampleWeekly') }}</p>
                    <p>{{ t('admin.scheduledTests.cronTooltipRange') }}</p>
                  </div>
                </HelpTooltip>
              </label>
              <Input
                v-model="newPlan.cron_expression"
                :placeholder="'*/30 * * * *'"
                :hint="t('admin.scheduledTests.cronHelp')"
              />
            </div>
            <div>
              <label
                class="mb-1 flex items-center gap-1 text-xs font-medium text-gray-600 dark:text-gray-400"
              >
                {{ t('admin.scheduledTests.maxResults') }}
                <HelpTooltip>
                  <div class="space-y-1.5">
                    <p class="font-medium">
                      {{ t('admin.scheduledTests.maxResultsTooltipTitle') }}
                    </p>
                    <p>{{ t('admin.scheduledTests.maxResultsTooltipMeaning') }}</p>
                    <p>{{ t('admin.scheduledTests.maxResultsTooltipBody') }}</p>
                    <p>{{ t('admin.scheduledTests.maxResultsTooltipExample') }}</p>
                    <p>{{ t('admin.scheduledTests.maxResultsTooltipRange') }}</p>
                  </div>
                </HelpTooltip>
              </label>
              <Input v-model="newPlan.max_results" type="number" placeholder="100" />
            </div>
            <div class="flex items-end">
              <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                <Toggle v-model="newPlan.enabled" />
                {{ t('admin.scheduledTests.enabled') }}
              </label>
            </div>
            <div class="flex items-end">
              <div>
                <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                  <Toggle v-model="newPlan.auto_recover" />
                  {{ t('admin.scheduledTests.autoRecover') }}
                </label>
                <p class="mt-0.5 text-xs text-gray-400 dark:text-gray-500">
                  {{ t('admin.scheduledTests.autoRecoverHelp') }}
                </p>
              </div>
            </div>
          </div>
          <div class="mt-3 flex justify-end gap-2">
            <button
              @click="
                showAddForm = false;
                resetNewPlan();
              "
              class="btn btn-secondary"
            >
              {{ t('common.cancel') }}
            </button>
            <button
              @click="handleCreate"
              :disabled="!newPlan.model_id || !newPlan.cron_expression || creating"
              class="btn btn-primary"
            >
              <Icon
                v-if="creating"
                name="refresh"
                size="sm"
                class="animate-spin"
                :stroke-width="2"
                :animate-on-hover="false"
              />
              {{ t('common.save') }}
            </button>
          </div>
        </div>
      </Collapse>

      <!-- Loading State -->
      <ContentSkeleton v-if="loading" variant="list" :rows="3" class="py-4" />

      <!-- Empty State -->
      <div
        v-else-if="plans.length === 0"
        class="rounded-surface border border-dashed border-gray-300 py-10 text-center dark:border-dark-600"
      >
        <Icon name="calendar" size="lg" class="mx-auto mb-2 text-gray-400" :stroke-width="1.5" />
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.scheduledTests.noPlans') }}
        </p>
      </div>

      <!-- Plans List -->
      <div v-else class="space-y-3">
        <div
          v-for="plan in plans"
          :key="plan.id"
          class="rounded-surface border border-gray-200 bg-white transition dark:border-dark-600 dark:bg-dark-800"
        >
          <!-- Plan Header -->
          <div
            data-icon-trigger
            class="flex cursor-pointer items-center justify-between px-4 py-3"
            @click="toggleExpand(plan.id)"
          >
            <div class="flex flex-1 items-center gap-4">
              <!-- Model -->
              <div class="min-w-0">
                <div class="text-sm font-medium text-gray-900 dark:text-gray-100">
                  {{ plan.model_id }}
                </div>
                <div class="mt-0.5 font-mono text-xs text-gray-500 dark:text-gray-400">
                  {{ plan.cron_expression }}
                </div>
              </div>

              <!-- Enabled Toggle -->
              <div class="flex items-center gap-1.5" @click.stop>
                <Toggle
                  :model-value="plan.enabled"
                  @update:model-value="(val: boolean) => handleToggleEnabled(plan, val)"
                />
                <span class="text-xs text-gray-500 dark:text-gray-400">
                  {{ plan.enabled ? t('admin.scheduledTests.enabled') : '' }}
                </span>
              </div>

              <!-- Auto Recover Badge -->
              <span
                v-if="plan.auto_recover"
                class="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-500/20 dark:text-emerald-400"
              >
                {{ t('admin.scheduledTests.autoRecover') }}
              </span>
            </div>

            <div class="flex items-center gap-3">
              <!-- Last Run -->
              <div
                v-if="plan.last_run_at"
                class="hidden text-right text-xs text-gray-500 dark:text-gray-400 sm:block"
              >
                <div>{{ t('admin.scheduledTests.lastRun') }}</div>
                <div>{{ formatDateTime(plan.last_run_at) }}</div>
              </div>

              <!-- Next Run -->
              <div
                v-if="plan.next_run_at"
                class="hidden text-right text-xs text-gray-500 dark:text-gray-400 sm:block"
              >
                <div>{{ t('admin.scheduledTests.nextRun') }}</div>
                <div>{{ formatDateTime(plan.next_run_at) }}</div>
              </div>

              <!-- Actions -->
              <div class="flex items-center gap-1" @click.stop>
                <button
                  @click="startEdit(plan)"
                  class="rounded-control p-1.5 text-gray-400 transition-colors hover:bg-primary-50 hover:text-primary-500 dark:hover:bg-primary-500/8"
                  :title="t('admin.scheduledTests.editPlan')"
                >
                  <Icon name="edit" size="sm" :stroke-width="2" />
                </button>
                <button
                  @click="confirmDeletePlan(plan)"
                  class="rounded-control p-1.5 text-gray-400 transition-colors hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-900/20"
                  :title="t('admin.scheduledTests.deletePlan')"
                >
                  <Icon name="trash" size="sm" :stroke-width="2" />
                </button>
              </div>

              <!-- Expand indicator -->
              <Icon
                name="chevronDown"
                size="sm"
                :class="[
                  'text-gray-400 transition-transform duration-normal',
                  expandedPlanId === plan.id ? 'rotate-180' : '',
                ]"
                :animate-on-hover="false"
              />
            </div>
          </div>

          <!-- Edit Form -->
          <div
            v-if="editingPlanId === plan.id"
            class="border-t border-blue-100 bg-blue-50/50 px-4 py-3 dark:border-blue-900 dark:bg-blue-900/10"
            @click.stop
          >
            <div class="mb-2 text-xs font-medium text-gray-600 dark:text-gray-400">
              {{ t('admin.scheduledTests.editPlan') }}
            </div>
            <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div>
                <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
                  {{ t('admin.scheduledTests.model') }}
                </label>
                <Select
                  v-model="editForm.model_id"
                  :options="modelOptions"
                  :placeholder="t('admin.scheduledTests.model')"
                  :searchable="modelOptions.length > 5"
                />
              </div>
              <div>
                <label
                  class="mb-1 flex items-center gap-1 text-xs font-medium text-gray-600 dark:text-gray-400"
                >
                  {{ t('admin.scheduledTests.cronExpression') }}
                  <HelpTooltip>
                    <div class="space-y-1.5">
                      <p class="font-medium">{{ t('admin.scheduledTests.cronTooltipTitle') }}</p>
                      <p>{{ t('admin.scheduledTests.cronTooltipMeaning') }}</p>
                      <p>{{ t('admin.scheduledTests.cronTooltipExampleEvery30Min') }}</p>
                      <p>{{ t('admin.scheduledTests.cronTooltipExampleHourly') }}</p>
                      <p>{{ t('admin.scheduledTests.cronTooltipExampleDaily') }}</p>
                      <p>{{ t('admin.scheduledTests.cronTooltipExampleWeekly') }}</p>
                      <p>{{ t('admin.scheduledTests.cronTooltipRange') }}</p>
                    </div>
                  </HelpTooltip>
                </label>
                <Input
                  v-model="editForm.cron_expression"
                  :placeholder="'*/30 * * * *'"
                  :hint="t('admin.scheduledTests.cronHelp')"
                />
              </div>
              <div>
                <label
                  class="mb-1 flex items-center gap-1 text-xs font-medium text-gray-600 dark:text-gray-400"
                >
                  {{ t('admin.scheduledTests.maxResults') }}
                  <HelpTooltip>
                    <div class="space-y-1.5">
                      <p class="font-medium">
                        {{ t('admin.scheduledTests.maxResultsTooltipTitle') }}
                      </p>
                      <p>{{ t('admin.scheduledTests.maxResultsTooltipMeaning') }}</p>
                      <p>{{ t('admin.scheduledTests.maxResultsTooltipBody') }}</p>
                      <p>{{ t('admin.scheduledTests.maxResultsTooltipExample') }}</p>
                      <p>{{ t('admin.scheduledTests.maxResultsTooltipRange') }}</p>
                    </div>
                  </HelpTooltip>
                </label>
                <Input v-model="editForm.max_results" type="number" placeholder="100" />
              </div>
              <div class="flex items-end">
                <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                  <Toggle v-model="editForm.enabled" />
                  {{ t('admin.scheduledTests.enabled') }}
                </label>
              </div>
              <div class="flex items-end">
                <div>
                  <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                    <Toggle v-model="editForm.auto_recover" />
                    {{ t('admin.scheduledTests.autoRecover') }}
                  </label>
                  <p class="mt-0.5 text-xs text-gray-400 dark:text-gray-500">
                    {{ t('admin.scheduledTests.autoRecoverHelp') }}
                  </p>
                </div>
              </div>
            </div>
            <div class="mt-3 flex justify-end gap-2">
              <button @click="cancelEdit" class="btn btn-secondary">
                {{ t('common.cancel') }}
              </button>
              <button
                @click="handleEdit"
                :disabled="!editForm.model_id || !editForm.cron_expression || updating"
                class="btn btn-primary"
              >
                <Icon
                  v-if="updating"
                  name="refresh"
                  size="sm"
                  class="animate-spin"
                  :stroke-width="2"
                  :animate-on-hover="false"
                />
                {{ t('common.save') }}
              </button>
            </div>
          </div>

          <!-- Expanded Results Section -->
          <Collapse :open="expandedPlanId === plan.id" unmount-on-hide>
            <div class="border-t border-gray-100 px-4 py-3 dark:border-dark-700">
              <div class="mb-2 text-xs font-medium text-gray-600 dark:text-gray-400">
                {{ t('admin.scheduledTests.results') }}
              </div>

              <!-- Results Loading -->
              <ContentSkeleton v-if="loadingResults" variant="list" :rows="3" class="py-4" />

              <!-- No Results -->
              <div
                v-else-if="results.length === 0"
                class="py-4 text-center text-xs text-gray-500 dark:text-gray-400"
              >
                {{ t('admin.scheduledTests.noResults') }}
              </div>

              <!-- Results List -->
              <div v-else class="max-h-64 space-y-2 overflow-y-auto">
                <div
                  v-for="result in results"
                  :key="result.id"
                  class="rounded-surface border border-gray-100 bg-gray-50 p-3 dark:border-dark-700 dark:bg-dark-900"
                >
                  <div class="flex items-center justify-between">
                    <div class="flex items-center gap-2">
                      <!-- Status Badge -->
                      <span
                        :class="[
                          'inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium',
                          result.status === 'success'
                            ? 'bg-green-100 text-green-700 dark:bg-green-500/20 dark:text-green-400'
                            : result.status === 'running'
                              ? 'bg-blue-100 text-blue-700 dark:bg-blue-500/20 dark:text-blue-400'
                              : 'bg-red-100 text-red-700 dark:bg-red-500/20 dark:text-red-400',
                        ]"
                      >
                        {{
                          result.status === 'success'
                            ? t('admin.scheduledTests.success')
                            : result.status === 'running'
                              ? t('admin.scheduledTests.running')
                              : t('admin.scheduledTests.failed')
                        }}
                      </span>

                      <!-- Latency -->
                      <span
                        v-if="result.latency_ms > 0"
                        class="text-xs text-gray-500 dark:text-gray-400"
                      >
                        {{ result.latency_ms }}ms
                      </span>
                    </div>

                    <!-- Started At -->
                    <span class="text-xs text-gray-400">
                      {{ formatDateTime(result.started_at) }}
                    </span>
                  </div>

                  <!-- Response / Error (collapsible) -->
                  <div v-if="result.error_message" class="mt-2">
                    <div
                      data-icon-trigger
                      class="cursor-pointer text-xs font-medium text-red-600 dark:text-red-400"
                      @click="toggleResultDetail(result.id)"
                    >
                      {{ t('admin.scheduledTests.errorMessage') }}
                      <Icon
                        name="chevronDown"
                        size="sm"
                        :class="[
                          'inline transition-transform duration-normal',
                          expandedResultIds.has(result.id) ? 'rotate-180' : '',
                        ]"
                        :animate-on-hover="false"
                      />
                    </div>
                    <Collapse :open="expandedResultIds.has(result.id)" unmount-on-hide>
                      <pre
                        class="mt-1 max-h-32 overflow-auto whitespace-pre-wrap rounded-compact bg-red-50 p-2 text-xs text-red-700 dark:bg-red-900/20 dark:text-red-300"
                        >{{ result.error_message }}</pre>
                    </Collapse>
                  </div>
                  <div v-else-if="result.response_text" class="mt-2">
                    <div
                      data-icon-trigger
                      class="cursor-pointer text-xs font-medium text-gray-600 dark:text-gray-400"
                      @click="toggleResultDetail(result.id)"
                    >
                      {{ t('admin.scheduledTests.responseText') }}
                      <Icon
                        name="chevronDown"
                        size="sm"
                        :class="[
                          'inline transition-transform duration-normal',
                          expandedResultIds.has(result.id) ? 'rotate-180' : '',
                        ]"
                        :animate-on-hover="false"
                      />
                    </div>
                    <Collapse :open="expandedResultIds.has(result.id)" unmount-on-hide>
                      <pre
                        class="mt-1 max-h-32 overflow-auto whitespace-pre-wrap rounded-compact bg-gray-100 p-2 text-xs text-gray-700 dark:bg-dark-800 dark:text-gray-300"
                        >{{ result.response_text }}</pre>
                    </Collapse>
                  </div>
                </div>
              </div>
            </div>
          </Collapse>
        </div>
      </div>
    </div>

    <!-- Delete Confirmation -->
    <ConfirmDialog
      :show="showDeleteConfirm"
      :title="t('admin.scheduledTests.deletePlan')"
      :message="t('admin.scheduledTests.confirmDelete')"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="handleDelete"
      @cancel="showDeleteConfirm = false"
    />
  </BaseDialog>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import Collapse from '@/components/common/Collapse.vue'

import { ref, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Input from '@/components/common/Input.vue'
import Toggle from '@/components/common/Toggle.vue'
import { Icon } from '@/components/icons'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import type { ScheduledTestPlan, ScheduledTestResult } from '@/api/admin/scheduledTests.types'

/**
 * ScheduledTestsPanel.vue manages recurring provider-test plans and displays retained execution
 * results for one expanded plan. Create and edit drafts submit model, cron, retention count,
 * and recovery settings through the administrative scheduled-test API. Successful writes update
 * or reload the displayed plan list, while failed requests show an error toast.
 */

/**
 * Translation and application toasts provide labels and request feedback for schedule loading,
 * editing, creation, and deletion within this panel.
 */
const { t } = useI18n()
const appStore = useAppStore()

/**
 * props identifies the current provider and model choices; visibility changes load schedules or
 * clear visible plan and result state.
 */
const props = defineProps<{
  show: boolean // parent-controlled panel visibility
  providerId: number | null // provider identifier or empty selection
  modelOptions: SelectOption[] // available scheduled model choices
}>()

/**
 * emit requests that the parent close the scheduled-tests panel while the parent retains
 * ownership of its visibility.
 */
const emit = defineEmits<{
  /**
   * close requests that the parent hide the scheduled-test management panel.
   */
  (e: 'close'): void
}>()

/**
 * Request flags, plan and result arrays, and expansion selections belong to this panel and are
 * updated by its schedule operations.
 */
const loading = ref(false)
const creating = ref(false)
const loadingResults = ref(false)
const plans = ref<ScheduledTestPlan[]>([])
const results = ref<ScheduledTestResult[]>([])
const expandedPlanId = ref<number | null>(null)
const expandedResultIds = reactive(new Set<number>())
const showAddForm = ref(false)
const showDeleteConfirm = ref(false)
const deletingPlan = ref<ScheduledTestPlan | null>(null)
const editingPlanId = ref<number | null>(null)
const updating = ref(false)
/**
 * editForm stores text inputs and booleans for the selected plan, with max_results converted
 * back to a number during submission.
 */
const editForm = reactive({
  model_id: '' as string, // scheduled test model identifier
  cron_expression: '' as string, // scheduled test cron expression
  /**
   * Text input becomes a number on save; zero or invalid conversion uses the default retention
   * count of one hundred.
   */
  max_results: '100' as string,
  enabled: true, // scheduled execution enabled flag
  auto_recover: false, // successful test recovery preference
})

/**
 * newPlan owns the create draft independently of inline editing and resets after a successful
 * create operation before plans reload.
 */
const newPlan = reactive({
  model_id: '' as string, // new scheduled model identifier
  cron_expression: '' as string, // new schedule cron expression
  /**
   * Text input becomes a number on create; zero or invalid conversion uses the default
   * retention count of one hundred.
   */
  max_results: '100' as string,
  enabled: true, // new schedule enabled flag
  auto_recover: false, // successful test recovery preference
})

/**
 * resetNewPlan restores the create draft to its initial schedule, retention, and recovery
 * values.
 */
const resetNewPlan = () => {
  newPlan.model_id = ''
  newPlan.cron_expression = ''
  newPlan.max_results = '100'
  newPlan.enabled = true
  newPlan.auto_recover = false
}

// Hiding the panel clears the visible list and expansion state; outstanding requests still
// settle through their normal handlers.
watch(
  () => props.show,
  async (visible) => {
    if (visible && props.providerId) {
      await loadPlans()
    } else {
      plans.value = []
      results.value = []
      expandedPlanId.value = null
      expandedResultIds.clear()
      showAddForm.value = false
      showDeleteConfirm.value = false
    }
  },
)

/**
 * loadPlans fetches schedules for the current provider when an identifier is available. It
 * retains displayed plans on failure and reports the request error through the application
 * toast store.
 */
const loadPlans = async () => {
  if (!props.providerId) return
  loading.value = true
  try {
    plans.value = await adminAPI.scheduledTests.listByProvider(props.providerId)
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to load plans')
  } finally {
    loading.value = false
  }
}

/**
 * handleCreate submits a complete schedule when provider, model, and cron values are present.
 * Falsy numeric retention becomes 100; success closes and resets the create form before
 * reloading plans.
 */
const handleCreate = async () => {
  if (!props.providerId || !newPlan.model_id || !newPlan.cron_expression) return
  creating.value = true
  try {
    const maxResults = Number(newPlan.max_results) || 100
    // Persist the plan before resetting its draft or refreshing the list so rejected requests
    // leave the administrator input available.
    await adminAPI.scheduledTests.create({
      provider_id: props.providerId,
      model_id: newPlan.model_id,
      cron_expression: newPlan.cron_expression,
      enabled: newPlan.enabled,
      max_results: maxResults,
      auto_recover: newPlan.auto_recover,
    })
    appStore.showSuccess(t('admin.scheduledTests.createSuccess'))
    showAddForm.value = false
    resetNewPlan()
    await loadPlans()
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to create plan')
  } finally {
    creating.value = false
  }
}

/**
 * handleToggleEnabled saves one plan enabled flag and replaces its displayed row with the
 * server response. Failures leave the current row in place and report an error toast.
 */
const handleToggleEnabled = async (plan: ScheduledTestPlan, enabled: boolean) => {
  try {
    const updated = await adminAPI.scheduledTests.update(plan.id, { enabled })
    const index = plans.value.findIndex((p) => p.id === plan.id)
    if (index !== -1) {
      plans.value[index] = updated
    }
    appStore.showSuccess(t('admin.scheduledTests.updateSuccess'))
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to update plan')
  }
}

/**
 * startEdit copies a plan into the shared edit draft and selects its identifier. The retention
 * count becomes text for the input, while booleans retain the saved enabled and recovery
 * settings.
 */
const startEdit = (plan: ScheduledTestPlan) => {
  editingPlanId.value = plan.id
  editForm.model_id = plan.model_id
  editForm.cron_expression = plan.cron_expression
  editForm.max_results = String(plan.max_results)
  editForm.enabled = plan.enabled
  editForm.auto_recover = plan.auto_recover
}

/**
 * cancelEdit exits the inline editor by clearing the selected plan identifier.
 */
const cancelEdit = () => {
  editingPlanId.value = null
}

/**
 * handleEdit submits the current edit draft when its identifier, model, and cron values are
 * present. Success replaces the matching row and closes editing; failure leaves the draft
 * available for correction.
 */
const handleEdit = async () => {
  if (!editingPlanId.value || !editForm.model_id || !editForm.cron_expression) return
  updating.value = true
  try {
    const updated = await adminAPI.scheduledTests.update(editingPlanId.value, {
      model_id: editForm.model_id,
      cron_expression: editForm.cron_expression,
      max_results: Number(editForm.max_results) || 100,
      enabled: editForm.enabled,
      auto_recover: editForm.auto_recover,
    })
    const index = plans.value.findIndex((p) => p.id === editingPlanId.value)
    if (index !== -1) {
      plans.value[index] = updated
    }
    appStore.showSuccess(t('admin.scheduledTests.updateSuccess'))
    editingPlanId.value = null
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to update plan')
  } finally {
    updating.value = false
  }
}

/**
 * confirmDeletePlan selects a plan and opens its deletion confirmation dialog.
 */
const confirmDeletePlan = (plan: ScheduledTestPlan) => {
  deletingPlan.value = plan
  showDeleteConfirm.value = true
}

/**
 * handleDelete deletes the selected plan and removes its displayed row after success. Deleting
 * the expanded plan also clears its results; both success and failure close and clear the
 * confirmation selection.
 */
const handleDelete = async () => {
  if (!deletingPlan.value) return
  try {
    // Wait for server deletion before removing the row so a rejected request keeps the existing
    // plan visible for retry.
    await adminAPI.scheduledTests.delete(deletingPlan.value.id)
    appStore.showSuccess(t('admin.scheduledTests.deleteSuccess'))
    plans.value = plans.value.filter((p) => p.id !== deletingPlan.value!.id)
    if (expandedPlanId.value === deletingPlan.value.id) {
      expandedPlanId.value = null
      results.value = []
    }
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to delete plan')
  } finally {
    showDeleteConfirm.value = false
    deletingPlan.value = null
  }
}

/**
 * toggleExpand opens one plan result list or collapses the currently expanded plan. Expansion
 * clears result-detail selection and fetches at most twenty results; a failed fetch clears the
 * displayed result array.
 */
const toggleExpand = async (planId: number) => {
  if (expandedPlanId.value === planId) {
    expandedPlanId.value = null
    results.value = []
    expandedResultIds.clear()
    return
  }

  expandedPlanId.value = planId
  expandedResultIds.clear()
  loadingResults.value = true
  try {
    // Limit the expanded history to twenty retained results.
    results.value = await adminAPI.scheduledTests.listResults(planId, 20)
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to load results')
    // Clear failed history data so the newly expanded plan does not display results left from a
    // previous selection.
    results.value = []
  } finally {
    loadingResults.value = false
  }
}

/**
 * toggleResultDetail toggles one retained result identifier in the expanded detail set.
 */
const toggleResultDetail = (resultId: number) => {
  if (expandedResultIds.has(resultId)) {
    expandedResultIds.delete(resultId)
  } else {
    expandedResultIds.add(resultId)
  }
}
</script>
