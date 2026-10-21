<template>
  <div class="min-h-screen bg-gray-50 px-4 py-10 dark:bg-dark-900">
    <div v-content-reveal="route.path" class="mx-auto max-w-2xl">
      <div class="card p-6">
        <h1 class="page-title">
          {{ callbackTitleText }}
        </h1>
        <p class="mt-2 text-sm text-gray-600 dark:text-gray-400">
          {{ errorMessage || callbackProcessingText }}
        </p>

        <div v-if="!errorMessage" class="mt-6 flex items-center justify-center py-10">
          <div class="spinner h-8 w-8 text-primary-500"></div>
        </div>

        <div
          v-else
          class="mt-6 rounded-surface border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-800/80"
        >
          <p class="text-sm text-gray-700 dark:text-gray-300">
            {{ errorMessage }}
          </p>
          <button class="btn btn-primary mt-4" type="button" @click="goBackToPayment">
            {{ backToPaymentText }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { vContentReveal } from '@/directives/contentReveal'

import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { useAppStore } from '@/stores'

/**
 * WechatPaymentCallbackView.vue transfers WeChat authorization return data to the purchase
 * page. Fragment values take precedence over query values, and the callback supports both
 * backend resume tokens and older OpenID-based return data.
 *
 * The return destination is normalized to an application path before navigation. A resume
 * token is forwarded on its own, while OpenID returns also carry the available purchase
 * and authorization fields. Existing destination query fields remain available and the
 * resume marker signals that checkout should consume authorization before creating an
 * order. Missing authorization identity or an upstream error stays on the callback page
 * with an error message.
 */

// Translation, route, navigation, and toast bindings read authorization return
// data and present the callback result before resuming checkout.
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()

// The callback page retains its error locally and publishes nonempty failures through the
// application toast before the user chooses another purchase attempt.
const errorMessage = ref('')

watch(errorMessage, (message) => {
  if (message) {
    appStore.showError(message)
  }
})

// Callback labels stay reactive to locale changes while the route and fragment parsing
// determine whether payment resume can continue.
const callbackProcessingText = computed(() => t('auth.wechatPayment.callbackProcessing'))
const callbackTitleText = computed(() => t('auth.wechatPayment.callbackTitle'))
const backToPaymentText = computed(() => t('auth.wechatPayment.backToPayment'))

/**
 * readQueryString reads a scalar query value or the first array value. Non-string and
 * absent values become empty strings, letting fragment lookup fall back to a consistent
 * query representation.
 */
function readQueryString(key: string): string {
  const value = route.query[key]
  if (Array.isArray(value)) {
    return typeof value[0] === 'string' ? value[0] : ''
  }
  return typeof value === 'string' ? value : ''
}

/**
 * parseFragmentParams parses the browser fragment as OAuth return parameters. It removes
 * the leading hash when present and returns an empty parameter collection when there is no
 * browser location.
 */
function parseFragmentParams(): URLSearchParams {
  const raw = typeof window !== 'undefined' ? window.location.hash : ''
  const hash = raw.startsWith('#') ? raw.slice(1) : raw
  return new URLSearchParams(hash)
}

/**
 * normalizeRedirectPath accepts an application path and maps payment aliases to purchase.
 * Empty, relative, protocol-relative, and scheme-containing destinations return the
 * purchase path, keeping callback navigation inside the application route space.
 */
function normalizeRedirectPath(path: string | null | undefined): string {
  const value = (path || '').trim()
  if (!value) return '/purchase'
  if (!value.startsWith('/')) return '/purchase'
  if (value.startsWith('//') || value.includes('://')) return '/purchase'
  if (value === '/payment') return '/purchase'
  if (value.startsWith('/payment?')) return '/purchase' + value.slice('/payment'.length)
  return value
}

/**
 * appendQueryParam copies a nonempty callback field into the destination query. Empty
 * values are omitted so optional authorization metadata does not overwrite usable
 * destination fields with empty strings.
 */
function appendQueryParam(query: Record<string, string>, key: string, value: string) {
  if (value) {
    query[key] = value
  }
}

/**
 * goBackToPayment replaces the callback route with the purchase page.
 */
function goBackToPayment() {
  void router.replace('/purchase')
}

onMounted(async () => {
  const fragment = parseFragmentParams()
  /**
   * readParam prefers a nonempty fragment value and otherwise reads the route query.
   */
  // Fragment values carry the authorization response when present; query values keep older
  // return URLs usable for the same callback route.
  const readParam = (key: string) => fragment.get(key) || readQueryString(key)

  const error = readParam('error') || readParam('err_msg') || readParam('errmsg')
  const errorDescription = readParam('error_description') || readParam('message')

  if (error) {
    errorMessage.value = errorDescription || error
    return
  }

  const resumeToken = readParam('wechat_resume_token')
  const openid = readParam('openid')
  const state = readParam('state')
  const scope = readParam('scope')
  const paymentType = readParam('payment_type')
  const amount = readParam('amount')
  const orderType = readParam('order_type')
  const planId = readParam('plan_id')
  const redirectURL = new URL(normalizeRedirectPath(readParam('redirect')), window.location.origin)

  if (!resumeToken && !openid) {
    errorMessage.value = t('auth.wechatPayment.callbackMissingResumeToken')
    return
  }

  const query: Record<string, string> = {
    ...Object.fromEntries(redirectURL.searchParams.entries()),
    wechat_resume: '1',
  }

  if (resumeToken) {
    query.wechat_resume_token = resumeToken
  } else {
    query.openid = openid
    appendQueryParam(query, 'state', state)
    appendQueryParam(query, 'scope', scope)
    appendQueryParam(query, 'payment_type', paymentType)
    appendQueryParam(query, 'amount', amount)
    appendQueryParam(query, 'order_type', orderType)
    appendQueryParam(query, 'plan_id', planId)
  }

  await router.replace({
    path: redirectURL.pathname,
    query,
  })
})
</script>
