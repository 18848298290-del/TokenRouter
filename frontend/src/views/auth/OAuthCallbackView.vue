<template>
  <div class="min-h-screen bg-gray-50 px-4 py-10 dark:bg-dark-900">
    <div v-content-reveal="route.path" class="mx-auto max-w-2xl">
      <div v-if="isProcessing" class="card p-6 text-center">
        <div class="spinner mx-auto h-8 w-8 text-primary-500"></div>
        <h1 class="mt-4 page-title">
          {{ t('auth.oauth.callbackTitle') }}
        </h1>
        <p class="mt-2 text-sm text-gray-600 dark:text-gray-400">
          {{ t('auth.oauth.callbackHint') }}
        </p>
      </div>

      <div v-else-if="needsRegistrationCompletion" class="card p-6">
        <h1 class="page-title">
          {{ t('auth.oidc.callbackTitle', { providerName }) }}
        </h1>
        <p class="mt-2 text-sm text-gray-600 dark:text-gray-400">
          {{ registrationHint }}
        </p>

        <div class="mt-6 space-y-4">
          <div>
            <label class="input-label">{{ t('auth.emailLabel') }}</label>
            <input class="input w-full" type="email" :value="registrationEmail" readonly disabled />
          </div>
          <div>
            <label class="input-label">{{ t('auth.passwordLabel') }}</label>
            <input
              v-model="password"
              type="password"
              class="input w-full"
              :placeholder="t('auth.createPasswordPlaceholder')"
              :disabled="isSubmitting"
              autocomplete="new-password"
              @keyup.enter="handleSubmitRegistration"
            />
          </div>
          <div>
            <label class="input-label">{{ t('auth.confirmPassword') }}</label>
            <input
              v-model="confirmPassword"
              type="password"
              class="input w-full"
              :placeholder="t('auth.confirmPasswordPlaceholder')"
              :disabled="isSubmitting"
              autocomplete="new-password"
              @keyup.enter="handleSubmitRegistration"
            />
          </div>
          <div v-if="invitationRequired">
            <label class="input-label">{{ t('auth.invitationCodeLabel') }}</label>
            <input
              v-model="invitationCode"
              type="text"
              class="input w-full"
              :placeholder="t('auth.invitationCodePlaceholder')"
              :disabled="isSubmitting"
              @keyup.enter="handleSubmitRegistration"
            />
          </div>
          <p v-if="registrationError" class="text-sm text-red-600 dark:text-red-400">
            {{ registrationError }}
          </p>
          <button
            class="btn btn-primary w-full"
            type="button"
            :disabled="isSubmitting || !canSubmitRegistration"
            @click="handleSubmitRegistration"
          >
            {{ isSubmitting ? t('common.processing') : t('auth.oidc.completeRegistration') }}
          </button>
        </div>
      </div>

      <div v-else-if="invalidCallback" class="card p-6 text-center">
        <h1 class="page-title">
          {{ t('auth.oauth.invalidCallbackTitle') }}
        </h1>
        <p class="mt-2 text-sm text-gray-600 dark:text-gray-400">
          {{ t('auth.oauth.invalidCallbackHint') }}
        </p>
        <button class="btn btn-primary mt-6" type="button" @click="router.replace('/login')">
          {{ t('auth.backToLogin') }}
        </button>
      </div>

      <div v-else class="card p-6">
        <h1 class="page-title">
          {{ t('auth.oauth.callbackTitle') }}
        </h1>
        <p class="mt-2 text-sm text-gray-600 dark:text-gray-400">
          {{ t('auth.oauth.callbackHint') }}
        </p>

        <div class="mt-6 space-y-4">
          <div>
            <label class="input-label">{{ t('auth.oauth.code') }}</label>
            <div class="flex gap-2">
              <input class="input flex-1 font-mono text-sm" :value="code" readonly />
              <button class="btn btn-secondary" type="button" :disabled="!code" @click="copy(code)">
                {{ t('common.copy') }}
              </button>
            </div>
          </div>

          <div>
            <label class="input-label">{{ t('auth.oauth.state') }}</label>
            <div class="flex gap-2">
              <input class="input flex-1 font-mono text-sm" :value="state" readonly />
              <button
                class="btn btn-secondary"
                type="button"
                :disabled="!state"
                @click="copy(state)"
              >
                {{ t('common.copy') }}
              </button>
            </div>
          </div>

          <div>
            <label class="input-label">{{ t('auth.oauth.fullUrl') }}</label>
            <div class="flex gap-2">
              <input class="input flex-1 font-mono text-xs" :value="fullUrl" readonly />
              <button
                class="btn btn-secondary"
                type="button"
                :disabled="!fullUrl"
                @click="copy(fullUrl)"
              >
                {{ t('common.copy') }}
              </button>
            </div>
          </div>
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
import { useClipboard } from '@/composables/useClipboard'
import { useAppStore, useAuthStore } from '@/stores'
import { apiClient, buildApiUrl } from '@/api/client'
import {
  exchangePendingOAuthCompletion,
  persistOAuthTokenContext,
  type OAuthTokenResponse,
} from '@/api/auth'
import {
  clearAllAffiliateCodes,
  loadOAuthAffiliateCode,
  oauthAffiliatePayload,
} from '@/utils/oauthAffiliate'

