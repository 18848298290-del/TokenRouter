<template>
  <component
    :is="isPopup ? 'div' : AppLayout"
    :class="isPopup ? 'min-h-screen bg-gray-50 dark:bg-dark-900' : ''"
  >
    <div
      v-content-reveal="isPopup && route.path"
      class="mx-auto max-w-lg space-y-4 py-8"
      :class="isPopup ? 'px-4' : ''"
    >
      <div
        v-if="loading"
        class="space-y-4"
        role="status"
        :aria-label="t('common.loading')"
        aria-busy="true"
        data-loading-skeleton
      >
        <div class="card space-y-3 p-6" aria-hidden="true">
          <Skeleton width="35%" :height="16" class="mx-auto" />
          <Skeleton width="55%" :height="36" class="mx-auto" />
        </div>
        <ContentSkeleton variant="form" :rows="3" class="card p-6" />
      </div>
      <div v-else-if="initError" class="card p-6 text-center">
        <div
          class="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-red-100 dark:bg-red-900/30"
        >
          <Icon name="exclamationCircle" size="xl" class="text-red-500" />
        </div>
        <h3 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('payment.stripeLoadFailed') }}
        </h3>
        <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ initError }}</p>
        <button class="btn btn-primary mt-6" @click="router.push('/purchase')">
          {{ t('payment.result.backToRecharge') }}
        </button>
      </div>
      <template v-else>
        <!-- Order amount heading. -->
        <div v-if="order" class="card overflow-hidden">
          <div class="bg-gradient-to-br from-[#635bff] to-[#4f46e5] px-6 py-6 text-center">
            <p class="text-sm font-medium text-indigo-200">{{ t('payment.actualPay') }}</p>
            <p class="mt-1 text-3xl font-bold text-white">
              {{ formatGatewayAmount(order.pay_amount) }}
            </p>
          </div>
        </div>

        <!-- Gateway processing state. -->
        <template v-if="paymentProcessing">
          <div class="card p-6">
            <div class="flex flex-col items-center space-y-4 py-6 text-center">
              <div class="spinner h-10 w-10 text-cyan-500"></div>
              <p class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ t('payment.result.processing') }}
              </p>
              <p class="text-sm text-gray-500 dark:text-gray-400">
                {{ t('payment.result.processingHint') }}
              </p>
            </div>
          </div>
        </template>

        <!-- WeChat QR instructions. -->
        <template v-else-if="wechatQrUrl">
          <div class="card p-6">
            <div class="flex flex-col items-center space-y-4">
              <p class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ t('payment.qr.scanWxpay') }}
              </p>
              <div
                class="relative rounded-control border-2 border-[#2BB741] bg-green-50 p-4 dark:border-[#2BB741]/70 dark:bg-green-950/20"
              >
                <img :src="wechatQrUrl" alt="WeChat Pay QR" class="h-56 w-56 rounded-compact" />
                <div class="pointer-events-none absolute inset-0 flex items-center justify-center">
                  <span class="rounded-full bg-[#2BB741] p-2 shadow ring-2 ring-white">
                    <svg class="h-5 w-5 text-white" viewBox="0 0 24 24" fill="currentColor">
                      <path
                        d="M8.691 2.188C3.891 2.188 0 5.476 0 9.53c0 2.212 1.17 4.203 3.002 5.55a.59.59 0 0 1 .213.665l-.39 1.48c-.019.07-.048.141-.048.213 0 .163.13.295.29.295a.326.326 0 0 0 .167-.054l1.903-1.114a.864.864 0 0 1 .717-.098 10.16 10.16 0 0 0 2.837.403c.276 0 .543-.027.811-.05-.857-2.578.157-4.972 1.932-6.446 1.703-1.415 3.882-1.98 5.853-1.838-.576-3.583-4.196-6.348-8.596-6.348zM5.785 5.991c.642 0 1.162.529 1.162 1.18a1.17 1.17 0 0 1-1.162 1.178A1.17 1.17 0 0 1 4.623 7.17c0-.651.52-1.18 1.162-1.18zm5.813 0c.642 0 1.162.529 1.162 1.18a1.17 1.17 0 0 1-1.162 1.178 1.17 1.17 0 0 1-1.162-1.178c0-.651.52-1.18 1.162-1.18zm3.636 4.35c-2.084 0-3.993.672-5.363 1.844-1.188.982-2.004 2.308-2.004 3.862 0 1.207.546 2.355 1.483 3.285.114.113.238.213.358.321l-.105.42c-.021.084-.042.17-.042.253 0 .168.126.258.282.258.065 0 .126-.025.18-.058l1.27-.765a.69.69 0 0 1 .58-.086c.96.282 1.99.437 3.043.437 2.633 0 5.03-.972 6.4-2.5.782-.87 1.258-1.901 1.258-3.006 0-3.328-3.325-6.006-7.34-6.006zm-3.21 3.09c.52 0 .94.429.94.957a.949.949 0 0 1-.94.955.949.949 0 0 1-.94-.955c0-.528.42-.957.94-.957zm4.739 0c.52 0 .94.429.94.957a.949.949 0 0 1-.94.955.949.949 0 0 1-.94-.955c0-.528.42-.957.94-.957z"
                      />
                    </svg>
                  </span>
                </div>
              </div>
              <p class="text-center text-sm text-gray-500 dark:text-gray-400">
                {{ t('payment.qr.scanWxpayHint') }}
              </p>
            </div>
          </div>
          <div class="card p-4 text-center">
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t('payment.qr.waitingPayment') }}
            </p>
          </div>
        </template>

        <!-- Alipay redirect state. -->
        <template v-else-if="redirecting">
          <div class="card p-6">
            <div class="flex flex-col items-center space-y-4 py-4">
              <div class="spinner h-10 w-10 text-[#00AEEF]"></div>
              <p class="text-sm text-gray-500 dark:text-gray-400">
                {{ t('payment.qr.payInNewWindowHint') }}
              </p>
            </div>
          </div>
        </template>

        <!-- Payment success state. -->
        <template v-else-if="stripeSuccess">
          <div class="card p-6 text-center">
            <div class="flex flex-col items-center gap-3 py-4">
              <div
                class="flex h-16 w-16 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/30"
              >
                <Icon name="check" size="lg" class="text-green-500" :animate-on-hover="false" />
              </div>
              <p class="text-lg font-bold text-gray-900 dark:text-white">
                {{ t('payment.result.success') }}
              </p>
              <p class="text-sm text-gray-500 dark:text-gray-400">
                {{ t('payment.stripeSuccessProcessing') }}
              </p>
            </div>
          </div>
        </template>

        <!-- An absent or unknown method uses the complete Payment Element. -->
        <template v-else-if="showPaymentElement">
          <div class="card p-6">
            <div id="stripe-payment-element" class="min-h-[200px]"></div>
            <p v-if="stripeError" class="mt-4 text-sm text-red-600 dark:text-red-400">
              {{ stripeError }}
            </p>
            <button
              class="btn btn-stripe mt-6 w-full py-0 text-base"
              :disabled="stripeSubmitting || !stripeReady"
              @click="handleGenericPay"
            >
              <span v-if="stripeSubmitting" class="flex items-center justify-center gap-2">
                <span class="spinner h-4 w-4"></span>
                {{ t('common.processing') }}
              </span>
              <span v-else>{{ t('payment.stripePay') }}</span>
            </button>
          </div>
          <div class="text-center">
            <button class="btn btn-secondary" @click="router.push('/purchase')">
              {{ t('payment.result.backToRecharge') }}
            </button>
          </div>
        </template>

        <!-- Payment error state. -->
        <div v-if="stripeError && !showPaymentElement" class="card p-4">
          <p class="text-sm text-red-600 dark:text-red-400">{{ stripeError }}</p>
          <button class="btn btn-secondary mt-3 w-full" @click="router.push('/purchase')">
            {{ t('payment.result.backToRecharge') }}
          </button>
        </div>
      </template>
    </div>
  </component>
