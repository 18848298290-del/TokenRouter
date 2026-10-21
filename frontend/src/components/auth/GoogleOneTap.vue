<template>
  <span class="hidden" aria-hidden="true"></span>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { exchangeGoogleOneTap, persistOAuthTokenContext } from '@/api/auth'
import { useAppStore, useAuthStore } from '@/stores'
import {
  cancelGoogleOneTap,
  isGoogleOneTapOriginSupported,
  showGoogleOneTap,
  type GoogleCredentialResponse,
} from '@/utils/googleIdentity'
import {
  clearAllAffiliateCodes,
  resolveAffiliateCode,
  storeOAuthAffiliateCode,
} from '@/utils/oauthAffiliate'
import { extractI18nErrorMessage } from '@/utils/apiError'

// GoogleOneTap.vue connects the Google browser prompt to the application login and
// pending-registration routes. Prompt eligibility follows enabled settings, a
// nonblank client identifier, authentication state, and origin support. The
// component watches those inputs and releases its prompt callback when disabled or
// unmounted.
//
// Credential exchange has its own busy guard because browser callbacks can
// overlap. The prompt is cancelled before exchange begins, but an already running
// exchange continues to its result. Registration-required responses save the
// provider marker for the shared callback page; authenticated responses install
// token context before clearing referral attribution and opening the sanitized
// destination.

// props supplies prompt enablement and the Google OAuth client identifier. The
// component combines them with session and origin eligibility before registering
// its SDK callback.
const props = defineProps<{
  // enabled participates in prompt eligibility together with client identity,
  // authentication state, and origin support. Changing it can cancel the owned
  // browser prompt.
  enabled: boolean // parent prompt eligibility

  // clientId identifies the configured Google OAuth client. Blank trimmed values
  // prevent prompting, while changes retrigger registration of the current
  // credential callback.
  clientId: string // Google OAuth client identifier
}>() // parent component inputs

// route and router connect callback or sign-in handlers to query values and
// navigation. Authentication stores and translators supply the session actions and
// notifications used by those handlers.
const route = useRoute() // current callback query context
const router = useRouter() // application navigation service
const { t } = useI18n() // translated authentication message resolver
const appStore = useAppStore() // public settings and toast owner
const authStore = useAuthStore() // shared authentication state owner
// processingCredential rejects overlapping browser callbacks until the active
// exchange settles. Prompt cancellation does not abort that pending exchange.
let processingCredential = false // in-flight Google credential guard

const canPrompt = computed(
  () =>
    props.enabled &&
    Boolean(props.clientId.trim()) &&
    !authStore.isAuthenticated &&
    isGoogleOneTapOriginSupported(),
) // current Google prompt eligibility

// sanitizeRedirectPath accepts only a scalar application-relative redirect for One
// Tap completion. Non-string values, protocol-relative addresses, protocol
// separators, and line breaks use /dashboard so query arrays cannot become
// navigation destinations.
function sanitizeRedirectPath(value: unknown): string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//')) {
    return '/dashboard'
  }
  // Reject embedded protocols and line breaks before returning the local
  // destination, keeping malformed callback values away from navigation.
  if (value.includes('://') || value.includes('\n') || value.includes('\r')) {
    return '/dashboard'
  }
  return value
}

// routeQueryValue trims a scalar query value or the first array element.
// Non-string values become empty, allowing optional promotion parameters to be
// omitted from One Tap exchange requests.
function routeQueryValue(value: unknown): string {
  const candidate = Array.isArray(value) ? value[0] : value
  return typeof candidate === 'string' ? candidate.trim() : ''
}

// handleCredential exchanges one nonempty Google credential while blocking
// overlapping callbacks. It cancels the prompt and saves affiliate attribution
// before the request. Registration-required responses retain the Google provider
// marker and open the completion page; authenticated responses persist token
// context before awaiting the auth store and clearing attribution. Errors show a
// toast and the busy guard resets afterward, allowing a later callback to retry
// without claiming that in-flight requests are cancelled.
async function handleCredential(response: GoogleCredentialResponse): Promise<void> {
  const credential = response.credential?.trim()
  if (!credential || processingCredential) return

  // Claim the credential callback before awaiting exchange and cancel its prompt,
  // preventing overlapping callbacks from starting another request while this one
  // runs.
  processingCredential = true
  cancelGoogleOneTap(handleCredential)
  const redirect = sanitizeRedirectPath(route.query.redirect)
  const affiliateCode = resolveAffiliateCode(route.query.aff, route.query.aff_code)
  // Save referral attribution before leaving the current sign-in entry, allowing
  // the callback to include it if the provider requires account creation.
  storeOAuthAffiliateCode(affiliateCode)

  try {
    const result = await exchangeGoogleOneTap({
      credential,
      redirect,
      aff_code: affiliateCode || undefined,
      promo_code: routeQueryValue(route.query.promo_code) || undefined,
    })
    if (result.status === 'registration_required') {
      window.sessionStorage.setItem('email_oauth_pending_provider', 'google')
      await router.push('/auth/oauth/callback')
      return
    }

    // Publish the response refresh context before token installation, so the newly
    // authenticated store can read the matching session metadata.
    persistOAuthTokenContext(result)
    await authStore.setToken(result.access_token)
    // This path has completed authentication or account binding. Clear referral
    // attribution before navigation so a later signup does not reuse the finished
    // flow context.
    clearAllAffiliateCodes()
    appStore.showSuccess(t('auth.loginSuccess'))
    await router.push(redirect)
  } catch (error: unknown) {
    appStore.showError(extractI18nErrorMessage(error, t, 'auth.errors', t('auth.loginFailed')))
  } finally {
    processingCredential = false
  }
}

// cancelPrompt releases the Google prompt owned by this component callback. The
// watcher, exposed parent action, and unmount hook reuse it without cancelling
// another component callback explicitly.
function cancelPrompt(): void {
  cancelGoogleOneTap(handleCredential)
}

// React to both eligibility and client identity changes so the SDK callback is
// registered for the current settings or released when prompting becomes
// unavailable.
watch(
  [canPrompt, () => props.clientId],
  ([enabled]) => {
    if (!enabled) {
      cancelPrompt()
      return
    }
    // When the SDK or browser rejects prompt display, leave the failure silent
    // because the page still offers its ordinary sign-in controls.
    void showGoogleOneTap(props.clientId, handleCredential).catch(() => undefined)
  },
  { immediate: true },
)

// Release this component prompt callback when it unmounts. Any credential exchange
// already started keeps its own promise lifecycle after prompt cancellation.
onBeforeUnmount(cancelPrompt)

defineExpose({ cancelPrompt })
</script>
