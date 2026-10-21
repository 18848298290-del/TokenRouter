<template>
  <AuthLayout>
    <div class="space-y-6">
      <div class="text-center">
        <h2 class="text-2xl font-bold text-gray-900 dark:text-white">
          {{ t('auth.oidc.callbackTitle', { providerName }) }}
        </h2>
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
          {{
            isProcessing
              ? t('auth.oidc.callbackProcessing', { providerName })
              : t('auth.oidc.callbackHint')
          }}
        </p>
      </div>

      <MotionTransition name="fade-slow">
        <div
          v-if="
            needsInvitation ||
            needsChooser ||
            needsAdoptionConfirmation ||
            needsCreateAccount ||
            needsBindLogin ||
            needsTotpChallenge
          "
          class="space-y-4"
        >
          <div
            v-if="adoptionRequired && (suggestedDisplayName || suggestedAvatarUrl)"
            class="rounded-surface border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800/60"
          >
            <div class="space-y-3">
              <div class="space-y-1">
                <p class="text-sm font-medium text-gray-900 dark:text-white">
                  {{ t('auth.oauthFlow.profileDetailsTitle', { providerName }) }}
                </p>
                <p class="text-xs text-gray-500 dark:text-dark-400">
                  {{ t('auth.oauthFlow.profileDetailsDescription', { providerName }) }}
                </p>
              </div>

              <label
                v-if="suggestedDisplayName"
                class="flex items-start gap-3 rounded-surface border border-gray-200 bg-white p-3 text-sm dark:border-dark-600 dark:bg-dark-900/50"
              >
                <input v-model="adoptDisplayName" type="checkbox" class="mt-1 h-4 w-4" />
                <span class="space-y-1">
                  <span class="block font-medium text-gray-900 dark:text-white">
                    {{ t('auth.oauthFlow.useDisplayName') }}
                  </span>
                  <span class="block text-gray-500 dark:text-dark-400">
                    {{ suggestedDisplayName }}
                  </span>
                </span>
              </label>

              <label
                v-if="suggestedAvatarUrl"
                class="flex items-start gap-3 rounded-surface border border-gray-200 bg-white p-3 text-sm dark:border-dark-600 dark:bg-dark-900/50"
              >
                <input v-model="adoptAvatar" type="checkbox" class="mt-1 h-4 w-4" />
                <img
                  :src="suggestedAvatarUrl"
                  :alt="t('auth.oauthFlow.avatarAlt', { providerName })"
                  class="h-10 w-10 rounded-full border border-gray-200 object-cover dark:border-dark-600"
                />
                <span class="space-y-1">
                  <span class="block font-medium text-gray-900 dark:text-white">
                    {{ t('auth.oauthFlow.useAvatar') }}
                  </span>
                  <span class="block break-all text-gray-500 dark:text-dark-400">
                    {{ suggestedAvatarUrl }}
                  </span>
                </span>
              </label>
            </div>
          </div>

          <template v-if="needsInvitation">
            <p class="text-sm text-gray-700 dark:text-gray-300">
              {{ t('auth.oidc.invitationRequired', { providerName }) }}
            </p>
            <div>
              <input
                v-model="invitationCode"
                type="text"
                class="input w-full"
                :placeholder="t('auth.invitationCodePlaceholder')"
                :disabled="isSubmitting"
                @keyup.enter="handleSubmitInvitation"
              />
            </div>
            <button
              class="btn btn-primary w-full"
              :disabled="isSubmitting || !invitationCode.trim()"
              @click="handleSubmitInvitation"
            >
              {{ isSubmitting ? t('auth.oidc.completing') : t('auth.oidc.completeRegistration') }}
            </button>

            <div
              class="rounded-surface border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800/60"
            >
              <div class="space-y-3">
                <div class="space-y-1">
                  <p class="text-sm font-medium text-gray-900 dark:text-white">
                    {{ t('auth.alreadyHaveAccount') }}
                  </p>
                  <p class="text-xs text-gray-500 dark:text-dark-400">
                    {{
                      hasCurrentAuthToken
                        ? t('auth.oauthFlow.bindCurrentAccountDescription', { providerName })
                        : t('auth.oauthFlow.signInThenBindDescription', { providerName })
                    }}
                  </p>
                </div>

                <input
                  v-if="!hasCurrentAuthToken"
                  v-model="existingAccountEmail"
                  data-testid="existing-account-email"
                  type="email"
                  class="input w-full"
                  :placeholder="t('auth.emailPlaceholder')"
                  :disabled="isSubmitting"
                />

                <button
                  data-testid="existing-account-submit"
                  type="button"
                  class="btn btn-secondary w-full"
                  :disabled="isSubmitting"
                  @click="handleExistingAccountBinding"
                >
                  {{
                    hasCurrentAuthToken ? t('auth.oauthFlow.bindCurrentAccount') : t('auth.signIn')
                  }}
                </button>
              </div>
            </div>
          </template>

          <template v-else-if="needsChooser">
            <div
              class="rounded-surface border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800/60"
            >
              <div class="space-y-4">
                <div class="space-y-1">
                  <p class="text-sm font-medium text-gray-900 dark:text-white">
                    {{ t('auth.oauthFlow.chooseHowToContinue') }}
                  </p>
                  <p class="text-xs text-gray-500 dark:text-dark-400">
                    {{ t('auth.oauthFlow.chooseAccountActionHint') }}
                  </p>
                </div>

                <button
                  data-testid="wechat-choice-bind-existing"
                  type="button"
                  class="btn btn-primary w-full"
                  :disabled="isSubmitting"
                  @click="switchToBindLoginMode()"
                >
                  {{ t('auth.oauthFlow.bindExistingAccount') }}
                </button>

                <button
                  data-testid="wechat-choice-create-account"
                  type="button"
                  class="btn btn-secondary w-full"
                  :disabled="isSubmitting"
                  @click="switchToCreateAccountMode()"
                >
                  {{ t('auth.oauthFlow.createNewAccount') }}
                </button>
              </div>
            </div>
          </template>

          <template v-else-if="needsAdoptionConfirmation">
            <p class="text-sm text-gray-700 dark:text-gray-300">
              {{ t('auth.oauthFlow.reviewProfileBeforeContinue', { providerName }) }}
            </p>
            <button
              class="btn btn-primary w-full"
              :disabled="isSubmitting"
              @click="handleContinueLogin"
            >
              {{ isSubmitting ? t('common.processing') : t('auth.continue') }}
            </button>
          </template>

          <template v-else-if="needsCreateAccount">
            <p class="text-sm text-gray-700 dark:text-gray-300">
              {{ t('auth.oauthFlow.createAccountHint') }}
            </p>
            <PendingOAuthCreateAccountForm
              test-id-prefix="wechat"
              :initial-email="pendingAccountEmail"
              :is-submitting="isSubmitting"
              :error-message="accountActionError"
              @submit="handleCreateAccount"
              @switch-to-bind="switchToBindLoginMode"
            />
            <button
              v-if="showBackToChooser"
              class="btn btn-secondary w-full"
              :disabled="isSubmitting"
              @click="switchToCreateAccountMode()"
            >
              {{ t('auth.oauthFlow.createNewAccount') }}
            </button>
          </template>

          <template v-else-if="needsBindLogin">
            <p class="text-sm text-gray-700 dark:text-gray-300">
              {{ t('auth.oauthFlow.bindSignInToExistingAccount', { providerName }) }}
            </p>
            <div
              v-if="hasCurrentAuthToken"
              class="rounded-surface border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800/60"
            >
              <div class="space-y-3">
                <div class="space-y-1">
                  <p class="text-sm font-medium text-gray-900 dark:text-white">
                    {{ t('auth.oauthFlow.bindCurrentAccountTitle') }}
                  </p>
                  <p class="text-xs text-gray-500 dark:text-dark-400">
                    {{ t('auth.oauthFlow.bindCurrentAccountDescription', { providerName }) }}
                  </p>
                </div>

                <button
                  data-testid="existing-account-submit"
                  type="button"
                  class="btn btn-primary w-full"
                  :disabled="isSubmitting"
                  @click="handleBindCurrentAccount"
                >
                  {{
                    isSubmitting ? t('common.processing') : t('auth.oauthFlow.bindCurrentAccount')
                  }}
                </button>
              </div>
            </div>
            <div v-else class="space-y-3">
              <input
                v-model="bindLoginEmail"
                data-testid="wechat-bind-login-email"
                type="email"
                class="input w-full"
                :placeholder="t('auth.emailPlaceholder')"
                :disabled="isSubmitting"
                @keyup.enter="handleBindLogin"
              />
              <input
                v-model="bindLoginPassword"
                data-testid="wechat-bind-login-password"
                type="password"
                class="input w-full"
                :placeholder="t('auth.passwordPlaceholder')"
                :disabled="isSubmitting"
                @keyup.enter="handleBindLogin"
              />
              <button
                data-testid="wechat-bind-login-submit"
                class="btn btn-primary w-full"
                :disabled="isSubmitting || !bindLoginEmail.trim() || !bindLoginPassword"
                @click="handleBindLogin"
              >
                {{ isSubmitting ? t('common.processing') : t('auth.oauthFlow.logInAndBind') }}
              </button>
            </div>
            <button
              v-if="showBackToChooser"
              class="btn btn-secondary w-full"
              :disabled="isSubmitting"
              @click="switchToCreateAccountMode()"
            >
              {{ t('auth.oauthFlow.createNewAccount') }}
            </button>
          </template>

          <template v-else-if="needsTotpChallenge">
            <p class="text-sm text-gray-700 dark:text-gray-300">
              {{
                t('auth.oauthFlow.totpHint', {
                  providerName,
                  account: totpUserEmailMasked || t('auth.oauthFlow.yourAccount'),
                })
              }}
            </p>
            <div class="space-y-3">
              <input
                v-model="totpCode"
                data-testid="wechat-bind-login-totp"
                type="text"
                inputmode="numeric"
                maxlength="6"
                autocomplete="one-time-code"
                class="input w-full"
                placeholder="123456"
                :disabled="isSubmitting"
                @keyup.enter="handleSubmitTotpChallenge"
              />
              <button
                data-testid="wechat-bind-login-totp-submit"
                class="btn btn-primary w-full"
                :disabled="isSubmitting || totpCode.trim().length !== 6"
                @click="handleSubmitTotpChallenge"
              >
                {{ isSubmitting ? t('common.processing') : t('auth.oauthFlow.verifyAndContinue') }}
              </button>
            </div>
          </template>
        </div>
      </MotionTransition>
    </div>
  </AuthLayout>
