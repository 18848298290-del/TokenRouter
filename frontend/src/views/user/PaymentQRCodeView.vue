<template>
  <AppLayout>
    <div class="mx-auto flex max-w-md flex-col items-center space-y-6 py-8">
      <h2 class="text-xl font-semibold text-gray-900 dark:text-white">
        {{ qrUrl ? scanTitle : t('payment.qr.payInNewWindow') }}
      </h2>
      <div v-if="qrUrl" class="rounded-surface bg-white p-6 shadow-lg dark:bg-dark-900">
        <canvas ref="qrCanvas" class="mx-auto"></canvas>
      </div>
      <!-- Scan prompt for QR code -->
      <p
        v-if="qrUrl && !expired && scanHint"
        class="text-center text-sm text-gray-500 dark:text-gray-400"
      >
        {{ scanHint }}
      </p>
      <div v-if="expired" class="text-center">
        <p class="text-lg font-medium text-red-500">{{ t('payment.qr.expired') }}</p>
        <button class="btn btn-primary mt-4" @click="router.push('/purchase')">
          {{ t('payment.result.backToRecharge') }}
        </button>
      </div>
      <div v-else class="text-center">
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{ qrUrl ? t('payment.qr.expiresIn') : t('payment.qr.payInNewWindowHint') }}
        </p>
        <p class="mt-1 text-2xl font-bold tabular-nums text-gray-900 dark:text-white">
          {{ countdownDisplay }}
        </p>
        <p class="mt-2 text-sm text-gray-400 dark:text-gray-500">
          {{ t('payment.qr.waitingPayment') }}
        </p>
      </div>
      <a
        v-if="payUrl && !qrUrl && !expired"
        :href="payUrl"
        target="_blank"
        rel="noopener noreferrer"
        class="btn btn-primary w-full py-0"
      >
        {{ t('payment.qr.openPayWindow') }}
      </a>
      <!-- Cancel button -->
      <button
        v-if="!expired && orderId"
        class="btn btn-secondary w-full"
        :disabled="cancelling"
        @click="handleCancel"
      >
        {{ cancelling ? t('common.processing') : t('payment.qr.cancelOrder') }}
      </button>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import { usePaymentStore } from '@/stores/payment'
import { paymentAPI } from '@/api/payment'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores'
import { isBuiltInAlipayMethod, isBuiltInWxpayMethod } from '@/utils/payment/paymentMethods'
import QRCode from 'qrcode'
import alipayIcon from '@/assets/icons/alipay.svg'
import wxpayIcon from '@/assets/icons/wxpay.svg'

/**
 * PaymentQRCodeView.vue renders a route-provided QR checkout and tracks its countdown,
 * cancellation, and order status. The QR page reads its order identifier and payment
 * instructions from the route, then polls the application order while the user completes
 * payment.
 *
 * The countdown defaults to thirty minutes when no expiry is supplied, and terminal
 * statuses stop its timers. Status requests share an in-flight guard, and responses
 * arriving after polling cleanup cannot trigger terminal navigation. This page recognizes
 * COMPLETED and PAID as success; its polling rules remain separate from the shared
 * purchase waiting panel. A method logo is drawn over the QR after image loading.
 */

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const paymentStore = usePaymentStore()
const appStore = useAppStore()

// Route-derived payment data drives the QR canvas and countdown, while this page owns
// cancellation and terminal navigation state.
const qrCanvas = ref<HTMLCanvasElement | null>(null)
const qrUrl = ref('')
const payUrl = ref('')
const orderId = ref(0)
const remainingSeconds = ref(0)
const expired = ref(false)
const cancelling = ref(false)
const paymentType = ref('')

// Polling and countdown intervals belong to the QR page and are released together before
// terminal navigation or component teardown.
let pollTimer: ReturnType<typeof setInterval> | null = null
let countdownTimer: ReturnType<typeof setInterval> | null = null

// QR presentation derives its time label, wallet branding, and scanning instructions from the
// current route-provided order data.
const countdownDisplay = computed(() => {
  const m = Math.floor(remainingSeconds.value / 60)
  const s = remainingSeconds.value % 60
  return m.toString().padStart(2, '0') + ':' + s.toString().padStart(2, '0')
})

const isAlipay = computed(() => isBuiltInAlipayMethod(paymentType.value))
const isWxpay = computed(() => isBuiltInWxpayMethod(paymentType.value))

const scanTitle = computed(() => {
  if (isAlipay.value) return t('payment.qr.scanAlipay')
  if (isWxpay.value) return t('payment.qr.scanWxpay')
  return t('payment.qr.scanToPay')
})

const scanHint = computed(() => {
  if (isAlipay.value) return t('payment.qr.scanAlipayHint')
  if (isWxpay.value) return t('payment.qr.scanWxpayHint')
  return ''
})

/**
 * getLogoForType returns the exact built-in wallet logo or null.
 */
function getLogoForType(): string | null {
  if (isAlipay.value) return alipayIcon
  if (isWxpay.value) return wxpayIcon
  return null
}

/**
 * renderQR waits for the canvas to exist before drawing the payment payload. Known wallet
 * logos select medium QR error correction and load an overlay image asynchronously.
 * Callers can invoke it after a QR change; an absent canvas or payload leaves the current
 * drawing unchanged.
 */
