<template>
  <div class="flex min-h-screen items-center justify-center bg-gray-50 px-4 dark:bg-dark-900">
    <div v-content-reveal="route.path" class="w-full max-w-md space-y-6">
      <!-- Loading -->
      <div
        v-if="loading"
        class="space-y-4"
        role="status"
        :aria-label="t('common.loading')"
        aria-busy="true"
        data-loading-skeleton
      >
        <div class="space-y-4 text-center" aria-hidden="true">
          <Skeleton variant="circle" :width="80" :height="80" class="mx-auto" />
          <Skeleton width="50%" :height="28" class="mx-auto" />
        </div>
        <ContentSkeleton variant="detail" :rows="6" class="card p-6" />
      </div>
      <template v-else>
        <!-- Status Icon -->
        <div class="text-center">
          <div
            v-if="isSuccess"
            class="mx-auto flex h-20 w-20 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/30"
          >
            <Icon
              name="check"
              size="md"
              :animate-on-hover="false"
              class="h-10 w-10 text-green-500"
            />
          </div>
          <div
            v-else-if="isPending"
            class="mx-auto flex h-20 w-20 items-center justify-center rounded-full bg-yellow-100 dark:bg-yellow-900/30"
          >
            <div class="spinner h-10 w-10 text-yellow-500"></div>
          </div>
          <div
            v-else
            class="mx-auto flex h-20 w-20 items-center justify-center rounded-full bg-red-100 dark:bg-red-900/30"
          >
            <Icon name="x" size="md" class="h-10 w-10 text-red-500" />
          </div>
          <h2 class="mt-4 text-2xl font-bold text-gray-900 dark:text-white">
            {{ statusTitle }}
          </h2>
          <p v-if="isPending" class="mt-2 text-sm text-gray-500 dark:text-gray-400">
            {{ t('payment.result.processingHint') }}
          </p>
        </div>
        <!-- Order Info -->
        <div v-if="order" class="rounded-surface bg-white p-5 shadow-sm dark:bg-dark-900">
          <div class="space-y-3 text-sm">
            <div v-if="hasOrderId(order)" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{
                t('payment.orders.orderId')
              }}</span>
              <span class="font-medium text-gray-900 dark:text-white">#{{ order.id }}</span>
            </div>
            <div v-if="order.out_trade_no" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{
                t('payment.orders.orderNo')
              }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{
                order.out_trade_no
              }}</span>
            </div>
            <div v-if="hasAmountFields(order)" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{
                t('payment.orders.baseAmount')
              }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{
                formatGatewayAmount(baseAmount)
              }}</span>
            </div>
            <div v-if="hasAmountFields(order) && order.fee_fixed > 0" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.fixedFee') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{
                formatGatewayAmount(order.fee_fixed)
              }}</span>
            </div>
            <div
              v-if="hasAmountFields(order) && order.fee_rate_amount > 0"
              class="flex justify-between"
            >
              <span class="text-gray-500 dark:text-gray-400"
                >{{ t('payment.rateFee') }} ({{ order.fee_rate }}%)</span
              >
              <span class="font-medium text-gray-900 dark:text-white">{{
                formatGatewayAmount(order.fee_rate_amount)
              }}</span>
            </div>
            <div v-if="hasAmountFields(order) && feeAmount > 0" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.feeTotal') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{
                formatGatewayAmount(feeAmount)
              }}</span>
            </div>
            <div v-if="hasAmountFields(order)" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{
                t('payment.orders.payAmount')
              }}</span>
              <span class="font-bold text-primary-600 dark:text-primary-400">{{
                formatGatewayAmount(order.pay_amount)
              }}</span>
            </div>
            <div
              v-if="
                hasAmountFields(order) &&
                (order.amount !== order.pay_amount ||
                  (order.amount_currency && order.amount_currency !== order.currency))
              "
              class="flex justify-between"
            >
              <span class="text-gray-500 dark:text-gray-400">{{
                t(
                  order.order_type === 'balance'
                    ? 'payment.orders.creditedAmount'
                    : 'payment.orders.amount',
                )
              }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{
                formatOrderAmount(order.amount, order.order_type, order.amount_currency)
              }}</span>
            </div>
            <div v-if="hasPaymentType(order)" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{
                t('payment.orders.paymentMethod')
              }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{
                t(
                  paymentMethodI18nKey(order.payment_type),
                  normalizedOrderPaymentType(order.payment_type),
                )
              }}</span>
            </div>
            <div class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.status') }}</span>
              <OrderStatusBadge :status="displayOrderStatus(order.status)" />
            </div>
          </div>
        </div>
        <!-- EasyPay return info (when no order loaded) -->
        <div v-else-if="returnInfo" class="rounded-surface bg-white p-5 shadow-sm dark:bg-dark-900">
          <div class="space-y-3 text-sm">
            <div v-if="returnInfo.outTradeNo" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{
                t('payment.orders.orderId')
              }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{
                returnInfo.outTradeNo
              }}</span>
            </div>
            <div v-if="returnInfo.money" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{
                t('payment.orders.payAmount')
              }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{
                formatGatewayAmount(Number(returnInfo.money) || 0)
              }}</span>
            </div>
            <div v-if="returnInfo.type" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{
                t('payment.orders.paymentMethod')
              }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{
                t(
                  paymentMethodI18nKey(returnInfo.type),
                  normalizedOrderPaymentType(returnInfo.type),
                )
              }}</span>
            </div>
          </div>
        </div>
        <!-- Actions -->
        <div class="flex gap-3">
          <button class="btn btn-secondary flex-1" @click="router.push('/purchase')">
            {{ t('payment.result.backToRecharge') }}
          </button>
          <button class="btn btn-primary flex-1" @click="router.push('/orders')">
            {{ t('payment.result.viewOrders') }}
          </button>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import Skeleton from '@/components/common/Skeleton.vue'
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { vContentReveal } from '@/directives/contentReveal'

