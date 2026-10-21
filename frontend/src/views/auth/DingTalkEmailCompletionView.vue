<template>
  <AuthLayout>
    <div class="space-y-6">
      <div class="text-center">
        <h2 class="text-2xl font-bold text-gray-900 dark:text-white">
          {{ t('auth.dingtalk.createAccountTitle') }}
        </h2>
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
          {{ t('auth.oauthFlow.createAccountHint') }}
        </p>
      </div>

      <PendingOAuthCreateAccountForm
        test-id-prefix="dingtalk"
        :initial-email="initialEmail"
        :is-submitting="isSubmitting"
        :error-message="accountActionError"
        @submit="handleCreateAccount"
        @switch-to-bind="handleSwitchToBind"
      />
    </div>
  </AuthLayout>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { AuthLayout } from '@/components/layout'
import PendingOAuthCreateAccountForm, {
  type PendingOAuthCreateAccountPayload,
} from '@/components/auth/PendingOAuthCreateAccountForm.vue'
import { apiClient } from '@/api/client'
import { useAuthStore, useAppStore } from '@/stores'
import { persistOAuthTokenContext, type PendingOAuthExchangeResponse } from '@/api/auth'
import { clearAllAffiliateCodes } from '@/utils/oauthAffiliate'

// DingTalkEmailCompletionView.vue collects the email and password needed to finish
// a DingTalk signup. The shared form owns verification and captcha acquisition,
// while this page submits its draft and selects the next account step. A
// successful creation response installs refresh context before the access token
// and clears referral attribution before navigation.
//
// An existing-email choice returns the entered address to the DingTalk callback
// with a binding marker. Registration-disabled errors follow that same
// existing-account route with explanatory feedback. The marker lets callback
// initialization bypass another email-completion redirect and open password
// authentication instead.

// route and router connect callback or sign-in handlers to query values and
// navigation. Authentication stores and translators supply the session actions and
// notifications used by those handlers.
const route = useRoute() // current callback query context
const router = useRouter() // application navigation service
const { t } = useI18n() // translated authentication message resolver

const authStore = useAuthStore() // shared authentication state owner
const appStore = useAppStore() // public settings and toast owner

// isSubmitting disables the shared email-completion form during account creation.
// accountActionError returns server failures to that form, while initialEmail
// seeds its editable address from the route.
const isSubmitting = ref(false) // account-action request busy flag
const accountActionError = ref('') // account-action failure toast source

const initialEmail = (route.query.email as string | undefined) || '' // route-provided creation email suggestion

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

// handleCreateAccount submits the email-completion draft and resolves its returned
// account state. A token response persists refresh context before installing the
// access token and completing navigation. An existing-account choice reopens
// DingTalk callback binding with the submitted email, while disabled registration
// follows the same recovery route with an explanatory message. Other responses or
// request failures remain on the form, and submission state clears after each
// attempt.
async function handleCreateAccount(payload: PendingOAuthCreateAccountPayload) {
  accountActionError.value = ''
  if (!payload.email || !payload.password) return

  isSubmitting.value = true
  try {
    const { data } = await apiClient.post<
      PendingOAuthExchangeResponse & {
        step?: string // server continuation step

        redirect?: string // post-authentication destination path

        existing_account_bindable?: boolean // existing-account binding availability
      }
    >('/auth/oauth/pending/create-account', {
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
    })

    const redirect = sanitizeRedirectPath(
      data.redirect || (route.query.redirect as string | undefined),
    )

    if (data.access_token) {
      // Install the returned refresh context before the access token so session
      // initialization observes the metadata belonging to this authenticated
      // response.
      persistOAuthTokenContext(data)
      await authStore.setToken(data.access_token)
      // This path has completed authentication or account binding. Clear referral
      // attribution before navigation so a later signup does not reuse the
      // finished flow context.
      clearAllAffiliateCodes()
      appStore.showSuccess(t('auth.loginSuccess'))
      await router.replace(redirect)
      return
    }

    // An existing email can move the pending session to account choice. Return
    // with the entered email so callback binding can authenticate that account.
    if (data.step === 'choose_account_action_required' || data.existing_account_bindable === true) {
      navigateToBindLogin(payload.email)
      return
    }

    accountActionError.value = t('auth.loginFailed')
  } catch (e: unknown) {
    // When the server rejects creation because registration is disabled, explain
    // the restriction and return to existing-account binding with the entered
    // email retained.
    const err = e as {
      response?: {
        data?: {
          reason?: string // server failure reason
        } // HTTP response payload
      } // optional HTTP error response
    }
    if (err.response?.data?.reason === 'REGISTRATION_DISABLED') {
      appStore.showInfo(t('auth.dingtalk.registrationDisabledRedirectToBind'))
      navigateToBindLogin(payload.email)
      return
    }
    accountActionError.value = getRequestErrorMessage(e, t('auth.loginFailed'))
  } finally {
    isSubmitting.value = false
  }
}

// navigateToBindLogin returns to DingTalk callback with the bind marker and
// optional email. It carries the current redirect query so the callback can bypass
// email completion and authenticate an existing account.
function navigateToBindLogin(email: string) {
  const query: Record<string, string> = { bind: '1' }
  if (email) query.email = email
  const redirect = route.query.redirect as string | undefined
  if (redirect) query.redirect = redirect
  router.replace({ path: '/auth/dingtalk/callback', query })
}

// handleSwitchToBind forwards the creation form email to DingTalk binding.
function handleSwitchToBind(email: string) {
  navigateToBindLogin(email)
}
</script>