</template>

<script setup lang="ts">
import Skeleton from '@/components/common/Skeleton.vue'
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { vContentReveal } from '@/directives/contentReveal'

import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { usePaymentStore } from '@/stores/payment'
import { paymentAPI } from '@/api/payment'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { isMobileDevice } from '@/utils/device'
import { useTheme } from '@/composables/useTheme'
import { formatPaymentAmount, normalizePaymentCurrency } from '@/utils/payment/currency'
import {
  PAYMENT_RECOVERY_STORAGE_KEY,
  readPaymentRecoverySnapshot,
} from '@/utils/payment/paymentFlow'
import type { PaymentOrder } from '@/api/payment.types'
import type { Stripe, StripeElements } from '@stripe/stripe-js'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'

/**
 * StripePaymentView.vue initializes Stripe checkout for an existing order and renders
 * either a selected wallet flow or the full Payment Element. Order data supplies currency,
 * while the configured publishable key initializes the SDK after recovery data has been
 * checked for matching identity.
 *
 * Wallet routes can display a WeChat QR code or navigate through Alipay confirmation. QR
 * status polling suppresses overlapping requests and switches to a slower interval for
 * PROCESSING. The UI treats PAID and RECHARGING as payment success while result
 * verification remains responsible for order fulfillment details. Component teardown stops
 * polling and its pending close-or-result timer; the SDK page keeps this lifecycle
 * separate from purchase-page recovery.
 */