import Icon from '@/components/icons/Icon.vue'
import { ref, computed, onBeforeUnmount, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import OrderStatusBadge from '@/components/payment/OrderStatusBadge.vue'
import {
  PAYMENT_RECOVERY_STORAGE_KEY,
  clearPaymentRecoverySnapshot,
  readPaymentRecoverySnapshot,
} from '@/utils/payment/paymentFlow'
import { usePaymentStore } from '@/stores/payment'
import { useAuthStore } from '@/stores/auth'
import { paymentAPI } from '@/api/payment'
import type { PublicOrderVerifyResult } from '@/api/payment'
import type { OrderStatus, PaymentOrder, PublicPaymentOrder } from '@/api/payment.types'
import { formatPaymentAmount, normalizePaymentCurrency } from '@/utils/payment/currency'
import { normalizePaymentMethodForDisplay, paymentMethodI18nKey } from './payment/paymentUx'

/**
 * PaymentResultView.vue resolves checkout return identifiers into an order result and
 * refreshes pending payment status. Resume tokens, authenticated order IDs, and eligible
 * trade-reference returns use distinct lookup paths. A supplied token alone selects stored
 * recovery by token; routes without tokens compare their supplied order and trade identities.
 *
 * The result view groups PAID and RECHARGING with payment success for presentation, but
 * refreshes account balance only after COMPLETED. PROCESSING keeps a slower refresh
 * cadence while other pending statuses use a bounded number of attempts. Gateway return
 * text can be displayed when an eligible older return cannot be resolved, and stored
 * recovery is cleared when the resolved status leaves the pending set.
 */

const i18n = useI18n()
const { t } = i18n
const { formatBalanceAmount } = useBalanceDisplay()
const route = useRoute()
const router = useRouter()
const paymentStore = usePaymentStore()
const authStore = useAuthStore()

/**
 * PaymentResultOrder includes authenticated and public lookup shapes accepted by this
 * page.
 */
type PaymentResultOrder = PaymentOrder | PublicPaymentOrder | PublicOrderVerifyResult

// The resolved order drives result presentation; currency is seeded from matching recovery and
// replaced when an API response supplies its value.
const order = ref<PaymentResultOrder | null>(null)
const loading = ref(true)
const currency = ref('CNY')

/**
 * ReturnInfo retains display fields from an unresolved older gateway return.
 */
interface ReturnInfo {
  outTradeNo: string // gateway order trade reference

  money: string // gateway return amount text

  type: string // gateway return method code

  tradeStatus: string // gateway return status text
}
const returnInfo = ref<ReturnInfo | null>(null)

// Presentation groups paid and recharging with success, while pending statuses remain
// refreshable; account refresh separately requires completed fulfillment.
const SUCCESS_STATUSES = new Set(['COMPLETED', 'PAID', 'RECHARGING'])
const PENDING_STATUSES = new Set(['PENDING', 'CREATED', 'WAITING', 'PROCESSING'])
const STATUS_REFRESH_INTERVAL_MS = 2000
const PROCESSING_REFRESH_INTERVAL_MS = 15000
const STATUS_REFRESH_MAX_ATTEMPTS = 15

// This page owns the next refresh timeout and account-refresh guard; a timer request already
// in flight follows its existing completion path after unmount.
let statusRefreshTimer: ReturnType<typeof setTimeout> | null = null
let userBalanceRefreshStarted = false
const refreshAttempts = ref(0)

/** baseAmount subtracts an explicit fee amount from the gateway charge. Replies without that fee split reconstruct the base from the stored percentage rate, rounding to two decimals. */
const baseAmount = computed(() => {
  if (!hasAmountFields(order.value)) return 0
  const feeRate = Number(order.value.fee_rate) || 0
  if ((order.value.fee_amount || 0) > 0)
    return Math.round((order.value.pay_amount - order.value.fee_amount) * 100) / 100
  if (feeRate <= 0) return order.value.pay_amount ?? 0
  return Math.round((order.value.pay_amount / (1 + feeRate / 100)) * 100) / 100
})

/** feeAmount prefers the explicit fee split. When the split is absent and a positive rate exists, the displayed fee is the charge minus the reconstructed base amount. */
const feeAmount = computed(() => {
  if (!hasAmountFields(order.value)) return 0
  const feeRate = Number(order.value.fee_rate) || 0
  if ((order.value.fee_amount || 0) > 0) return order.value.fee_amount
  if (feeRate <= 0) return 0
  return Math.round((order.value.pay_amount - baseAmount.value) * 100) / 100
})

const localeCode = computed(() => {
  const raw = i18n.locale as unknown
  if (typeof raw === 'string') return raw
  if (raw && typeof raw === 'object' && 'value' in raw) {
    return String(
      (
        raw as {
          value?: string // reactive locale string value
        }
      ).value || '',
    )
  }
  return undefined
})

const isSuccess = computed(() => {
  return isSuccessStatus(order.value?.status)
})

const isPending = computed(() => {
  return isPendingStatus(order.value?.status)
})

const statusTitle = computed(() => {
  if (isSuccess.value) {
    return t('payment.result.success')
  }
  if (isPending.value) {
    return t('payment.result.processing')
  }
  return t('payment.result.failed')
})

/**
 * normalizedOrderPaymentType resolves the visible identity of a returned payment method.
 * Empty values stay empty, while unknown codes remain available for display through the
 * shared method normalizer.
 */
function normalizedOrderPaymentType(paymentType: string): string {
  return normalizePaymentMethodForDisplay(paymentType || '') || paymentType || ''
}

/**
 * formatGatewayAmount renders a monetary value in the supplied currency or the resolved
 * payment currency. Empty overrides use the payment currency, and locale follows the page.
 */
function formatGatewayAmount(value: number, amountCurrency?: string): string {
  return formatPaymentAmount(value, amountCurrency || currency.value, localeCode.value)
}

/**
 * setResolvedOrder publishes the fetched order and its available currency. It also checks
 * whether this completed order permits the account-balance refresh, keeping that update
 * tied to server-resolved status.
 */
function setResolvedOrder(nextOrder: PaymentResultOrder | null): void {
  order.value = nextOrder
  if (nextOrder && 'currency' in nextOrder && nextOrder.currency) {
    currency.value = normalizePaymentCurrency(nextOrder.currency)
  }
  refreshUserBalanceForSuccessfulOrder(nextOrder)
}

/**
 * refreshUserBalanceForSuccessfulOrder starts one account refresh for a completed balance
 * purchase. PAID and RECHARGING can already display payment success, but they do not
 * qualify because balance fulfillment may still be running. An order carrying another
 * order_type is excluded; public responses without that field remain eligible. The guard
 * is set before the asynchronous refresh, so repeated status observations and a rejected
 * refresh do not start another account request.
 */
function refreshUserBalanceForSuccessfulOrder(nextOrder: PaymentResultOrder | null): void {
  if (
    !nextOrder ||
    userBalanceRefreshStarted ||
    normalizeOrderStatus(nextOrder.status) !== 'COMPLETED'
  ) {
    return
  }
  if ('order_type' in nextOrder && nextOrder.order_type !== 'balance') {
    return
  }

  userBalanceRefreshStarted = true
  void authStore.refreshUser().catch(() => {
    // A profile refresh failure leaves the resolved order authoritative for this result page,
    // so the payment state remains visible.
  })
}

/**
 * hasOrderId reports whether the result exposes a numeric internal order identifier.
 */
function hasOrderId(nextOrder: PaymentResultOrder | null): nextOrder is PaymentOrder {
  return !!nextOrder && 'id' in nextOrder && typeof nextOrder.id === 'number'
}

/**
 * hasAmountFields reports whether the result exposes numeric purchase and charge amounts.
 */
function hasAmountFields(nextOrder: PaymentResultOrder | null): nextOrder is PaymentOrder {
  return (
    !!nextOrder &&
    'pay_amount' in nextOrder &&
    typeof nextOrder.pay_amount === 'number' &&
    'amount' in nextOrder &&
    typeof nextOrder.amount === 'number'
  )
}

/**
 * hasPaymentType reports whether the result exposes a nonempty payment method string.
 */
function hasPaymentType(nextOrder: PaymentResultOrder | null): nextOrder is PaymentOrder {
  return (
    !!nextOrder &&
    'payment_type' in nextOrder &&
    typeof nextOrder.payment_type === 'string' &&
    nextOrder.payment_type.trim() !== ''
  )
}

/**
 * normalizeOrderStatus trims and uppercases a supplied order status. Missing values become
 * an empty string, allowing result classifiers to reject absent status without treating it
 * as any known lifecycle state.
 */
function normalizeOrderStatus(status: string | null | undefined): string {
  return String(status || '')
    .trim()
    .toUpperCase()
}

/**
 * displayOrderStatus normalizes status text and casts it for the order badge. It does not
 * validate membership in the declared status union; the badge owns its neutral
 * presentation for unknown runtime values.
 */
function displayOrderStatus(status: string): OrderStatus {
  return normalizeOrderStatus(status) as OrderStatus
}

/**
 * isSuccessStatus classifies completed, paid, and recharging statuses as payment success.
 */
function isSuccessStatus(status: string | null | undefined): boolean {
  return SUCCESS_STATUSES.has(normalizeOrderStatus(status))
}

/**
 * isPendingStatus classifies statuses that keep result refresh and processing presentation
 * active.
 */
function isPendingStatus(status: string | null | undefined): boolean {
  return PENDING_STATUSES.has(normalizeOrderStatus(status))
}

/**
 * readRouteQueryString reads a scalar query value or the first array value. Non-string
 * values become empty strings, allowing order resolution to distinguish missing
 * identifiers from usable route context.
 */
function readRouteQueryString(key: string): string {
  const value = route.query[key]
  if (Array.isArray(value)) {
    return typeof value[0] === 'string' ? value[0] : ''
  }
  return typeof value === 'string' ? value : ''
}

/**
 * restoreRecoverySnapshot checks browser recovery against available route identity. A
 * supplied resume token selects token matching directly and skips route order-ID and
 * trade-reference comparisons. With no token, an order ID or trade reference is required
 * before storage can contribute fields; each positive route ID and nonempty trade reference
 * must then match the snapshot. Accepted recovery can supply missing currency and order
 * details, so token-based restoration can use a stored order even when other route
 * identifiers differ.
 */
function restoreRecoverySnapshot(context: {
  resumeToken: string // public order recovery token

  routeOrderId: number // route-provided internal order identifier

  routeOutTradeNo: string // route-provided gateway trade reference
}) {
  if (typeof window === 'undefined') {
    return null
  }

  const rawSnapshot = window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)
  if (!rawSnapshot) {
    return null
  }

  if (context.resumeToken) {
    return readPaymentRecoverySnapshot(rawSnapshot, {
      resumeToken: context.resumeToken,
    })
  }

  if (!context.routeOrderId && !context.routeOutTradeNo) {
    return null
  }

  const restored = readPaymentRecoverySnapshot(rawSnapshot)
  if (!restored) {
    return null
  }

  if (context.routeOrderId > 0 && restored.orderId !== context.routeOrderId) {
    return null
  }

  if (context.routeOutTradeNo && restored.outTradeNo !== context.routeOutTradeNo) {
    return null
  }

  return restored
}

