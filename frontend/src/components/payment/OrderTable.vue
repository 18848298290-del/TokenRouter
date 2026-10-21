<template>
  <DataTable
    column-order-storage-key="user-orders-column-order"
    :columns="columns"
    :data="orders"
    :loading="loading"
  >
    <template #cell-id="{ value }">
      <span class="font-mono text-sm">#{{ value }}</span>
    </template>
    <template #cell-out_trade_no="{ value }">
      <span class="text-sm text-gray-900 dark:text-white">{{ value }}</span>
    </template>
    <template v-if="showUser" #cell-user_email="{ value, row }">
      <div class="text-sm">
        <span class="text-gray-900 dark:text-white">{{
          value || row.user_name || '#' + row.user_id
        }}</span>
        <span v-if="row.user_notes" class="ml-1 text-xs text-gray-400">({{ row.user_notes }})</span>
      </div>
    </template>
    <template #cell-pay_amount="{ value, row }">
      <div class="text-sm">
        <span class="font-medium text-gray-900 dark:text-white">{{
          formatGatewayAmount(value, row.currency)
        }}</span>
        <span
          v-if="row.fee_rate > 0"
          class="ml-1 text-xs text-gray-400"
          :title="t('payment.orders.fee') + ': ' + row.fee_rate + '%'"
        >
          ({{ t('payment.orders.fee') }} {{ row.fee_rate }}%)
        </span>
        <div
          v-if="
            row.amount !== row.pay_amount ||
            (row.amount_currency && row.amount_currency !== row.currency)
          "
          class="text-xs text-gray-500"
        >
          {{
            t(
              row.order_type === 'balance'
                ? 'payment.orders.creditedAmount'
                : 'payment.orders.amount',
            )
          }}: {{ formatOrderAmount(row.amount, row) }}
        </div>
      </div>
    </template>
    <template #cell-payment_type="{ value }">
      <span class="text-sm text-gray-700 dark:text-gray-300">{{
        t('payment.methods.' + value, value)
      }}</span>
    </template>
    <template #cell-status="{ value }">
      <OrderStatusBadge :status="value" />
    </template>
    <template #cell-created_at="{ value }">
      <span class="text-xs text-gray-500 dark:text-gray-400">{{ formatDate(value) }}</span>
    </template>
    <template #cell-actions="{ row }">
      <slot name="actions" :row="row" />
    </template>
  </DataTable>
</template>

<script setup lang="ts">
import { getLocale } from '@/i18n'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PaymentOrder } from '@/api/payment.types'
import type { Column } from '@/components/common/table/types'
import DataTable from '@/components/common/table/DataTable.vue'
import OrderStatusBadge from '@/components/payment/OrderStatusBadge.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { formatPaymentAmount } from '@/utils/payment/currency'

/**
 * OrderTable.vue presents payment orders with currency-aware amounts, status badges, and
 * caller-provided row actions. Balance purchase quantities use the account balance unit
 * while gateway charges and subscription prices retain their respective recorded currencies.
 */

const { t } = useI18n()
const { formatBalanceAmount } = useBalanceDisplay()

const props = defineProps<{
  orders: PaymentOrder[] // current page order records
  loading: boolean // order retrieval loading state
  showUser?: boolean // optional purchaser email column
}>()

/**
 * formatDate renders the order creation timestamp in the active locale.
 */
function formatDate(dateStr: string) {
  return new Date(dateStr).toLocaleString(getLocale())
}

/**
 * formatOrderAmount uses account units for recharge quantities and saved price currency
 * for subscriptions. Responses without a price currency use their recorded payment
 * currency so historical orders remain displayable.
 */
function formatOrderAmount(amount: number, order: PaymentOrder): string {
  return order.order_type === 'balance'
    ? formatBalanceAmount(amount, { fractionDigits: 2 })
    : formatGatewayAmount(amount, order.amount_currency || order.currency)
}

/**
 * formatGatewayAmount formats a gateway charge with the supplied order currency.
 */
function formatGatewayAmount(amount: number, currency?: string | null): string {
  return formatPaymentAmount(amount, currency)
}

// Localized column definitions follow the requested user-column visibility while the parent
// supplies rows, loading, and per-order actions.
const columns = computed((): Column[] => {
  const cols: Column[] = [
    { key: 'id', label: t('payment.orders.orderId') },
    { key: 'out_trade_no', label: t('payment.orders.orderNo') },
  ]
  if (props.showUser) {
    cols.push({ key: 'user_email', label: t('payment.admin.colUser') })
  }
  cols.push(
    { key: 'pay_amount', label: t('payment.orders.payAmount') },
    { key: 'payment_type', label: t('payment.orders.paymentMethod') },
    { key: 'status', label: t('payment.orders.status') },
    { key: 'created_at', label: t('payment.orders.createdAt') },
    { key: 'actions', label: t('common.actions') },
  )
  return cols
})
</script>