async function renderQR() {
  await nextTick()
  if (!qrCanvas.value || !qrUrl.value) return

  // Use medium error correction to support logo overlay while keeping QR code scannable
  const logoSrc = getLogoForType()
  await QRCode.toCanvas(qrCanvas.value, qrUrl.value, {
    width: 256,
    margin: 2,
    errorCorrectionLevel: logoSrc ? 'M' : 'L',
  })

  if (!logoSrc) return

  // Draw logo in center of QR code
  const canvas = qrCanvas.value
  const ctx = canvas.getContext('2d')
  if (!ctx) return

  const img = new Image()
  img.src = logoSrc
  img.onload = () => {
    // The wallet logo occupies a fixed pixel square inside the QR image, using the error-
    // correction margin selected above.
    const logoSize = 48
    const x = (canvas.width - logoSize) / 2
    const y = (canvas.height - logoSize) / 2
    // White background with rounded-compact corners
    // Padding keeps a white border around the wallet logo so underlying QR modules do not touch
    // its edges.
    const pad = 5
    ctx.fillStyle = '#FFFFFF'
    ctx.beginPath()
    // The background uses a six-pixel corner radius.
    const r = 6
    ctx.moveTo(x - pad + r, y - pad)
    ctx.arcTo(x + logoSize + pad, y - pad, x + logoSize + pad, y + logoSize + pad, r)
    ctx.arcTo(x + logoSize + pad, y + logoSize + pad, x - pad, y + logoSize + pad, r)
    ctx.arcTo(x - pad, y + logoSize + pad, x - pad, y - pad, r)
    ctx.arcTo(x - pad, y - pad, x + logoSize + pad, y - pad, r)
    ctx.fill()
    // Draw logo
    ctx.drawImage(img, x, y, logoSize, logoSize)
  }
}

// pollInFlight belongs to this page and rejects overlapping status requests until the active
// lookup finishes and releases the guard.
let pollInFlight = false
/**
 * pollStatus fetches the active order while rejecting overlapping interval requests. After
 * awaiting the server response, it checks whether polling is still installed before
 * applying terminal navigation. COMPLETED and PAID move to the result page, while expired,
 * cancelled, and failed states stop both timers and mark the QR expired. The request guard
 * is reset in finally so subsequent polling remains possible when the server returns no
 * order or throws.
 */
async function pollStatus() {
  if (!orderId.value) return
  // A status request can exceed the three-second interval, so reject overlapping ticks that
  // could produce duplicate terminal navigation.
  if (pollInFlight) return
  pollInFlight = true
  try {
    const order = await paymentStore.pollOrderStatus(orderId.value)
    if (!order) return
    // A response can arrive after cleanup clears polling; discard that response before it can
    // navigate away from the user's current page.
    if (!pollTimer) return
    if (order.status === 'COMPLETED' || order.status === 'PAID') {
      cleanup()
      router.push({
        path: '/payment/result',
        query: { order_id: String(orderId.value), status: 'success' },
      })
    } else if (
      order.status === 'EXPIRED' ||
      order.status === 'CANCELLED' ||
      order.status === 'FAILED'
    ) {
      cleanup()
      expired.value = true
    }
  } finally {
    pollInFlight = false
  }
}

/**
 * startCountdown starts a one-second display timer from a supplied duration in seconds.
 * Nonpositive durations mark the QR expired immediately; reaching zero while running also
 * releases both countdown and polling timers.
 */
function startCountdown(seconds: number) {
  remainingSeconds.value = Math.max(0, seconds)
  if (remainingSeconds.value <= 0) {
    expired.value = true
    return
  }
  countdownTimer = setInterval(() => {
    remainingSeconds.value--
    if (remainingSeconds.value <= 0) {
      expired.value = true
      cleanup()
    }
  }, 1000)
}

/**
 * handleCancel requests cancellation once while its previous request is pending. A
 * successful response releases timers and navigates to purchase, while an error leaves the
 * QR page available and displays the API message.
 */
async function handleCancel() {
  if (!orderId.value || cancelling.value) return
  cancelling.value = true
  try {
    await paymentAPI.cancelOrder(orderId.value)
    cleanup()
    router.push('/purchase')
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    cancelling.value = false
  }
}

/**
 * cleanup releases the polling and countdown intervals and clears their handles. Poll
 * responses use the cleared polling handle to reject terminal navigation after cleanup;
 * the current request itself is not aborted.
 */
function cleanup() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  if (countdownTimer) {
    clearInterval(countdownTimer)
    countdownTimer = null
  }
}

watch(qrUrl, () => renderQR())

onMounted(() => {
  orderId.value = Number(route.query.order_id) || 0
  qrUrl.value = String(route.query.qr || '')
  payUrl.value = String(route.query.pay_url || '')
  paymentType.value = String(route.query.payment_type || '')

  // Calculate countdown from expiresAt
  const expiresAtStr = String(route.query.expires_at || '')
  // An absent gateway expiry selects thirty minutes for the countdown display.
  let seconds = 30 * 60
  if (expiresAtStr) {
    const expiresAt = new Date(expiresAtStr)
    const now = new Date()
    // Convert the expiry difference from milliseconds to whole countdown seconds, discarding the
    // remaining fraction of a second.
    seconds = Math.floor((expiresAt.getTime() - now.getTime()) / 1000)
  }
  startCountdown(seconds)
  pollTimer = setInterval(pollStatus, 3000)
  renderQR()
})

onUnmounted(() => cleanup())
</script>