const i18n = useI18n()
const { t } = i18n
const route = useRoute()
const router = useRouter()
const paymentStore = usePaymentStore()

// A method query selects the compact wallet checkout presentation and omits the surrounding
// application layout.
const isPopup = computed(() => !!route.query.method)

// The page owns SDK initialization, submission, wallet QR, and success presentation; order
// currency comes from matching recovery and the order API.
const loading = ref(true)
const initError = ref('')
const stripeError = ref('')
const stripeSubmitting = ref(false)
const stripeSuccess = ref(false)
const paymentProcessing = ref(false)
const stripeReady = ref(false)
const order = ref<PaymentOrder | null>(null)
const currency = ref('CNY')
const wechatQrUrl = ref('')
const redirecting = ref(false)
const showPaymentElement = ref(false)

// Stripe and Elements instances remain available for later confirmation and theme updates
// after initialization completes for this page.
let stripeInstance: Stripe | null = null
let elementsInstance: StripeElements | null = null

// Theme changes update the retained Elements instance so the embedded payment form follows the
// application appearance.
const { isDark } = useTheme()
/**
 * stripeAppearance returns the Stripe Elements theme matching the current application
 * theme.
 */
const stripeAppearance = () =>
  ({ theme: isDark.value ? 'night' : 'stripe', variables: { borderRadius: '8px' } }) as const

watch(isDark, () => {
  elementsInstance?.update({ appearance: stripeAppearance() })
})
// The page retains its close-or-result timeout so component teardown can cancel that pending
// navigation after a user leaves checkout.
let redirectTimer: ReturnType<typeof setTimeout> | null = null