</template>

<script setup lang="ts">
import MotionTransition from '@/components/common/MotionTransition.vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { AuthLayout } from '@/components/layout'
import PendingOAuthCreateAccountForm, {
  type PendingOAuthCreateAccountPayload,
} from '@/components/auth/PendingOAuthCreateAccountForm.vue'
import { apiClient, buildApiUrl } from '@/api/client'
import { useAuthStore, useAppStore } from '@/stores'
import {
  completeWeChatOAuthRegistration,
  exchangePendingOAuthCompletion,
  getAuthToken,
  hasExplicitWeChatOAuthCapabilities,
  getOAuthCompletionKind,
  isOAuthLoginCompletion,
  login2FA,
  prepareOAuthBindAccessTokenCookie,
  persistOAuthTokenContext,
  resolveWeChatOAuthStartStrict,
  type OAuthAdoptionDecision,
  type OAuthTokenResponse,
  type PendingOAuthExchangeResponse,
} from '@/api/auth'
import {
  clearAllAffiliateCodes,
  loadOAuthAffiliateCode,
  oauthAffiliatePayload,
} from '@/utils/oauthAffiliate'

// WechatCallbackView.vue completes WeChat authentication and keeps unfinished
// account actions on their corresponding forms. Legacy fragment tokens and
// cookie-backed pending exchanges enter the page through separate paths.
// Invitation, account choice, profile adoption, and TOTP results remain pending
// until the server returns a completed login or binding.
//
// Binding success clears continuation metadata without replacing the current
// account token. Login success installs token context before clearing referral
// attribution and navigating, while creation errors that identify an existing
// email reopen password binding. Existing-account binding can leave for login and
// resume with mode, email, and destination query data. Confirmed per-mode settings
// take precedence, followed by query and browser-derived mode recovery.