// OAuthCallbackView.vue handles email-provider authentication and displays generic
// callback values for manual consumers. The email-provider route can forward code
// and state back to a remembered GitHub or Google backend endpoint, resume a
// cookie-backed pending signup, or consume a token response from the fragment.
//
// The session provider marker associates a frontend callback with the provider
// that started it. Successful token completion persists refresh context and
// installs the access token before removing that marker and referral attribution.
// Invitation or registration requirements keep the page open for password entry,
// while invalid email-provider callbacks suppress the manual copy fields.

// route and router connect callback or sign-in handlers to query values and
// navigation. Authentication stores and translators supply the session actions and
// notifications used by those handlers.
const route = useRoute() // current callback query context
const router = useRouter() // application navigation service
const { t } = useI18n() // translated authentication message resolver
const { copyToClipboard } = useClipboard() // manual callback clipboard action
const appStore = useAppStore() // public settings and toast owner
const authStore = useAuthStore() // shared authentication state owner

// Email-provider state tracks exchange progress, registration requirements, and
// the password draft. pendingProvider and its session key keep the completion
// request associated with the provider that began signup.
const isProcessing = ref(false) // callback exchange progress flag
const isSubmitting = ref(false) // account-action request busy flag
const needsRegistrationCompletion = ref(false) // email-provider signup screen visibility
const invitationRequired = ref(false) // email-provider invitation requirement
const registrationEmail = ref('') // resolved email-provider account address
const password = ref('') // editable account password draft
const confirmPassword = ref('') // account password confirmation draft
const invitationCode = ref('') // entered signup invitation code
const registrationError = ref('') // email-provider signup failure message
const pendingProvider = ref<'github' | 'google'>('github') // restored authentication provider identity
const redirectTo = ref('/dashboard') // saved authentication continuation destination
const invalidCallback = ref(false) // email-provider callback rejection screen
const EMAIL_OAUTH_PENDING_PROVIDER_KEY = 'email_oauth_pending_provider' // email-provider callback session key

// EmailOAuthPendingCompletion combines optional login tokens with email-provider
// registration requirements.
type EmailOAuthPendingCompletion = Partial<OAuthTokenResponse> & {
  error?: string // server failure code

  provider?: string // authentication provider identifier

  redirect?: string // post-authentication destination path

  email?: string // account email address

  resolved_email?: string // resolved account email

  invitation_required?: boolean // signup invitation requirement
}

// The callback code is read from route query data for display or provider
// forwarding. It does not itself represent an installed application access token.
const code = computed(() => (route.query.code as string) || '') // current callback code or OTP draft
// state and error expose provider callback query values, while fullUrl supports
// manual copying. Pending-provider labels and registration readiness remain
// separate from these callback transport values.
const state = computed(() => (route.query.state as string) || '') // provider callback state parameter
const error = computed(
  () => (route.query.error as string) || (route.query.error_description as string) || '',
) // provider callback error parameter

const fullUrl = computed(() => {
  if (typeof window === 'undefined') return ''
  return window.location.href
}) // complete manual callback address
const providerName = computed(() => (pendingProvider.value === 'google' ? 'Google' : 'GitHub')) // displayed authentication provider name
const registrationHint = computed(() =>
  invitationRequired.value
    ? t('auth.oidc.invitationRequired', { providerName: providerName.value })
    : t('auth.oidc.completeRegistration'),
) // pending signup instruction message
const canSubmitRegistration = computed(() => {
  if (!registrationEmail.value.trim()) return false
  // Reject passwords below the six-character signup minimum.
  if (password.value.length < 6) return false
  if (password.value !== confirmPassword.value) return false
  if (invitationRequired.value && !invitationCode.value.trim()) return false
  return true
}) // email-provider form readiness gate

// parseFragmentParams reads callback parameters from the current browser fragment.
// It removes the leading hash marker and returns an empty parameter collection
// when no browser location is available.
function parseFragmentParams(): URLSearchParams {
  const raw = typeof window !== 'undefined' ? window.location.hash : ''
  const hash = raw.startsWith('#') ? raw.slice(1) : raw
  return new URLSearchParams(hash)
}