/**
 * resolveOrderFromResumeToken fetches a public order using its resume token. Lookup errors
 * return null so the owning result-page sequence can decide whether another route-provided
 * identifier permits recovery.
 */
async function resolveOrderFromResumeToken(
  resumeToken: string,
): Promise<PaymentResultOrder | null> {
  try {
    const result = await paymentAPI.resolveOrderPublicByResumeToken(resumeToken)
    return result.data
  } catch (_err: unknown) {
    // A failed token lookup returns no order so route-provided identifiers can determine whether
    // another resolution path is eligible.
    return null
  }
}

/**
 * resolveOrderFromOutTradeNo tries authenticated trade verification and then public
 * verification. A failure of both lookups returns null, allowing the result page to retain
 * its existing order or eligible return metadata.
 */
async function resolveOrderFromOutTradeNo(outTradeNo: string): Promise<PaymentResultOrder | null> {
  try {
    const result = await paymentAPI.verifyOrder(outTradeNo)
    return result.data
  } catch (_err: unknown) {
    // An authenticated verification failure can occur outside a signed-in session, so this
    // eligible trade-reference path tries public verification next.
    try {
      const result = await paymentAPI.verifyOrderPublic(outTradeNo)
      return result.data
    } catch (_innerErr: unknown) {
      // Both verification paths failed; retaining no resolved order lets the page keep its pending
      // result or older return display.
      return null
    }
  }
}