// route and router connect callback or sign-in handlers to query values and
// navigation. Authentication stores and translators supply the session actions and
// notifications used by those handlers.
const route = useRoute() // current callback query context
const router = useRouter() // application navigation service
const { t } = useI18n() // translated authentication message resolver

const authStore = useAuthStore() // shared authentication state owner
const appStore = useAppStore() // public settings and toast owner

// Callback state separates exchange progress from invitation, account choice,
// profile adoption, and binding drafts. Recognized continuation modes select
// their controls; a bare pending session can retain metadata with no form active.
const isProcessing = ref(true) // callback exchange progress flag
const errorMessage = ref('') // current action failure message
const needsInvitation = ref(false) // pending invitation screen flag
const needsChooser = ref(false) // account choice screen visibility
const invitationCode = ref('') // entered signup invitation code
const isSubmitting = ref(false) // account-action request busy flag
const invitationError = ref('') // invitation completion failure message
const redirectTo = ref('/dashboard') // saved authentication continuation destination
const adoptionRequired = ref(false) // server profile-consent requirement
const suggestedDisplayName = ref('') // server suggested account name
const suggestedAvatarUrl = ref('') // server suggested avatar URL
const existingAccountEmail = ref('') // WeChat binding recovery email
const adoptDisplayName = ref(true) // selected name adoption choice
const adoptAvatar = ref(true) // selected avatar adoption choice
const needsAdoptionConfirmation = ref(false) // profile-consent screen visibility
const pendingAccountAction = ref<'none' | 'choice' | 'create_account' | 'bind_login'>('none') // active account continuation mode
const pendingAccountEmail = ref('') // pending creation email draft
const bindLoginEmail = ref('') // existing-account email draft
// bindLoginPassword belongs to the existing-account form. Entering binding mode
// clears the previous password while retaining the selected email for the next
// authentication request.
const bindLoginPassword = ref('') // existing-account password draft
// legacyPendingOAuthToken retains the explicit invitation token read from a legacy
// fragment. Cookie-backed pending flows leave it empty and use their own
// completion path.
const legacyPendingOAuthToken = ref('') // legacy fragment invitation token
const accountActionError = ref('') // account-action failure toast source
// needsTotpChallenge selects the second-factor form after password binding.
// totpTempToken, totpCode, totpError, and totpUserEmailMasked keep the challenge
// credential, entered response, feedback, and account hint together.
const needsTotpChallenge = ref(false) // pending second-factor screen visibility
const totpTempToken = ref('') // pending second-factor challenge token
const totpCode = ref('') // entered second-factor challenge code
const totpError = ref('') // second-factor failure toast source
const totpUserEmailMasked = ref('') // masked second-factor account address
// The binding message and provider name describe WeChat completion separately from
// account-form modes. Current-token availability selects direct binding or the
// login-and-resume recovery route.
const bindSuccessMessage = computed(() => t('profile.authBindings.bindSuccess')) // completed binding notification text

const providerName = computed(() => t('auth.wechatProviderName')) // displayed authentication provider name
const showBackToChooser = computed(
  () =>
    pendingAccountAction.value === 'create_account' || pendingAccountAction.value === 'bind_login',
) // WeChat account chooser return flag
const needsCreateAccount = computed(() => pendingAccountAction.value === 'create_account') // account creation screen visibility
const needsBindLogin = computed(() => pendingAccountAction.value === 'bind_login') // existing-account screen visibility
// hasCurrentAuthToken reads token availability through getAuthToken inside a
// computed getter. The storage read itself does not provide a reactive dependency
// for later storage changes.
const hasCurrentAuthToken = computed(() => Boolean(getAuthToken())) // current WeChat binding token availability

// Show a nonempty authentication error through the shared app toast when its
// watched value changes, keeping feedback outside the form content.
watch(invitationError, (value) => {
  if (value) {
    appStore.showError(value)
  }
})

// Show a nonempty authentication error through the shared app toast when its
// watched value changes, keeping feedback outside the form content.
watch(accountActionError, (value) => {
  if (value) {
    appStore.showError(value)
  }
})

// Show a nonempty authentication error through the shared app toast when its
// watched value changes, keeping feedback outside the form content.
watch(totpError, (value) => {
  if (value) {
    appStore.showError(value)
  }
})

// Show a nonempty authentication error through the shared app toast when its
// watched value changes, keeping feedback outside the form content.
watch(errorMessage, (value) => {
  if (value) {
    appStore.showError(value)
  }
})

// PendingWeChatCompletion describes provider continuation fields accepted
// alongside the shared OAuth response.
type PendingWeChatCompletion = PendingOAuthExchangeResponse & {
  step?: string // server continuation step

  status?: string // compatibility continuation status

  state?: string // compatibility continuation state

  pending_email?: string // pending creation email

  resolved_email?: string // resolved account email

  existing_account_email?: string // existing account email

  email?: string // account email address

  intent?: string // server authentication intent

  requires_2fa?: boolean // second-factor requirement flag

  temp_token?: string // temporary second-factor token

  user_email_masked?: string // masked challenge account email
}

// persistPendingAuthSession records the provider and sanitized continuation route
// in the auth store. The empty token denotes the cookie-backed pending flow, so
// callers retain metadata without copying a server session credential into this
// record.
function persistPendingAuthSession(redirect?: string) {
  authStore.setPendingAuthSession({
    token: '',
    token_field: 'pending_oauth_token',
    provider: 'wechat',
    redirect: sanitizeRedirectPath(redirect || redirectTo.value),
  })
}