// readTokenResponse reconstructs a token response from legacy callback fragment
// fields. A missing access token returns null; optional refresh and token-type
// fields are trimmed, and expiry is included only when it parses as a positive
// finite integer.
function readTokenResponse(params: URLSearchParams): OAuthTokenResponse | null {
  const accessToken = params.get('access_token')?.trim() || ''
  if (!accessToken) return null

  const response: OAuthTokenResponse = { access_token: accessToken }
  const refreshToken = params.get('refresh_token')?.trim() || ''
  if (refreshToken) response.refresh_token = refreshToken
  // Legacy expiry arrives as text. Accept a finite positive integer before adding
  // it to the typed token response used for session expiry.
  const expiresIn = Number.parseInt(params.get('expires_in')?.trim() || '', 10)
  if (Number.isFinite(expiresIn) && expiresIn > 0) response.expires_in = expiresIn
  const tokenType = params.get('token_type')?.trim() || ''
  if (tokenType) response.token_type = tokenType
  return response
}

// sanitizeRedirectPath accepts an application-relative destination for callback
// navigation. Missing values, protocol-relative addresses, protocol separators,
// and line breaks select /dashboard so callers can use the result as a local
// continuation.
function sanitizeRedirectPath(path: string | null | undefined): string {
  if (!path) return '/dashboard'
  if (!path.startsWith('/')) return '/dashboard'
  if (path.startsWith('//')) return '/dashboard'
  if (path.includes('://')) return '/dashboard'
  if (path.includes('\n') || path.includes('\r')) return '/dashboard'
  return path
}

// readPendingEmailOAuthProvider restores the session marker for GitHub or Google
// login. Other stored values and environments without window return null, so
// generic callback visits cannot invent a provider.
function readPendingEmailOAuthProvider(): 'github' | 'google' | null {
  if (typeof window === 'undefined') return null
  const provider = window.sessionStorage.getItem(EMAIL_OAUTH_PENDING_PROVIDER_KEY)
  if (provider === 'github' || provider === 'google') return provider
  return null
}

// redirectProviderCallbackToBackend forwards the frontend callback query to the
// selected provider endpoint. Repeated query values are appended separately and
// null values are omitted, retaining code and state parameters across the
// redirect.
function redirectProviderCallbackToBackend(provider: 'github' | 'google'): void {
  if (typeof window === 'undefined') return
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(route.query)) {
    if (Array.isArray(value)) {
      value.forEach((item) => {
        if (item != null) params.append(key, String(item))
      })
    } else if (value != null) {
      params.set(key, String(value))
    }
  }
  const suffix = params.toString() ? `?${params.toString()}` : ''
  window.location.href = `${buildApiUrl(`/auth/oauth/${provider}/callback`)}${suffix}`
}

// finalizeTokenResponse installs the OAuth session before completing callback
// navigation. Refresh context is persisted before the auth store accepts the
// access token, and the pending provider marker is removed only after that await
// succeeds. Affiliate context is then cleared and success announced before
// replacing the route with a sanitized destination. Storage or navigation failures
// propagate to the callback caller, which owns the visible failure state.
async function finalizeTokenResponse(tokenResponse: OAuthTokenResponse, redirect: string) {
  // Publish token metadata before awaiting the auth store, keeping refresh context
  // available while the authenticated user session is initialized.
  persistOAuthTokenContext(tokenResponse)
  await authStore.setToken(tokenResponse.access_token)
  if (typeof window !== 'undefined') {
    // Token installation has completed. Clear the remembered provider before
    // leaving the callback so a later generic visit does not forward stale
    // provider identity.
    window.sessionStorage.removeItem(EMAIL_OAUTH_PENDING_PROVIDER_KEY)
  }
  // This path has completed authentication or account binding. Clear referral
  // attribution before navigation so a later signup does not reuse the finished
  // flow context.
  clearAllAffiliateCodes()
  appStore.showSuccess(t('auth.loginSuccess'))
  await router.replace(sanitizeRedirectPath(redirect))
}

// hasOAuthTokenResponse reports whether a response contains a nonblank access
// token.
function hasOAuthTokenResponse(value: Partial<OAuthTokenResponse>): value is OAuthTokenResponse {
  return typeof value.access_token === 'string' && value.access_token.trim() !== ''
}

