<template>
  <AppLayout>
    <template #page-heading-actions>
      <div class="flex items-center justify-end gap-2">
        <button
          class="btn btn-secondary shrink-0 btn-icon"
          :disabled="plansLoading"
          :title="t('common.refresh')"
          @click="loadPlans"
        >
          <Icon name="refresh" size="sm" :class="plansLoading ? 'animate-spin' : ''" />
        </button>
        <button class="btn btn-primary" @click="openPlanEdit(null)">
          <Icon name="plus" size="sm" class="mr-2" />
          {{ t('payment.admin.createPlan') }}
        </button>
      </div>
    </template>
    <TablePageLayout>
      <template #table>
        <DataTable
          column-order-storage-key="admin-payment-plans-column-order"
          :columns="planColumns"
          :data="plans"
          :loading="plansLoading"
        >
          <template #cell-price="{ value, row }">
            <div class="text-sm">
              <span class="font-medium text-gray-900 dark:text-white"
                >{{ planCurrencySymbol(row.currency) }}{{ (value ?? 0).toFixed(2) }}</span
              >
              <span v-if="row.currency" class="ml-1 text-xs text-gray-400">{{ row.currency }}</span>
              <span v-if="row.original_price" class="ml-1 text-xs text-gray-400 line-through">
                {{ planCurrencySymbol(row.currency) }}{{ row.original_price.toFixed(2) }}
              </span>
            </div>
          </template>

          <template #cell-validity_days="{ value, row }">
            <span class="text-sm">{{ value }} {{ formatValidityUnit(row.validity_unit) }}</span>
          </template>

          <template #cell-quota="{ row }">
            <div class="space-y-1 text-xs text-gray-600 dark:text-gray-300">
              <div v-if="row.daily_limit_usd != null && row.daily_limit_usd > 0">
                {{ t('payment.admin.dailyLimit') }}: {{ row.daily_limit_usd }}
              </div>
              <div v-if="row.weekly_limit_usd != null && row.weekly_limit_usd > 0">
                {{ t('payment.admin.weeklyLimit') }}: {{ row.weekly_limit_usd }}
              </div>
              <div v-if="row.monthly_limit_usd != null && row.monthly_limit_usd > 0">
                {{ t('payment.admin.monthlyLimit') }}: {{ row.monthly_limit_usd }}
              </div>
              <div
                v-if="
                  !(row.daily_limit_usd != null && row.daily_limit_usd > 0) &&
                  !(row.weekly_limit_usd != null && row.weekly_limit_usd > 0) &&
                  !(row.monthly_limit_usd != null && row.monthly_limit_usd > 0)
                "
              >
                {{ t('payment.admin.unlimited') }}
              </div>
            </div>
          </template>

          <template #cell-for_sale="{ value, row }">
            <Toggle :model-value="!!value" size="sm" @update:model-value="toggleForSale(row)" />
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center gap-2">
              <button
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-primary-50 hover:text-primary-600 dark:hover:bg-primary-500/8 dark:hover:text-primary-500"
                @click="openPlanEdit(row)"
              >
                <Icon name="edit" size="sm" />
                <span class="text-xs">{{ t('common.edit') }}</span>
              </button>
              <button
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                @click="confirmDeletePlan(row)"
              >
                <Icon name="trash" size="sm" />
                <span class="text-xs">{{ t('common.delete') }}</span>
              </button>
            </div>
          </template>
        </DataTable>
      </template>
    </TablePageLayout>

    <PlanEditDialog
      :show="showPlanDialog"
      :plan="editingPlan"
      :payment-config="paymentConfig"
      @close="showPlanDialog = false"
      @saved="loadPlans"
    />

    <ConfirmDialog
      :show="showDeletePlanDialog"
      :title="t('payment.admin.deletePlan')"
      :message="t('payment.admin.deletePlanConfirm')"
      :confirm-text="t('common.delete')"
      danger
      @confirm="handleDeletePlan"
      @cancel="showDeletePlanDialog = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminPaymentAPI } from '@/api/admin/payment'
import type { AdminPaymentConfig } from '@/api/admin/payment'
import { extractI18nErrorMessage } from '@/utils/apiError'
import type { SubscriptionPlan } from '@/api/payment.types'
import type { Column } from '@/components/common/table/types'
import Toggle from '@/components/common/Toggle.vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/table/DataTable.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import PlanEditDialog from './PlanEditDialog.vue'
import { currencySymbol } from '@/utils/payment/currency'

// AdminPaymentPlansView.vue lists subscription plans and coordinates editing, sale
// availability, and deletion. Plan prices use each plan's currency with USD for legacy
// empty values, while optional payment configuration supplies the edit dialog's conversion
// preview.

// t resolves the locale labels used by payment controls, validation messages, and
// displayed order information.
const { t } = useI18n()
// appStore provides page feedback for completed operations and request failures while
// handlers retain their local view state.
const appStore = useAppStore()

// planCurrencySymbol resolves the symbol for a plan's configured currency code. An empty
// or absent code is interpreted as USD for legacy plan prices displayed in the management
// table.
function planCurrencySymbol(currency?: string): string {
  return currencySymbol(currency || 'USD')
}

// paymentConfig holds optional conversion settings for the plan editor preview; list
// management remains usable if loading fails.
const paymentConfig = ref<AdminPaymentConfig | null>(null)