/**
 * clearStatusRefreshTimer cancels the scheduled result refresh and clears its handle. It
 * does not cancel a request already issued by the timer callback, which completes through
 * that callback's normal path.
 */
function clearStatusRefreshTimer(): void {
  if (statusRefreshTimer !== null) {
    clearTimeout(statusRefreshTimer)
    statusRefreshTimer = null
  }
}

/**
 * clearRecoverySnapshot removes browser checkout recovery after a resolved terminal
 * result. When no browser window exists it does nothing; storage failures propagate to the
 * operation invoking this cleanup.
 */
function clearRecoverySnapshot(): void {
  if (typeof window === 'undefined') return
  clearPaymentRecoverySnapshot(window.localStorage, PAYMENT_RECOVERY_STORAGE_KEY)
}

/**
 * clearRecoverySnapshotForTerminalStatus clears recovery when a nonempty status leaves the
 * pending set. Payment-success statuses qualify even when balance fulfillment is still
 * progressing, matching this page's terminal presentation rule.
 */
function clearRecoverySnapshotForTerminalStatus(status: string | null | undefined): void {
  if (!status) return
  if (!isPendingStatus(status)) {
    clearRecoverySnapshot()
  }
}

/**
 * scheduleStatusRefresh replaces the pending timer with the next eligible status check. It
 * stops when there is no refresh operation, the displayed order is terminal, or the
 * ordinary pending attempt budget is exhausted. PROCESSING uses a slower interval and
 * bypasses that ordinary attempt limit. After a successful lookup the order and recovery
 * cleanup are updated before deciding whether another timer is needed; a null lookup
 * retains the existing displayed state.
 */