// clearPendingAuthSession delegates removal of the saved pending authentication
// metadata. Call it when the callback finishes binding or fails its initial
// exchange so later registration cannot reuse this continuation.
function clearPendingAuthSession() {
  authStore.clearPendingAuthSession()
}

// parseFragmentParams reads callback parameters from the current browser fragment.
// It removes the leading hash marker and returns an empty parameter collection
// when no browser location is available.
function parseFragmentParams(): URLSearchParams {
  const raw = typeof window !== 'undefined' ? window.location.hash : ''
  const hash = raw.startsWith('#') ? raw.slice(1) : raw
  return new URLSearchParams(hash)
}

// readLegacyFragmentLogin reconstructs a token response from legacy callback
// fragment fields. A missing access token returns null; optional refresh and
// token-type fields are trimmed, and expiry is included only when it parses as a
// positive finite integer.
function readLegacyFragmentLogin(params: URLSearchParams): OAuthTokenResponse | null {
  const accessToken = params.get('access_token')?.trim() || ''
  if (!accessToken) {
    return null
  }

  const completion: OAuthTokenResponse = {
    access_token: accessToken,
  }
  const refreshToken = params.get('refresh_token')?.trim() || ''
  if (refreshToken) {
    completion.refresh_token = refreshToken
  }
  // Legacy expiry arrives as text. Accept a finite positive integer before adding
  // it to the typed token response used for session expiry.
  const expiresIn = Number.parseInt(params.get('expires_in')?.trim() || '', 10)
  if (Number.isFinite(expiresIn) && expiresIn > 0) {
    completion.expires_in = expiresIn
  }
  const tokenType = params.get('token_type')?.trim() || ''
  if (tokenType) {
    completion.token_type = tokenType
  }
  return completion
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

// ensurePublicSettingsLoaded fetches settings only when explicit WeChat
// capabilities and a completed settings load are both absent. Callers handle
// rejected fetches and decide whether query or runtime mode recovery remains
// usable.
async function ensurePublicSettingsLoaded(): Promise<void> {
  if (hasExplicitWeChatOAuthCapabilities(appStore.cachedPublicSettings)) {
    return
  }

  if (appStore.publicSettingsLoaded) {
    return
  }

  await appStore.fetchPublicSettings()
}

// resolveConfiguredWeChatOAuthMode returns the strict browser-compatible mode from
// explicit capabilities. Missing capability fields return null, keeping an old
// aggregate enabled flag distinct from confirmed per-mode configuration.
function resolveConfiguredWeChatOAuthMode(): 'open' | 'mp' | null {
  if (!hasExplicitWeChatOAuthCapabilities(appStore.cachedPublicSettings)) {
    return null
  }

  return resolveWeChatOAuthStartStrict(appStore.cachedPublicSettings).mode
}

// resolveWeChatOAuthUnavailableMessage explains the strict capability resolver
// rejection to the user. Known browser and configuration reasons select their
// specific messages; unknown reasons use the general login failure text.
function resolveWeChatOAuthUnavailableMessage(): string {
  const resolved = resolveWeChatOAuthStartStrict(appStore.cachedPublicSettings)

  switch (resolved.unavailableReason) {
    case 'capability_unknown':
      return t('auth.oauthFlow.wechatAvailabilityUnknown')
    case 'external_browser_required':
      return t('auth.oauthFlow.wechatSystemBrowserOnly')
    case 'wechat_browser_required':
      return t('auth.oauthFlow.wechatBrowserOnly')
    case 'native_app_required':
      return 'This WeChat sign-in flow is only available from the native mobile app.'
    case 'not_configured':
      return t('auth.oauthFlow.wechatNotConfigured')
    default:
      return t('auth.loginFailed')
  }
}

// resolveRuntimeWeChatOAuthMode chooses mp inside the WeChat browser and open
// otherwise. Environments without navigator use open, providing a runtime fallback
// when capability and query mode selection cannot choose one.
function resolveRuntimeWeChatOAuthMode(): 'open' | 'mp' {
  if (typeof navigator === 'undefined') {
    return 'open'
  }
  return /MicroMessenger/i.test(navigator.userAgent) ? 'mp' : 'open'
}

// normalizeWeChatOAuthMode accepts exactly the open and mp mode strings.
function normalizeWeChatOAuthMode(value: unknown): 'open' | 'mp' | null {
  return value === 'open' || value === 'mp' ? value : null
}

// resolveRequestedWeChatOAuthMode prioritizes a confirmed configured mode over
// query and browser-derived choices. When strict configuration returns no mode, a
// valid query mode still wins over the runtime fallback, including unavailable or
// unknown capability results.
function resolveRequestedWeChatOAuthMode(): 'open' | 'mp' | null {
  // Confirmed compatible capabilities win over query recovery. If they cannot
  // choose a mode, the query and runtime fallback remain available on this path.
  const configuredMode = resolveConfiguredWeChatOAuthMode()
  if (configuredMode) {
    return configuredMode
  }

  const queryMode = normalizeWeChatOAuthMode(route.query.mode)
  if (queryMode) {
    return queryMode
  }

  return resolveRuntimeWeChatOAuthMode()
}

// resolveRedirectTarget sanitizes the query destination or stored continuation
// route. The query value takes precedence when truthy, and invalid destinations
// fall back to /dashboard through the callback redirect validator.
function resolveRedirectTarget(): string {
  return sanitizeRedirectPath(
    (route.query.redirect as string | undefined) || redirectTo.value || '/dashboard',
  )
}

// resolveWeChatStartURL builds the bind or adoption authorization endpoint for the
// requested intent. It includes the selected mode and sanitized redirect,
// returning null if mode resolution provides no usable value.
function resolveWeChatStartURL(
  intent: 'bind_current_user' | 'adopt_existing_user_by_email',
): string | null {
  const mode = resolveRequestedWeChatOAuthMode()
  if (!mode) {
    return null
  }
  const params = new URLSearchParams({
    mode,
    redirect: resolveRedirectTarget(),
    intent,
  })

  const startPath =
    intent === 'bind_current_user' ? '/auth/oauth/wechat/bind/start' : '/auth/oauth/wechat/start'
  return `${buildApiUrl(startPath)}?${params.toString()}`
}

// buildExistingAccountResumePath encodes the callback needed after
// existing-account login. The resume marker, selected mode, and sanitized
// destination travel together through the login redirect parameter. A nonempty
// trimmed email is added as a convenience for the resumed form, while the current
// account token is obtained separately after login. Keeping these values inside
// URLSearchParams prevents nested redirect and email characters from becoming
// unrelated outer query fields.
function buildExistingAccountResumePath(): string | null {
  const mode = resolveRequestedWeChatOAuthMode()
  if (!mode) {
    return null
  }

  const params = new URLSearchParams({
    wechat_bind_existing: '1',
    redirect: resolveRedirectTarget(),
    mode,
  })

  const email = existingAccountEmail.value.trim()
  if (email) {
    params.set('email', email)
  }

  return `/auth/wechat/callback?${params.toString()}`
}

// currentAdoptionDecision captures the current name and avatar consent selections.
function currentAdoptionDecision(): OAuthAdoptionDecision {
  return {
    adoptDisplayName: adoptDisplayName.value,
    adoptAvatar: adoptAvatar.value,
  }
}

// resolveResumeEmail returns the trimmed scalar email from callback query data.
function resolveResumeEmail(): string {
  return typeof route.query.email === 'string' ? route.query.email.trim() : ''
}

// serializeAdoptionDecision maps explicitly supplied profile choices to backend
// field names. Boolean false remains an explicit refusal, while absent choices are
// omitted so an initial exchange need not make a decision.
function serializeAdoptionDecision(decision: OAuthAdoptionDecision): Record<string, boolean> {
  const payload: Record<string, boolean> = {}
  // Only explicit boolean choices cross the request boundary. Omitting unknown
  // choices keeps the initial exchange distinct from an explicit refusal to adopt
  // profile data.
  if (typeof decision.adoptDisplayName === 'boolean') {
    payload.adopt_display_name = decision.adoptDisplayName
  }
  if (typeof decision.adoptAvatar === 'boolean') {
    payload.adopt_avatar = decision.adoptAvatar
  }
  return payload
}

// handleBindCurrentAccount restarts WeChat authorization for the currently
// authenticated account. It builds the start URL, prepares the backend binding
// cookie before leaving the page, and reports failures without navigating.
async function handleBindCurrentAccount() {
  const unavailableMessage =
    resolveConfiguredWeChatOAuthMode() === null ? resolveWeChatOAuthUnavailableMessage() : ''

  const startURL = resolveWeChatStartURL('bind_current_user')
  if (!startURL) {
    errorMessage.value = unavailableMessage || resolveWeChatOAuthUnavailableMessage()
    return
  }

  try {
    // Prepare the authenticated binding cookie before navigating to the provider
    // start endpoint, which needs that browser context to identify the current
    // account.
    await prepareOAuthBindAccessTokenCookie()
    window.location.href = startURL
  } catch (e: unknown) {
    errorMessage.value = getRequestErrorMessage(e, t('auth.loginFailed'))
  }
}

// handleExistingAccountBinding sends an authenticated user directly to
// current-account binding. Otherwise it builds a nested callback destination and
// opens login with the optional email, preserving the intended WeChat
// continuation.
async function handleExistingAccountBinding() {
  if (getAuthToken()) {
    await handleBindCurrentAccount()
    return
  }

  const resumePath = buildExistingAccountResumePath()
  if (!resumePath) {
    errorMessage.value = resolveWeChatOAuthUnavailableMessage()
    return
  }

  const params = new URLSearchParams({
    redirect: resumePath,
  })
  const email = existingAccountEmail.value.trim()
  if (email) {
    params.set('email', email)
  }
  await router.replace(`/login?${params.toString()}`)
}

// applyAdoptionSuggestionState stores the server profile suggestions and adoption
// requirement. A missing name or avatar disables its corresponding adoption
// choice; a later nonempty suggestion does not automatically restore that choice.
function applyAdoptionSuggestionState(completion: PendingOAuthExchangeResponse) {
  adoptionRequired.value = completion.adoption_required === true
  suggestedDisplayName.value = completion.suggested_display_name || ''
  suggestedAvatarUrl.value = completion.suggested_avatar_url || ''

  if (!suggestedDisplayName.value) {
    adoptDisplayName.value = false
  }
  if (!suggestedAvatarUrl.value) {
    adoptAvatar.value = false
  }
}

// hasSuggestedProfile reports whether either server profile suggestion is present.
function hasSuggestedProfile(completion: PendingOAuthExchangeResponse): boolean {
  return Boolean(completion.suggested_display_name || completion.suggested_avatar_url)
}

// normalizedPendingState trims and lowercases a server continuation state value.
// Missing values become the empty string, allowing the action resolver to handle
// absent fields through its ordinary unknown-state result.
function normalizedPendingState(value: string | null | undefined): string {
  return value?.trim().toLowerCase() || ''
}

// extractPendingAccountEmail selects the first nonempty email in this provider
// response precedence. Trimming happens after selection, so a whitespace-only
// higher-priority field produces an empty result rather than trying later fields.
function extractPendingAccountEmail(completion: PendingWeChatCompletion): string {
  return (
    completion.pending_email ||
    completion.existing_account_email ||
    completion.resolved_email ||
    completion.email ||
    resolveResumeEmail() ||
    ''
  ).trim()
}

// resolvePendingAccountAction translates provider response states into the page
// account-action modes. It checks the first truthy state field, recognizes the
// listed compatibility spellings, and returns none for other values.
function resolvePendingAccountAction(
  completion: PendingWeChatCompletion,
): 'none' | 'choice' | 'create_account' | 'bind_login' {
  // Interpret the selected compatibility state once before applying its aliases,
  // keeping unknown provider response values out of the editable account modes.
  const raw = normalizedPendingState(
    completion.step ||
      completion.status ||
      completion.state ||
      completion.error ||
      completion.intent,
  )
  if (
    raw === 'choice' ||
    raw === 'choose_account_action_required' ||
    raw === 'choose_account_action' ||
    raw === 'choose_account' ||
    raw === 'choose'
  ) {
    return 'choice'
  }
  if (raw === 'email_required' || raw === 'create_account_required' || raw === 'create_account') {
    return 'create_account'
  }
  if (
    raw === 'existing_account' ||
    raw === 'existing_account_required' ||
    raw === 'existing_account_binding_required' ||
    raw === 'adopt_existing_user_by_email' ||
    raw === 'bind_login_required' ||
    raw === 'bind_login'
  ) {
    return 'bind_login'
  }
  return 'none'
}

// applyPendingAccountAction updates the account form selected by the server
// response. It clears the previous action error and TOTP challenge before applying
// the selected email; binding and chooser transitions discard the entered binding
// password.
function applyPendingAccountAction(completion: PendingWeChatCompletion) {
  const action = resolvePendingAccountAction(completion)
  pendingAccountAction.value = action
  accountActionError.value = ''
  needsChooser.value = false
  needsTotpChallenge.value = false
  totpTempToken.value = ''
  totpCode.value = ''
  totpError.value = ''
  totpUserEmailMasked.value = ''

  const email = extractPendingAccountEmail(completion)
  pendingAccountEmail.value = email
  if (action === 'create_account') {
    return
  }

  if (action === 'bind_login') {
    bindLoginEmail.value = email
    bindLoginPassword.value = ''
    return
  }

  if (action === 'choice') {
    needsChooser.value = true
    bindLoginPassword.value = ''
    return
  }
}

// applyTotpChallenge activates the second-factor form only when both the required
// flag and temporary token are present. It clears competing continuation screens
// and old challenge input, returning whether it consumed the response.
function applyTotpChallenge(completion: PendingWeChatCompletion): boolean {
  if (completion.requires_2fa !== true || !completion.temp_token) {
    return false
  }

  pendingAccountAction.value = 'none'
  needsChooser.value = false
  needsInvitation.value = false
  needsAdoptionConfirmation.value = false
  needsTotpChallenge.value = true
  totpTempToken.value = completion.temp_token
  totpCode.value = ''
  totpError.value = ''
  totpUserEmailMasked.value = completion.user_email_masked || ''
  isProcessing.value = false
  return true
}

// switchToBindLoginMode opens existing-account authentication while keeping the
// best available email. It prefers the current binding address, then the supplied
// address, then the creation draft, and clears the password and action error.
function switchToBindLoginMode(nextEmail?: string) {
  pendingAccountAction.value = 'bind_login'
  needsChooser.value = false
  bindLoginEmail.value =
    bindLoginEmail.value.trim() || nextEmail?.trim() || pendingAccountEmail.value.trim()
  bindLoginPassword.value = ''
  accountActionError.value = ''
}

// switchToCreateAccountMode reopens account creation with the current email draft.
// It uses the binding email only when the creation email is empty and clears the
// previous account-action error.
function switchToCreateAccountMode() {
  pendingAccountAction.value = 'create_account'
  needsChooser.value = false
  pendingAccountEmail.value = pendingAccountEmail.value.trim() || bindLoginEmail.value.trim()
  accountActionError.value = ''
}

// getRequestErrorMessage selects server detail, server message, or the error
// message in that order. It returns the caller fallback when those fields are
// empty, keeping endpoint-specific text available to toast callers.
function getRequestErrorMessage(error: unknown, fallback: string): string {
  const err = error as {
    message?: string // reported failure message

    response?: {
      data?: {
        detail?: string // server detailed failure message
        message?: string // reported failure message
      } // HTTP response payload
    } // optional HTTP error response
  }
  return err.response?.data?.detail || err.response?.data?.message || err.message || fallback
}

// isCreateAccountRecoveryError recognizes errors that require authenticating an
// existing account. It normalizes all supported server reason fields
// independently, so a matching compatibility field can request recovery even when
// another field differs.
function isCreateAccountRecoveryError(error: unknown): boolean {
  const data = (
    error as {
      response?: {
        data?: {
          reason?: string // server failure reason

          error?: string // server failure code

          code?: string // server failure code

          step?: string // server continuation step

          intent?: string // server authentication intent
        } // HTTP response payload
      } // optional HTTP error response
    }
  ).response?.data
  const states = [data?.reason, data?.error, data?.code, data?.step, data?.intent]
    .map((value) => value?.trim().toLowerCase())
    .filter((value): value is string => Boolean(value))

  return (
    states.includes('email_exists') ||
    states.includes('bind_login_required') ||
    states.includes('bind_login') ||
    states.includes('adopt_existing_user_by_email') ||
    states.includes('existing_account_required') ||
    states.includes('existing_account_binding_required')
  )
}

// finalizeCompletion finishes a resolved binding or authenticated login and
// navigates afterward. Binding clears the pending session and affiliate context
// without replacing the current access token. Login requires a nonempty token
// response, persists its refresh metadata, and waits for auth-store token
// installation before clearing attribution and announcing success. A pending
// response belongs to the continuation dispatcher and cannot be treated as a
// successful login here.
async function finalizeCompletion(completion: PendingOAuthExchangeResponse, redirect: string) {
  if (getOAuthCompletionKind(completion) === 'bind') {
    const bindRedirect = sanitizeRedirectPath(completion.redirect || '/profile')
    // Clear the saved continuation at this terminal or failed-initialization path
    // so a later account form does not resume the discarded pending session.
    clearPendingAuthSession()
    // This path has completed authentication or account binding. Clear referral
    // attribution before navigation so a later signup does not reuse the finished
    // flow context.
    clearAllAffiliateCodes()
    appStore.showSuccess(bindSuccessMessage.value)
    await router.replace(bindRedirect)
    return
  }

  if (!isOAuthLoginCompletion(completion)) {
    throw new Error(t('auth.oidc.callbackMissingToken'))
  }

  // Persist refresh metadata before awaiting token installation, so the auth store
  // can initialize the authenticated session using this response context.
  persistOAuthTokenContext(completion)
  await authStore.setToken(completion.access_token)
  // This path has completed authentication or account binding. Clear referral
  // attribution before navigation so a later signup does not reuse the finished
  // flow context.
  clearAllAffiliateCodes()
  appStore.showSuccess(t('auth.loginSuccess'))
  await router.replace(redirect)
}

// finalizePendingAccountResponse dispatches account responses by continuation
// state. Invitation requirements take priority over TOTP and recognized account
// modes; those branches persist continuation metadata and select their controls.
// A bare pending_session also persists metadata, but clears the TOTP, invitation,
// and adoption flags and leaves the account mode as none, so it selects no
// editable continuation. Responses outside those pending branches reach
// finalizeCompletion, which checks for binding or login completion.
async function finalizePendingAccountResponse(completion: PendingWeChatCompletion) {
  applyAdoptionSuggestionState(completion)
  const redirect = sanitizeRedirectPath(completion.redirect || redirectTo.value)

  if (completion.error === 'invitation_required') {
    pendingAccountAction.value = 'none'
    needsInvitation.value = true
    needsAdoptionConfirmation.value = false
    isProcessing.value = false
    persistPendingAuthSession(redirect)
    return
  }

  if (applyTotpChallenge(completion)) {
    persistPendingAuthSession(redirect)
    return
  }

  applyPendingAccountAction(completion)
  if (pendingAccountAction.value !== 'none') {
    needsInvitation.value = false
    needsAdoptionConfirmation.value = false
    isProcessing.value = false
    persistPendingAuthSession(redirect)
    return
  }

  if (completion.auth_result === 'pending_session') {
    needsInvitation.value = false
    needsAdoptionConfirmation.value = false
    isProcessing.value = false
    persistPendingAuthSession(redirect)
    return
  }

  await finalizeCompletion(completion, redirect)
}

// handleSubmitInvitation completes this provider registration with a nonempty
// invitation code. It includes the current adoption choices and stored affiliate
// attribution, then routes any remaining server step through the pending-response
// dispatcher.
async function handleSubmitInvitation() {
  invitationError.value = ''
  if (!invitationCode.value.trim()) return

  isSubmitting.value = true
  try {
    const affCode = loadOAuthAffiliateCode()
    const completion: PendingWeChatCompletion = legacyPendingOAuthToken.value
      ? (
          await apiClient.post<PendingWeChatCompletion>(
            '/auth/oauth/wechat/complete-registration',
            {
              pending_oauth_token: legacyPendingOAuthToken.value,
              invitation_code: invitationCode.value.trim(),
              ...oauthAffiliatePayload(affCode),
              ...serializeAdoptionDecision(currentAdoptionDecision()),
            },
          )
        ).data
      : await completeWeChatOAuthRegistration(
          invitationCode.value.trim(),
          currentAdoptionDecision(),
          affCode || undefined,
        )
    await finalizePendingAccountResponse(completion)
  } catch (e: unknown) {
    const err = e as {
      message?: string // reported failure message
      response?: {
        data?: {
          message?: string // reported failure message
        } // HTTP response payload
      } // optional HTTP error response
    }
    invitationError.value =
      err.response?.data?.message || err.message || t('auth.oidc.completeRegistrationFailed')
  } finally {
    isSubmitting.value = false
  }
}

// handleContinueLogin exchanges the pending session with the current explicit
// adoption choices. It delegates subsequent states to the continuation dispatcher
// and hides adoption confirmation when the exchange fails.
async function handleContinueLogin() {
  isSubmitting.value = true
  try {
    const completion = (await exchangePendingOAuthCompletion(
      currentAdoptionDecision(),
    )) as PendingWeChatCompletion
    await finalizePendingAccountResponse(completion)
  } catch (e: unknown) {
    errorMessage.value = getRequestErrorMessage(e, t('auth.loginFailed'))
    needsAdoptionConfirmation.value = false
  } finally {
    isSubmitting.value = false
  }
}

// handleCreateAccount submits the pending OAuth creation form and its optional
// verification proof. Existing-account recovery errors reopen binding with the
// submitted email; other errors remain available to the page toast path, and
// submission state clears afterward.
async function handleCreateAccount(payload: PendingOAuthCreateAccountPayload) {
  accountActionError.value = ''
  if (!payload.email || !payload.password) return

  isSubmitting.value = true
  try {
    const { data } = await apiClient.post<PendingWeChatCompletion>(
      '/auth/oauth/pending/create-account',
      {
        email: payload.email,
        password: payload.password,
        verify_code: payload.verifyCode || undefined,
        ...(payload.turnstileToken ? { turnstile_token: payload.turnstileToken } : {}),
        ...(payload.tencentCaptchaTicket
          ? {
              tencent_captcha_ticket: payload.tencentCaptchaTicket,
              tencent_captcha_randstr: payload.tencentCaptchaRandstr,
            }
          : {}),
        invitation_code: payload.invitationCode || undefined,
        ...oauthAffiliatePayload(loadOAuthAffiliateCode()),
        ...serializeAdoptionDecision(currentAdoptionDecision()),
      },
    )
    await finalizePendingAccountResponse(data)
  } catch (e: unknown) {
    // The creation response identifies an existing account. Reuse the submitted
    // email in password binding instead of presenting account creation as a
    // generic failure.
    if (isCreateAccountRecoveryError(e)) {
      switchToBindLoginMode(payload.email.trim())
      return
    }
    accountActionError.value = getRequestErrorMessage(e, t('auth.loginFailed'))
  } finally {
    isSubmitting.value = false
  }
}

// handleBindLogin authenticates the existing account with a trimmed email and
// unchanged password. It sends the selected adoption choices and delegates the
// response so TOTP or another pending step can remain active.
async function handleBindLogin() {
  accountActionError.value = ''
  const email = bindLoginEmail.value.trim()
  const password = bindLoginPassword.value
  if (!email || !password) return

  isSubmitting.value = true
  try {
    const { data } = await apiClient.post<PendingWeChatCompletion>(
      '/auth/oauth/pending/bind-login',
      {
        email,
        password,
        ...serializeAdoptionDecision(currentAdoptionDecision()),
      },
    )
    await finalizePendingAccountResponse(data)
  } catch (e: unknown) {
    accountActionError.value = getRequestErrorMessage(e, t('auth.loginFailed'))
  } finally {
    isSubmitting.value = false
  }
}

// handleSubmitTotpChallenge submits a six-character code with the pending
// temporary token. It installs the returned access token before clearing affiliate
// attribution and navigating, while failures keep the challenge available for
// retry.
async function handleSubmitTotpChallenge() {
  totpError.value = ''
  const code = totpCode.value.trim()
  if (!totpTempToken.value || code.length !== 6) return

  isSubmitting.value = true
  try {
    const completion = await login2FA({
      temp_token: totpTempToken.value,
      totp_code: code,
    })
    // Persist refresh metadata before awaiting token installation, so the auth
    // store can initialize the authenticated session using this response context.
    persistOAuthTokenContext(completion)
    await authStore.setToken(completion.access_token)
    // This path has completed authentication or account binding. Clear referral
    // attribution before navigation so a later signup does not reuse the finished
    // flow context.
    clearAllAffiliateCodes()
    appStore.showSuccess(t('auth.loginSuccess'))
    await router.replace(redirectTo.value)
  } catch (e: unknown) {
    totpError.value = getRequestErrorMessage(e, t('auth.loginFailed'))
  } finally {
    isSubmitting.value = false
  }
}

// Mounting first resumes provider-specific return state or reads the legacy
// fragment, then exchanges the pending session without adoption choices when that
// path still applies.
onMounted(async () => {
  try {
    await ensurePublicSettingsLoaded()
  } catch {
    // If capability loading fails, binding recovery can still select a valid query
    // mode or browser fallback through resolveRequestedWeChatOAuthMode instead of
    // aborting initialization.
  }

  if (typeof route.query.email === 'string') {
    const email = route.query.email.trim()
    existingAccountEmail.value = email
    bindLoginEmail.value = email
    pendingAccountEmail.value = email
  }

  if (route.query.wechat_bind_existing === '1') {
    if (getAuthToken()) {
      await handleBindCurrentAccount()
      return
    }

    const resumePath = buildExistingAccountResumePath()
    if (!resumePath) {
      errorMessage.value = resolveWeChatOAuthUnavailableMessage()
      isProcessing.value = false
      return
    }

    const params = new URLSearchParams({
      redirect: resumePath,
    })
    const email = existingAccountEmail.value.trim()
    if (email) {
      params.set('email', email)
    }
    await router.replace(`/login?${params.toString()}`)
    return
  }

  const params = parseFragmentParams()
  const legacyLogin = readLegacyFragmentLogin(params)
  const legacyPendingToken = params.get('pending_oauth_token')?.trim() || ''
  const error = params.get('error')
  const errorDesc = params.get('error_description') || params.get('error_message') || ''
  const redirect = sanitizeRedirectPath(
    params.get('redirect') || (route.query.redirect as string | undefined) || '/dashboard',
  )

  try {
    if (legacyLogin) {
      // Legacy fragments carry refresh context with the access token. Publish that
      // context before the auth store initializes the corresponding session.
      persistOAuthTokenContext(legacyLogin)
      await authStore.setToken(legacyLogin.access_token)
      // This path has completed authentication or account binding. Clear referral
      // attribution before navigation so a later signup does not reuse the
      // finished flow context.
      clearAllAffiliateCodes()
      appStore.showSuccess(t('auth.loginSuccess'))
      await router.replace(redirect)
      return
    }

    if (error === 'invitation_required' && legacyPendingToken) {
      legacyPendingOAuthToken.value = legacyPendingToken
      redirectTo.value = redirect
      needsInvitation.value = true
      isProcessing.value = false
      return
    }

    if (error) {
      errorMessage.value = errorDesc || error
      isProcessing.value = false
      return
    }

    const completion = (await exchangePendingOAuthCompletion()) as PendingWeChatCompletion
    const completionRedirect = sanitizeRedirectPath(
      completion.redirect || (route.query.redirect as string | undefined) || '/dashboard',
    )
    applyAdoptionSuggestionState(completion)
    redirectTo.value = completionRedirect

    if (completion.error === 'invitation_required') {
      needsInvitation.value = true
      isProcessing.value = false
      persistPendingAuthSession(completionRedirect)
      return
    }

    if (applyTotpChallenge(completion)) {
      persistPendingAuthSession(completionRedirect)
      return
    }

    applyPendingAccountAction(completion)
    if (pendingAccountAction.value !== 'none') {
      isProcessing.value = false
      persistPendingAuthSession(completionRedirect)
      return
    }

    if (adoptionRequired.value && hasSuggestedProfile(completion)) {
      needsAdoptionConfirmation.value = true
      isProcessing.value = false
      persistPendingAuthSession(completionRedirect)
      return
    }

    await finalizeCompletion(completion, completionRedirect)
  } catch (e: unknown) {
    // Clear the saved continuation at this terminal or failed-initialization path
    // so a later account form does not resume the discarded pending session.
    clearPendingAuthSession()
    errorMessage.value = getRequestErrorMessage(e, t('auth.loginFailed'))
    isProcessing.value = false
  }
})
</script>

<style scoped></style>