onMounted(async () => {
  const orderId = Number(route.query.order_id)
  const clientSecret = String(route.query.client_secret || '')
  const method = String(route.query.method || '')
  const resumeToken =
    typeof route.query.resume_token === 'string' ? route.query.resume_token : undefined

  if (!orderId || !clientSecret) {
    loading.value = false
    initError.value = t('payment.stripeMissingParams')
    return
  }

  try {
    if (typeof window !== 'undefined') {
      const restored = readPaymentRecoverySnapshot(
        window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY),
        { resumeToken },
      )
      if (restored?.orderId === orderId) {
        currency.value = normalizePaymentCurrency(restored.currency)
      }
    }
    const res = await paymentAPI.getOrder(orderId)
    order.value = res.data
    if (res.data.currency) {
      currency.value = normalizePaymentCurrency(res.data.currency)
    }

    await paymentStore.fetchConfig()
    const publishableKey = paymentStore.config?.stripe_publishable_key
    if (!publishableKey) {
      initError.value = t('payment.stripeNotConfigured')
      return
    }

    const { loadStripe } = await import('@stripe/stripe-js/pure')
    const stripe = await loadStripe(publishableKey)
    if (!stripe) {
      initError.value = t('payment.stripeLoadFailed')
      return
    }

    stripeInstance = stripe
    loading.value = false

    // Selected wallet routes invoke their Stripe confirmation directly, keeping the purchase
    // method chosen on the preceding checkout page.
    if (method === 'alipay') {
      await confirmAlipay(stripe, clientSecret, orderId)
    } else if (method === 'wechat_pay') {
      await confirmWechatPay(stripe, clientSecret)
    } else {
      // The full Payment Element lets Stripe offer payment methods when the checkout route did
      // not preselect a wallet.
      showPaymentElement.value = true
      await nextTick()
      mountPaymentElement(stripe, clientSecret)
    }
  } catch (err: unknown) {
    initError.value = extractI18nErrorMessage(
      err,
      t,
      'payment.errors',
      t('payment.stripeLoadFailed'),
    )
  } finally {
    loading.value = false
  }
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

/**
 * formatGatewayAmount renders a charge using this order's currency and locale.
 */
function formatGatewayAmount(value: number): string {
  return formatPaymentAmount(value, currency.value, localeCode.value)
}

/**
 * confirmAlipay requests Stripe's Alipay confirmation for the supplied client secret. The
 * page enters redirecting state before the call; a returned error restores the page and
 * displays its message.
 */
async function confirmAlipay(stripe: Stripe, clientSecret: string, orderId: number) {
  redirecting.value = true
  const returnUrl =
    window.location.origin + '/payment/result?order_id=' + orderId + '&status=success'
  const { error } = await stripe.confirmAlipayPayment(clientSecret, { return_url: returnUrl })
  if (error) {
    redirecting.value = false
    stripeError.value = error.message || t('payment.result.failed')
  }
  // An accepted Alipay confirmation delegates navigation to Stripe, so the page keeps its
  // redirecting presentation until that navigation occurs.
}

/**
 * confirmWechatPay requests a wallet-specific Stripe confirmation using the current device
 * class. A returned QR image becomes the page's payment instruction and starts order
 * polling. An immediately succeeded intent marks success and schedules the close-or-result
 * action; a gateway error or a reply containing neither QR nor success produces a visible
 * failure. The caller provides an already initialized Stripe object and this order's
 * client secret.
 */
async function confirmWechatPay(stripe: Stripe, clientSecret: string) {
  const { paymentIntent, error } = await (
    stripe as Stripe & {
      /**
       * confirmWechatPayPayment requests Stripe WeChat confirmation using this order's
       * secret and client options. Its response can contain an error, a completed intent,
       * or QR continuation data for the owning page.
       */
      confirmWechatPayPayment: (
        cs: string,
        opts: Record<string, unknown>,
      ) => Promise<{
        paymentIntent?: {
          status: string // Stripe intent lifecycle status

          next_action?: {
            wechat_pay_display_qr_code?: {
              image_data_url?: string // QR image data URL
            } // WeChat QR display instruction
          } // Stripe continuation instruction payload
        } // Stripe confirmation result data

        error?: {
          message?: string // gateway response message text
        } // Stripe confirmation failure details
      }>
    }
  ).confirmWechatPayPayment(clientSecret, {
    payment_method_options: { wechat_pay: { client: isMobileDevice() ? 'mobile_web' : 'web' } },
  })

  if (error) {
    stripeError.value = error.message || t('payment.result.failed')
    return
  }

  // The WeChat next_action carries the QR image used to guide payment while local order
  // polling observes settlement.
  const qrData = paymentIntent?.next_action?.wechat_pay_display_qr_code?.image_data_url
  if (qrData) {
    wechatQrUrl.value = qrData
    // The QR remains payable after confirmation returns, so polling observes the server order
    // until its payment state changes.
    startPolling()
  } else if (paymentIntent?.status === 'succeeded') {
    stripeSuccess.value = true
    scheduleClose()
  } else {
    stripeError.value = t('payment.result.failed')
  }
}

/**
 * mountPaymentElement creates and mounts Stripe's full payment form after its host element
 * exists. It retains the Elements instance for theme updates and submission, and enables
 * the submit action after Stripe reports readiness.
 */
function mountPaymentElement(stripe: Stripe, clientSecret: string) {
  const elements = stripe.elements({
    clientSecret,
    appearance: stripeAppearance(),
  })
  elementsInstance = elements
  const paymentElement = elements.create('payment', {
    layout: 'tabs',
    paymentMethodOrder: ['alipay', 'wechat_pay', 'card', 'link'],
  } as Record<string, unknown>)
  paymentElement.mount('#stripe-payment-element')
  paymentElement.on('ready', () => {
    stripeReady.value = true
  })
}

/**
 * handleGenericPay confirms the mounted Payment Element while guarding duplicate
 * submissions. Missing SDK resources leave state unchanged, errors populate the form
 * message, and accepted confirmation schedules popup closing or result navigation.
 */
async function handleGenericPay() {
  if (!stripeInstance || !elementsInstance || stripeSubmitting.value) return
  stripeSubmitting.value = true
  stripeError.value = ''
  try {
    const { error } = await stripeInstance.confirmPayment({
      elements: elementsInstance,
      confirmParams: {
        return_url:
          window.location.origin +
          '/payment/result?order_id=' +
          route.query.order_id +
          '&status=success',
      },
      redirect: 'if_required',
    })
    if (error) {
      stripeError.value = error.message || t('payment.result.failed')
    } else {
      stripeSuccess.value = true
      scheduleClose()
    }
  } catch (err: unknown) {
    stripeError.value = extractI18nErrorMessage(
      err,
      t,
      'payment.errors',
      t('payment.result.failed'),
    )
  } finally {
    stripeSubmitting.value = false
  }
}

// Wallet polling owns one interval and an in-flight guard, with the remembered cadence
// allowing repeated processing observations to reuse the same timer.
let pollTimer: ReturnType<typeof setInterval> | null = null
let pollIntervalMs = 0
let pollInFlight = false

// These millisecond intervals keep ordinary QR checks frequent and reduce request frequency
// after the gateway reports asynchronous processing.
const PENDING_POLL_INTERVAL_MS = 3000
const PROCESSING_POLL_INTERVAL_MS = 15000

/**
 * stopPolling clears the current order polling interval and resets its remembered cadence.
 * Callers use it on terminal statuses and component teardown; clearing an interval does
 * not cancel an in-flight request.
 */
function stopPolling() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = null
  pollIntervalMs = 0
}