function scheduleStatusRefresh(
  refreshOrder: (() => Promise<PaymentResultOrder | null>) | null,
): void {
  clearStatusRefreshTimer()
  const processing = normalizeOrderStatus(order.value?.status) === 'PROCESSING'
  if (
    !refreshOrder ||
    !isPending.value ||
    (!processing && refreshAttempts.value >= STATUS_REFRESH_MAX_ATTEMPTS)
  ) {
    return
  }

  // Schedule the next check after the previous one finishes so a slow lookup does not create
  // overlapping result refresh requests.
  statusRefreshTimer = setTimeout(
    async () => {
      refreshAttempts.value += 1
      const refreshedOrder = await refreshOrder()
      if (refreshedOrder) {
        setResolvedOrder(refreshedOrder)
        clearRecoverySnapshotForTerminalStatus(refreshedOrder.status)
      }

      if (isPendingStatus(order.value?.status)) {
        scheduleStatusRefresh(refreshOrder)
      }
    },
    processing ? PROCESSING_REFRESH_INTERVAL_MS : STATUS_REFRESH_INTERVAL_MS,
  )
}

/**
 * formatOrderAmount formats balance purchases in account units and subscriptions in
 * their saved price currency. An absent price currency uses the resolved payment
 * currency, allowing older order responses to retain their monetary labels.
 */