// resumePendingEmailOAuth exchanges the cookie-backed GitHub or Google
// continuation. A token response completes login; registration requirements
// populate email and invitation controls, while errors or unknown completion
// states display the invalid-callback screen.
async function resumePendingEmailOAuth() {
  isProcessing.value = true
  try {
    const completion = (await exchangePendingOAuthCompletion()) as EmailOAuthPendingCompletion
    const completionRedirect = completion.redirect || '/dashboard'
    if (hasOAuthTokenResponse(completion)) {
      await finalizeTokenResponse(completion, completionRedirect)
      return
    }

    const provider = String(completion.provider || '').toLowerCase()
    if (provider === 'github' || provider === 'google') {
      pendingProvider.value = provider
    }
    redirectTo.value = sanitizeRedirectPath(completionRedirect)

    if (
      completion.error === 'invitation_required' ||
      completion.error === 'registration_completion_required'
    ) {
      invitationRequired.value =
        completion.error === 'invitation_required' || completion.invitation_required === true
      registrationEmail.value = String(completion.resolved_email || completion.email || '').trim()
      needsRegistrationCompletion.value = true
      isProcessing.value = false
      return
    }

    appStore.showError(completion.error || t('auth.loginFailed'))
    invalidCallback.value = true
  } catch (e: unknown) {
    const err = e as {
      message?: string // reported failure message
      response?: {
        data?: {
          message?: string // reported failure message
        } // HTTP response payload
      } // optional HTTP error response
    }
    const message = err.response?.data?.message || err.message || t('auth.loginFailed')
    appStore.showError(message)
    invalidCallback.value = true
  } finally {
    if (!needsRegistrationCompletion.value) {
      isProcessing.value = false
    }
  }
}

// handleSubmitRegistration completes email-provider signup after password and
// invitation validation. It sends the password, optional required invitation, and
// stored affiliate code to the selected provider endpoint, then finalizes the
// returned session.
async function handleSubmitRegistration() {
  registrationError.value = ''
  if (!registrationEmail.value.trim()) {
    registrationError.value = t('auth.emailRequired')
    return
  }
  // Reject passwords below the six-character signup minimum.
  if (password.value.length < 6) {
    registrationError.value = t('auth.passwordMinLength')
    return
  }
  if (password.value !== confirmPassword.value) {
    registrationError.value = t('auth.passwordsDoNotMatch')
    return
  }
  const code = invitationCode.value.trim()
  if (invitationRequired.value && !code) return

  isSubmitting.value = true
  try {
    const payload: {
      password: string // entered account password
      invitation_code?: string // signup invitation code
      aff_code?: string // referral attribution code
    } = {
      password: password.value,
      ...oauthAffiliatePayload(loadOAuthAffiliateCode()),
    }
    if (invitationRequired.value) {
      payload.invitation_code = code
    }
    const { data } = await apiClient.post<OAuthTokenResponse>(
      `/auth/oauth/${pendingProvider.value}/complete-registration`,
      payload,
    )
    await finalizeTokenResponse(data, redirectTo.value)
  } catch (e: unknown) {
    const err = e as {
      message?: string // reported failure message
      response?: {
        data?: {
          message?: string // reported failure message
        } // HTTP response payload
      } // optional HTTP error response
    }
    registrationError.value =
      err.response?.data?.message || err.message || t('auth.oidc.completeRegistrationFailed')
  } finally {
    isSubmitting.value = false
  }
}

// Resolve fragment errors and tokens before choosing backend forwarding or pending
// signup exchange, so each callback follows its corresponding credential
// representation.
onMounted(async () => {
  const params = parseFragmentParams()
  const tokenResponse = readTokenResponse(params)
  const fragmentError = params.get('error') || ''
  const fragmentErrorDescription =
    params.get('error_description') || params.get('error_message') || ''

  if (fragmentError) {
    appStore.showError(fragmentErrorDescription || fragmentError)
    if (route.path === '/auth/oauth/callback') {
      invalidCallback.value = true
    }
    return
  }
  if (!tokenResponse) {
    if (route.path === '/auth/oauth/callback') {
      const pendingEmailOAuthProvider = readPendingEmailOAuthProvider()
      if (pendingEmailOAuthProvider && code.value && state.value) {
        redirectProviderCallbackToBackend(pendingEmailOAuthProvider)
        return
      }
      await resumePendingEmailOAuth()
    }
    return
  }

  isProcessing.value = true
  try {
    await finalizeTokenResponse(tokenResponse, params.get('redirect') || '/dashboard')
  } catch (error: unknown) {
    const message =
      (
        error as {
          message?: string // reported failure message
        }
      )?.message || t('auth.loginFailed')
    appStore.showError(message)
    isProcessing.value = false
  }
})

// Show a nonempty authentication error through the shared app toast when its
// watched value changes, keeping feedback outside the form content.
watch(
  error,
  (message) => {
    if (message) {
      appStore.showError(message)
    }
  },
  { immediate: true },
)

// copy forwards a nonempty callback value to the clipboard composable. Empty
// values do nothing, so the copy controls can pass their current optional code,
// state, or URL directly.
const copy = (value: string) => {
  if (!value) return
  copyToClipboard(value)
}
</script>