/**
 * pollOrderStatus refreshes the routed order while preventing overlapping requests.
 * PROCESSING clears the QR instruction and selects the slower polling interval. COMPLETED,
 * PAID, and RECHARGING stop polling and display payment success before scheduling result
 * navigation or popup closing. Expired, cancelled, and failed orders stop polling and
 * display the corresponding terminal message. The in-flight flag is released in finally,
 * including when the request throws to its interval caller.
 */
async function pollOrderStatus() {
  const orderId = Number(route.query.order_id)
  if (!orderId || pollInFlight) return
  pollInFlight = true
  try {
    const o = await paymentStore.pollOrderStatus(orderId)
    if (!o) return
    if (o.status === 'PROCESSING') {
      paymentProcessing.value = true
      wechatQrUrl.value = ''
      startPolling(PROCESSING_POLL_INTERVAL_MS)
    } else if (o.status === 'COMPLETED' || o.status === 'PAID' || o.status === 'RECHARGING') {
      stopPolling()
      paymentProcessing.value = false
      stripeSuccess.value = true
      wechatQrUrl.value = ''
      scheduleClose()
    } else if (o.status === 'EXPIRED' || o.status === 'CANCELLED' || o.status === 'FAILED') {
      stopPolling()
      paymentProcessing.value = false
      wechatQrUrl.value = ''
      stripeError.value =
        o.status === 'CANCELLED' ? t('payment.qr.cancelled') : t('payment.qr.expired')
    }
  } finally {
    pollInFlight = false
  }
}

/**
 * startPolling installs a polling interval in milliseconds, replacing a different active
 * cadence. Reusing the current interval leaves its timer unchanged; callers stop the timer
 * on settlement or unmount.
 */
function startPolling(intervalMs = PENDING_POLL_INTERVAL_MS) {
  if (pollTimer && pollIntervalMs === intervalMs) return
  if (pollTimer) clearInterval(pollTimer)
  pollIntervalMs = intervalMs
  pollTimer = setInterval(pollOrderStatus, intervalMs)
}

/**
 * scheduleClose waits two seconds before closing a popup or opening the result route. The
 * presence of window.opener chooses the destination, and component teardown clears the
 * stored timeout handle.
 */
function scheduleClose() {
  if (window.opener) {
    redirectTimer = setTimeout(() => {
      window.close()
    }, 2000)
  } else {
    redirectTimer = setTimeout(() => {
      router.push({
        path: '/payment/result',
        query: { order_id: String(route.query.order_id || ''), status: 'success' },
      })
    }, 2000)
  }
}

// Leaving the SDK page cancels its pending close-or-result timeout and polling interval;
// already-started SDK or network promises finish independently.
onUnmounted(() => {
  if (redirectTimer) clearTimeout(redirectTimer)
  stopPolling()
})
</script>