function formatOrderAmount(amount: number, orderType: string, amountCurrency?: string): string {
  return orderType === 'balance'
    ? formatBalanceAmount(amount, { fractionDigits: 2 })
    : formatGatewayAmount(amount, amountCurrency)
}

onMounted(async () => {
  const resumeToken = readRouteQueryString('resume_token')
  const routeOrderId = Number(readRouteQueryString('order_id')) || 0
  let outTradeNo = readRouteQueryString('out_trade_no')
  let orderId = 0
  let resumeTokenLookupFailed = false

  const restored = restoreRecoverySnapshot({
    resumeToken,
    routeOrderId,
    routeOutTradeNo: outTradeNo,
  })
  if (restored?.orderId) {
    orderId = restored.orderId
  }
  if (restored?.currency) {
    currency.value = normalizePaymentCurrency(restored.currency)
  }
  if (!outTradeNo && restored?.outTradeNo) {
    outTradeNo = restored.outTradeNo
  }

  if (resumeToken) {
    const resolvedOrder = await resolveOrderFromResumeToken(resumeToken)
    if (resolvedOrder) {
      setResolvedOrder(resolvedOrder)
      if (!orderId) {
        orderId = hasOrderId(resolvedOrder) ? resolvedOrder.id : 0
      }
    } else if (routeOrderId > 0) {
      resumeTokenLookupFailed = true
      orderId = routeOrderId
    } else {
      resumeTokenLookupFailed = true
    }
  } else if (routeOrderId > 0) {
    orderId = routeOrderId
  }

  const hasLegacyFallbackContext = readRouteQueryString('trade_status').trim() !== ''
  const shouldUsePublicOutTradeNo =
    outTradeNo !== '' && (hasLegacyFallbackContext || routeOrderId > 0 || orderId > 0)

  if (!order.value && orderId && (!resumeToken || routeOrderId > 0)) {
    try {
      setResolvedOrder(await paymentStore.pollOrderStatus(orderId))
    } catch (_err: unknown) {
      // A failed authenticated lookup leaves resolution available for the eligible trade-
      // reference public lookup later in this initialization sequence.
    }
  }

  if (!order.value && shouldUsePublicOutTradeNo && (!resumeToken || resumeTokenLookupFailed)) {
    const legacyOrder = await resolveOrderFromOutTradeNo(outTradeNo)
    if (legacyOrder) {
      setResolvedOrder(legacyOrder)
      if (!orderId) {
        orderId = hasOrderId(legacyOrder) ? legacyOrder.id : 0
      }
    }
  }

  if (!order.value && !orderId && outTradeNo && hasLegacyFallbackContext) {
    returnInfo.value = {
      outTradeNo,
      money: String(route.query.money || ''),
      type: String(route.query.type || ''),
      tradeStatus: String(route.query.trade_status || ''),
    }
  }

  /**
   * refreshOrder repeats the permitted lookup sequence for this result-page identity. It
   * tries resume-token resolution first, then an eligible authenticated order lookup, then
   * public trade verification when return context permits it.
   */
  const refreshOrder = async (): Promise<PaymentResultOrder | null> => {
    if (resumeToken) {
      const resolvedOrder = await resolveOrderFromResumeToken(resumeToken)
      if (resolvedOrder) {
        return resolvedOrder
      }
    }

    if (orderId) {
      try {
        return await paymentStore.pollOrderStatus(orderId)
      } catch (_err: unknown) {
        // An unavailable authenticated poll can continue through the eligible public trade-
        // reference lookup using the same result-page identity context.
      }
    }

    if (shouldUsePublicOutTradeNo) {
      return await resolveOrderFromOutTradeNo(outTradeNo)
    }

    return null
  }

  if (isPendingStatus(order.value?.status)) {
    scheduleStatusRefresh(refreshOrder)
  } else if (order.value) {
    clearRecoverySnapshotForTerminalStatus(order.value.status)
  } else if (returnInfo.value) {
    clearRecoverySnapshot()
  }
  loading.value = false
})

onBeforeUnmount(() => {
  clearStatusRefreshTimer()
})
</script>
