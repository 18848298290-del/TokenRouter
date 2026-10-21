<template>
  <AppLayout>
    <div class="mx-auto max-w-lg space-y-4 py-8">
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

      <div v-else-if="errorMessage" class="card p-6 text-center">
        <div
          class="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-red-100 dark:bg-red-900/30"
        >
          <Icon name="exclamationCircle" size="xl" class="text-red-500" />
        </div>
        <h3 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('payment.airwallexLoadFailed') }}
        </h3>
        <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ errorMessage }}</p>
        <button class="btn btn-primary mt-6" @click="router.push('/purchase')">
          {{ t('payment.result.backToRecharge') }}
        </button>
      </div>

      <div v-else class="card p-6">
        <div class="flex flex-col items-center space-y-4 py-4">
          <div class="spinner h-10 w-10 text-emerald-500"></div>
          <p class="text-sm text-gray-500 dark:text-gray-400">
            {{ t('payment.qr.payInNewWindowHint') }}
          </p>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { vendorLocale } from '@/i18n/catalog'
import Skeleton from '@/components/common/Skeleton.vue'
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  PAYMENT_RECOVERY_STORAGE_KEY,
  readPaymentRecoverySnapshot,
  type PaymentRecoverySnapshot,
} from '@/utils/payment/paymentFlow'

/**
 * AirwallexPaymentView.vue restores an Airwallex checkout intent and delegates its hosted
 * checkout redirect to the Airwallex SDK. The route carries order identity while the
 * stored recovery snapshot holds the client secret, intent identifier, currency, and
 * environment.
 *
 * Restoration checks the visible method and every supplied order identity before using the
 * snapshot. A missing or mismatched snapshot becomes a local error before SDK loading. The
 * SDK receives a result URL built from route identifiers with snapshot values filling
 * absent fields. SDK initialization and redirect errors remain visible on this page,
 * allowing the user to return to purchase selection.
 */

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()

// Loading and errors belong to the redirect page while the validated recovery snapshot
// supplies the SDK intent and browser destination.
const loading = ref(true)
const errorMessage = ref('')

/**
 * queryString reads the first string value of a route query field. Missing or non-string
 * values become empty strings so checkout identity checks can treat them as absent.
 */
function queryString(key: string): string {
  const value = route.query[key]
  if (Array.isArray(value)) return value[0] || ''
  return typeof value === 'string' ? value : ''
}

/**
 * buildSuccessUrl constructs the absolute payment result destination for this checkout.
 * Route values take precedence over snapshot values, and absent identifiers are omitted
 * from the resulting query string.
 */
function buildSuccessUrl(snapshot: PaymentRecoverySnapshot): string {
  const url = new URL('/payment/result', window.location.origin)
  const orderId = queryString('order_id')
  const outTradeNo = queryString('out_trade_no')
  const resumeToken = queryString('resume_token')

  if (orderId || snapshot.orderId > 0)
    url.searchParams.set('order_id', orderId || String(snapshot.orderId))
  if (outTradeNo || snapshot.outTradeNo)
    url.searchParams.set('out_trade_no', outTradeNo || snapshot.outTradeNo)
  if (resumeToken || snapshot.resumeToken)
    url.searchParams.set('resume_token', resumeToken || snapshot.resumeToken)
  return url.toString()
}

/**
 * restoreAirwallexSnapshot loads a valid Airwallex recovery entry for the current route. A
 * supplied resume token is checked by the storage parser, followed by any supplied order
 * ID or trade reference. Both intent ID and client secret are required before SDK
 * initialization. Empty route identifiers impose no additional identity restriction, while
 * malformed, expired, mismatched, or other-provider snapshots return null and produce the
 * page's missing-parameters state.
 */
function restoreAirwallexSnapshot(): PaymentRecoverySnapshot | null {
  if (typeof window === 'undefined') {
    return null
  }

  const orderId = Number(queryString('order_id')) || 0
  const outTradeNo = queryString('out_trade_no')
  const resumeToken = queryString('resume_token')
  const snapshot = readPaymentRecoverySnapshot(
    window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY),
    resumeToken ? { resumeToken } : {},
  )

  if (!snapshot || snapshot.paymentType !== 'airwallex') {
    return null
  }
  if (orderId > 0 && snapshot.orderId !== orderId) {
    return null
  }
  if (outTradeNo && snapshot.outTradeNo !== outTradeNo) {
    return null
  }
  if (!snapshot.intentId || !snapshot.clientSecret) {
    return null
  }
  return snapshot
}

// Read and validate recovery before importing the SDK so an unrelated or incomplete browser
// snapshot cannot initialize an Airwallex checkout intent.
onMounted(async () => {
  const snapshot = restoreAirwallexSnapshot()
  const checkoutLocale = vendorLocale('airwallex', locale.value)

  if (!snapshot) {
    loading.value = false
    errorMessage.value = t('payment.airwallexMissingParams')
    return
  }

  try {
    const airwallex = await import('@airwallex/components-sdk')
    const result = await airwallex.init({
      env: snapshot.paymentEnv === 'prod' ? 'prod' : 'demo',
      enabledElements: ['payments'],
      locale: checkoutLocale,
    })

    loading.value = false
    const checkoutOptions = {
      intent_id: snapshot.intentId,
      client_secret: snapshot.clientSecret,
      currency: snapshot.currency || 'CNY',
      country_code: snapshot.countryCode || 'CN',
      successUrl: buildSuccessUrl(snapshot),
    }
    if (!result.payments) {
      throw new Error(t('payment.airwallexLoadFailed'))
    }
    const redirectResult = result.payments.redirectToCheckout(checkoutOptions)

    if (typeof redirectResult === 'string' && redirectResult) {
      window.location.assign(redirectResult)
    }
  } catch (err: unknown) {
    loading.value = false
    errorMessage.value =
      err instanceof Error && err.message ? err.message : t('payment.airwallexLoadFailed')
  }
})
</script>
