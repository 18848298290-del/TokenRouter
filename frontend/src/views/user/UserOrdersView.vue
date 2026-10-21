<template>
  <AppLayout>
    <TablePageLayout>
      <!-- Place filters directly in the shared list toolbar so its spacing matches neighboring list pages. -->
      <template #filters>
        <div class="flex flex-wrap items-center gap-2">
          <Select
            v-model="currentFilter"
            :options="statusFilters"
            class="w-36"
            @change="fetchOrders"
          />
          <div class="flex flex-1 items-center justify-end gap-2">
            <button
              @click="fetchOrders"
              :disabled="loading"
              class="btn btn-secondary shrink-0 btn-icon"
              :title="t('common.refresh')"
            >
              <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            </button>
            <button class="btn btn-primary" @click="router.push('/purchase')">
              {{ t('payment.result.backToRecharge') }}
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <OrderTable :orders="orders" :loading="loading">
          <template #actions="{ row }">
            <div class="flex items-center gap-2">
              <button
                v-if="row.status === 'PENDING'"
                @click="handleCancel(row.id)"
                class="inline-flex items-center gap-1 rounded-compact px-2 py-1 text-xs font-medium text-yellow-600 hover:bg-yellow-50 dark:text-yellow-400 dark:hover:bg-yellow-900/20"
              >
                <Icon name="x" size="sm" />
                <span>{{ t('payment.orders.cancel') }}</span>
              </button>
              <button
                v-if="canRequestRefund(row)"
                @click="openRefundDialog(row)"
                class="inline-flex items-center gap-1 rounded-compact px-2 py-1 text-xs font-medium text-purple-600 hover:bg-purple-50 dark:text-purple-400 dark:hover:bg-purple-900/20"
              >
                <Icon name="dollar" size="sm" />
                <span>{{ t('payment.orders.requestRefund') }}</span>
              </button>
              <button
                v-if="canOpenInvoice(row)"
                @click="openInvoice(row)"
                class="inline-flex items-center gap-1 rounded-compact px-2 py-1 text-xs font-medium text-primary-600 hover:bg-primary-50 dark:text-primary-500 dark:hover:bg-primary-500/8"
              >
                <Icon name="document" size="sm" />
                <span>{{ t('payment.orders.invoice') }}</span>
              </button>
            </div>
          </template>
        </OrderTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <!-- Keep the selected cancellation target until confirmation succeeds or the user closes this dialog. -->
    <BaseDialog
      :show="!!cancelTargetId"
      :title="t('payment.orders.cancel')"
      width="narrow"
      @close="cancelTargetId = null"
    >
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('payment.confirmCancel') }}</p>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button class="btn btn-secondary" @click="cancelTargetId = null">
            {{ t('common.cancel') }}
          </button>
          <button class="btn btn-danger" :disabled="actionLoading" @click="confirmCancel">
            {{ actionLoading ? t('common.processing') : t('payment.orders.cancel') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- The refund dialog holds its own selected order and requires a nonblank reason before submission. -->
    <BaseDialog
      :show="!!refundTarget"
      :title="t('payment.orders.requestRefund')"
      @close="refundTarget = null"
    >
      <div v-if="refundTarget" class="space-y-4">
        <div class="rounded-surface bg-gray-50 p-4 dark:bg-dark-800">
          <div class="flex justify-between text-sm">
            <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.orderId') }}</span>
            <span class="font-mono text-gray-900 dark:text-white">#{{ refundTarget.id }}</span>
          </div>
          <div class="mt-2 flex justify-between text-sm">
            <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.amount') }}</span>
            <span class="text-gray-900 dark:text-white">{{
              formatOrderAmount(refundTarget.amount, refundTarget)
            }}</span>
          </div>
        </div>
        <div>
          <label class="input-label">{{ t('payment.refundReason') }}</label>
          <textarea
            v-model="refundReason"
            rows="3"
            class="input mt-1 w-full"
            :placeholder="t('payment.refundReasonPlaceholder')"
          />
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button class="btn btn-secondary" @click="refundTarget = null">
            {{ t('common.cancel') }}
          </button>
          <button
            class="btn btn-primary"
            :disabled="actionLoading || !refundReason.trim()"
            @click="confirmRefund"
          >
            {{ actionLoading ? t('common.processing') : t('payment.orders.requestRefund') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAppStore } from '@/stores'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { paymentAPI } from '@/api/payment'
import { extractI18nErrorMessage } from '@/utils/apiError'
import type { PaymentOrder } from '@/api/payment.types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import OrderTable from '@/components/payment/OrderTable.vue'
import { formatPaymentAmount } from '@/utils/payment/currency'

// UserOrdersView.vue coordinates the current user's order list and its available
// cancellation, refund, and invoice actions. Status filters and pagination feed
// the list request, while order type, completion state, and a separately loaded
// provider set control refund button visibility. Cancellation and refund targets
// remain page-local until their request succeeds or the corresponding dialog closes.
//
// List loading and mutation loading use separate flags. Successful mutations
// close their dialog and await a list refresh, whose errors are reported through
// the normal list path. Invoice requests select the first available document URL
// and open it in a separate browsing context. Requests retain their existing
// independent completion behavior; the page does not cancel in-flight list or
// eligibility reads when filters change or the page unmounts.

// Page services provide translated labels, navigation, feedback, and the account's
// selected balance display unit for balance-type order amounts.
const { t } = useI18n()
const router = useRouter()
const appStore = useAppStore()
const { formatBalanceAmount } = useBalanceDisplay()

// Loading flags independently indicate list requests and order actions, allowing
// the toolbar and confirmation controls to use their existing disabled states.
const loading = ref(false)
const actionLoading = ref(false)
// List results, provider eligibility, and the selected status determine the
// visible rows and the refund actions offered for those rows.
const orders = ref<PaymentOrder[]>([])
const refundEligibleProviders = ref<Set<string>>(new Set())
const currentFilter = ref('')
// Dialog targets retain the chosen order or identifier; each refund selection
// starts with an empty reason before the user confirms the request.
const cancelTargetId = ref<number | null>(null)
const refundTarget = ref<PaymentOrder | null>(null)
const refundReason = ref('')
// pagination retains the requested page size and backend total for the shared
// pager; changing page size restarts the table at its first page.
const pagination = reactive({ page: 1, page_size: 20, total: 0 })

// statusFilters rebuilds translated status choices without changing their API
// values; the empty selection omits the status constraint from list requests.
const statusFilters = computed(() => [
  { value: '', label: t('common.all') },
  { value: 'PENDING', label: t('payment.status.pending') },
  { value: 'PROCESSING', label: t('payment.status.processing') },
  { value: 'COMPLETED', label: t('payment.status.completed') },
  { value: 'FAILED', label: t('payment.status.failed') },
  { value: 'REFUNDED', label: t('payment.status.refunded') },
])

// fetchOrders loads the selected page and status into the visible table.
// Failures keep existing rows and report an error. Concurrent requests can each
// publish their result and clear loading when they finish.
async function fetchOrders() {
  loading.value = true
  try {
    const res = await paymentAPI.getMyOrders({
      page: pagination.page,
      page_size: pagination.page_size,
      status: currentFilter.value || undefined,
    })
    orders.value = res.data.items || []
    pagination.total = res.data.total || 0
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    loading.value = false
  }
}

// handlePageChange selects the requested page and starts its order reload.
function handlePageChange(page: number) {
  pagination.page = page
  fetchOrders()
}
// handlePageSizeChange resets pagination before requesting orders with the new size.
function handlePageSizeChange(size: number) {
  pagination.page_size = size
  pagination.page = 1
  fetchOrders()
}

// handleCancel selects an order for the cancellation confirmation dialog.
function handleCancel(orderId: number) {
  cancelTargetId.value = orderId
}

// confirmCancel submits the currently selected order for cancellation.
// Success closes the dialog and refreshes rows; failure retains the target for
// retry. The action loading flag spans the mutation and awaited refresh.
async function confirmCancel() {
  if (!cancelTargetId.value) return
  actionLoading.value = true
  try {
    await paymentAPI.cancelOrder(cancelTargetId.value)
    appStore.showSuccess(t('common.success'))
    cancelTargetId.value = null
    await fetchOrders()
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    actionLoading.value = false
  }
}

// openRefundDialog selects an order and clears the previous refund reason.
function openRefundDialog(order: PaymentOrder) {
  refundTarget.value = order
  refundReason.value = ''
}

// formatOrderAmount applies the account display unit to balance-type orders.
// Subscription quantities use their saved price currency, with the payment
// currency retained for historical responses that lack a separate price unit.
function formatOrderAmount(amount: number, order: PaymentOrder): string {
  return order.order_type === 'balance'
    ? formatBalanceAmount(amount, { fractionDigits: 2 })
    : formatGatewayAmount(amount, order.amount_currency || order.currency)
}

// formatGatewayAmount delegates currency fallback and precision to the payment formatter.
function formatGatewayAmount(amount: number, currency?: string | null): string {
  return formatPaymentAmount(amount, currency)
}

// confirmRefund sends a trimmed reason for the selected refund target.
// Empty reasons do nothing. Success clears the dialog and reloads orders, while
// a failed request retains its target and reason for retry.
async function confirmRefund() {
  if (!refundTarget.value || !refundReason.value.trim()) return
  actionLoading.value = true
  try {
    await paymentAPI.requestRefund(refundTarget.value.id, { reason: refundReason.value.trim() })
    appStore.showSuccess(t('common.success'))
    refundTarget.value = null
    refundReason.value = ''
    await fetchOrders()
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    actionLoading.value = false
  }
}

// canRequestRefund exposes refunds for completed balance orders from eligible providers.
// The order-type restriction follows the backend refund rule. Missing provider
// identity or an unavailable eligibility response leaves the action hidden.
function canRequestRefund(order: PaymentOrder): boolean {
  if (order.order_type !== 'balance') return false
  if (order.status !== 'COMPLETED') return false
  if (!order.provider_instance_id) return false
  return refundEligibleProviders.value.has(order.provider_instance_id)
}

// canOpenInvoice limits document actions to Stripe orders in accepted paid states.
// The check trims the payment type but retains its case-sensitive comparison and
// includes both fully refunded and partially refunded order statuses.
function canOpenInvoice(order: PaymentOrder): boolean {
  if ((order.payment_type || '').trim() !== 'stripe') return false
  return ['PAID', 'RECHARGING', 'COMPLETED', 'PARTIALLY_REFUNDED', 'REFUNDED'].includes(
    order.status,
  )
}

// openInvoice fetches the order's document and opens its first available URL.
// Selection prefers the general URL, hosted invoice, PDF, then receipt. Missing
// documents and request failures show translated feedback and release action loading.
async function openInvoice(order: PaymentOrder) {
  actionLoading.value = true
  try {
    const res = await paymentAPI.getOrderInvoice(order.id)
    const doc = res.data
    const url = doc.url || doc.hosted_invoice_url || doc.invoice_pdf || doc.receipt_url
    if (!url) {
      appStore.showError(t('payment.errors.PAYMENT_DOCUMENT_NOT_FOUND'))
      return
    }
    window.open(url, '_blank', 'noopener,noreferrer')
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    actionLoading.value = false
  }
}

// loadRefundEligibility fills the provider-ID set used to show refund actions.
// Failed reads preserve the current set; on the initial load that set is empty,
// so no refund button appears without a successful eligibility response.
async function loadRefundEligibility() {
  try {
    const res = await paymentAPI.getRefundEligibleProviders()
    refundEligibleProviders.value = new Set(res.data.provider_instance_ids || [])
  } catch {
    // Keep existing eligibility when loading fails; the initial empty set hides
    // refund actions until a successful response identifies supported providers.
  }
}

// Load the table and eligibility independently so either response can become
// visible without waiting for the other request to finish.
onMounted(() => {
  fetchOrders()
  loadRefundEligibility()
})
</script>