// loadPaymentConfig loads conversion settings used by the plan editor's price preview.
// Failure is tolerated because plan listing and editing can operate while preview
// configuration remains unavailable.
async function loadPaymentConfig() {
  try {
    const res = await adminPaymentAPI.getConfig()
    paymentConfig.value = res.data
  } catch {
    // Conversion settings serve the optional editor preview; failed loading leaves the
    // plan list and persistence actions usable with configuration unavailable.
  }
}
// plansLoading tracks plan-list requests and controls loading presentation while current
// rows remain available until a response arrives.
const plansLoading = ref(false)
// plans contains the normalized plan rows from the latest successful request, including
// array-form benefits for rendering.
const plans = ref<SubscriptionPlan[]>([])
// showPlanDialog opens the plan editor with either the selected record or a null selection
// for a new plan.
const showPlanDialog = ref(false)
// showDeletePlanDialog controls deletion confirmation for the separately stored plan
// identifier and closes after deletion succeeds.
const showDeletePlanDialog = ref(false)
// editingPlan supplies the editor record; null requests a new draft and an existing record
// selects update persistence.
const editingPlan = ref<SubscriptionPlan | null>(null)
// deletingPlanId holds the identifier selected by the deletion action and is read when the
// administrator confirms removal.
const deletingPlanId = ref<number | null>(null)

// planColumns describes the translated subscription table columns whose cells render
// pricing, quota, sale status, and management actions.
const planColumns = computed((): Column[] => [
  { key: 'id', label: 'ID' },
  { key: 'name', label: t('payment.admin.planName') },
  { key: 'price', label: t('payment.admin.price') },
  { key: 'validity_days', label: t('payment.admin.validity') },
  { key: 'quota', label: t('payment.admin.quota') },
  { key: 'for_sale', label: t('payment.admin.forSale') },
  { key: 'sort_order', label: t('payment.admin.sortOrder') },
  { key: 'actions', label: t('common.actions') },
])

// parsePlanFeatures converts legacy newline-separated benefits into the list consumed by
// plan rows. Array input passes through, empty values become an empty list, and string
// entries are trimmed and filtered.
function parsePlanFeatures(
  plan: Omit<SubscriptionPlan, 'features'> & {
    features: string | string[] /* stored plan benefit entries */
  },
): SubscriptionPlan {
  return {
    ...plan,
    features:
      // Legacy plan responses encode benefits as newline-separated text; normalize that
      // representation before table rendering so current and legacy records share list
      // handling.
      typeof plan.features === 'string'
        ? plan.features
            .split('\n')
            .map((feature) => feature.trim())
            .filter(Boolean)
        : plan.features || [],
  }
}

// formatValidityUnit resolves singular or plural stored duration units to localized
// labels. Week, month, and year are recognized, while other values use the day label in
// the plan table.
function formatValidityUnit(unit: string): string {
  if (unit === 'month' || unit === 'months') return t('payment.admin.months')
  if (unit === 'year' || unit === 'years') return t('payment.admin.years')
  if (unit === 'week' || unit === 'weeks') return t('payment.admin.weeks')
  return t('payment.admin.days')
}

// loadPlans fetches subscription plans and normalizes their benefits for table rendering.
// It retains existing rows when the request fails and releases the loading indicator after
// either result.
async function loadPlans() {
  plansLoading.value = true
  try {
    const response = await adminPaymentAPI.getPlans()
    plans.value = (response.data || []).map((plan) =>
      parsePlanFeatures(
        plan as Omit<SubscriptionPlan, 'features'> & {
          features: string | string[] /* stored plan benefit entries */
        },
      ),
    )
  } catch (error: unknown) {
    appStore.showError(extractI18nErrorMessage(error, t, 'payment.errors', t('common.error')))
  } finally {
    plansLoading.value = false
  }
}

// openPlanEdit opens the editor for an existing plan or a new draft. Passing null selects
// creation, and the dialog initializes its form when its show prop becomes true.
function openPlanEdit(plan: SubscriptionPlan | null) {
  editingPlan.value = plan
  showPlanDialog.value = true
}

// toggleForSale persists the inverse of the plan's current sale flag. The local row
// changes after the update succeeds, while errors leave its current value visible and
// report the failure.
async function toggleForSale(plan: SubscriptionPlan) {
  try {
    // Persist the requested availability before changing the visible row so a failed
    // update retains the currently displayed sale state.
    await adminPaymentAPI.updatePlan(plan.id, { for_sale: !plan.for_sale })
    plan.for_sale = !plan.for_sale
  } catch (error: unknown) {
    appStore.showError(extractI18nErrorMessage(error, t, 'payment.errors', t('common.error')))
  }
}

// confirmDeletePlan selects the plan identifier and opens its deletion confirmation
// dialog.
function confirmDeletePlan(plan: SubscriptionPlan) {
  deletingPlanId.value = plan.id
  showDeletePlanDialog.value = true
}

// handleDeletePlan deletes the selected plan after confirmation and refreshes the table.
// It returns when no identifier is selected, closes confirmation after deletion succeeds,
// and reports request errors through the store.
async function handleDeletePlan() {
  if (!deletingPlanId.value) return
  try {
    // Delete the selected record before closing confirmation and reloading rows, so
    // request failures keep the target available for administrator review.
    await adminPaymentAPI.deletePlan(deletingPlanId.value)
    appStore.showSuccess(t('common.deleted'))
    showDeletePlanDialog.value = false
    await loadPlans()
  } catch (error: unknown) {
    appStore.showError(extractI18nErrorMessage(error, t, 'payment.errors', t('common.error')))
  }
}

onMounted(() => {
  void loadPaymentConfig()
  loadPlans()
})
</script>
