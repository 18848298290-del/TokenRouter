<template>
  <BaseDialog
    :show="show"
    :title="t('admin.providers.reAuthorizeProvider')"
    width="normal"
    @close="handleClose"
  >
    <div v-if="provider" class="space-y-4">
      <!-- Provider Info -->
      <div
        class="rounded-surface border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800"
      >
        <div class="flex items-center gap-3">
          <div
            :class="[
              'flex h-10 w-10 items-center justify-center rounded-control bg-gradient-to-br',
              isOpenAILike
                ? 'from-green-500 to-green-600'
                : isGemini
                  ? 'from-blue-500 to-blue-600'
                  : isAntigravity
                    ? 'from-purple-500 to-purple-600'
                    : 'from-orange-500 to-orange-600',
            ]"
          >
            <Icon name="sparkles" size="md" class="text-white" />
          </div>
          <div>
            <span class="block font-semibold text-gray-900 dark:text-white">{{
              provider.name
            }}</span>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{
                isOpenAI
                  ? t('admin.providers.openaiProvider')
                  : isGemini
                    ? t('admin.providers.geminiProvider')
                    : isAntigravity
                      ? t('admin.providers.antigravityProvider')
                      : t('admin.providers.claudeCodeProvider')
              }}
            </span>
          </div>
        </div>
      </div>

      <!-- Add Method Selection (Claude only) -->
      <fieldset v-if="isAnthropic" class="border-0 p-0">
        <legend class="input-label">{{ t('admin.providers.oauth.authMethod') }}</legend>
        <div class="mt-2 flex gap-4">
          <label class="flex cursor-pointer items-center">
            <input
              v-model="addMethod"
              type="radio"
              value="oauth"
              class="mr-2 text-primary-600 focus:ring-primary-500"
            />
            <span class="text-sm text-gray-700 dark:text-gray-300">{{
              t('admin.providers.types.oauth')
            }}</span>
          </label>
          <label class="flex cursor-pointer items-center">
            <input
              v-model="addMethod"
              type="radio"
              value="setup-token"
              class="mr-2 text-primary-600 focus:ring-primary-500"
            />
            <span class="text-sm text-gray-700 dark:text-gray-300">{{
              t('admin.providers.setupTokenLongLived')
            }}</span>
          </label>
        </div>
      </fieldset>

      <!-- Gemini OAuth Type Display (read-only) -->
      <div
        v-if="isGemini"
        class="rounded-surface border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800"
      >
        <div class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.providers.oauth.gemini.oauthTypeLabel') }}
        </div>
        <div class="flex items-center gap-3">
          <div
            :class="[
              'flex h-8 w-8 shrink-0 items-center justify-center rounded-control',
              geminiOAuthType === 'google_one'
                ? 'bg-purple-500 text-white'
                : geminiOAuthType === 'code_assist'
                  ? 'bg-blue-500 text-white'
                  : 'bg-amber-500 text-white',
            ]"
          >
            <Icon v-if="geminiOAuthType === 'google_one'" name="user" size="sm" />
            <Icon v-else-if="geminiOAuthType === 'code_assist'" name="cloud" size="sm" />
            <Icon v-else name="sparkles" size="sm" />
          </div>
          <div>
            <span class="block text-sm font-medium text-gray-900 dark:text-white">
              {{
                geminiOAuthType === 'google_one'
                  ? 'Google One'
                  : geminiOAuthType === 'code_assist'
                    ? t('admin.providers.gemini.oauthType.builtInTitle')
                    : t('admin.providers.gemini.oauthType.customTitle')
              }}
            </span>
            <span class="text-xs text-gray-500 dark:text-gray-400">
              {{
                geminiOAuthType === 'google_one'
                  ? '个人提供商'
                  : geminiOAuthType === 'code_assist'
                    ? t('admin.providers.gemini.oauthType.builtInDesc')
                    : t('admin.providers.gemini.oauthType.customDesc')
              }}
            </span>
          </div>
        </div>
      </div>

      <OAuthAuthorizationFlow
        ref="oauthFlowRef"
        :add-method="addMethod"
        :auth-url="currentAuthUrl"
        :session-id="currentSessionId"
        :loading="currentLoading"
        :error="currentError"
        :show-help="isAnthropic"
        :show-proxy-warning="isAnthropic"
        :show-cookie-option="isAnthropic"
        :allow-multiple="false"
        :method-label="t('admin.providers.inputMethod')"
        :platform="
          isOpenAI ? 'openai' : isGemini ? 'gemini' : isAntigravity ? 'antigravity' : 'anthropic'
        "
        :show-project-id="isGemini && geminiOAuthType === 'code_assist'"
        @generate-url="handleGenerateUrl"
        @cookie-auth="handleCookieAuth"
      />
    </div>

    <template #footer>
      <div v-if="provider" class="flex justify-between gap-3">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button
          v-if="isManualInputMethod"
          type="button"
          :disabled="!canExchangeCode"
          class="btn btn-primary"
          @click="handleExchangeCode"
        >
          <Icon
            name="loader"
            size="sm"
            :animate-on-hover="false"
            v-if="currentLoading"
            class="-ml-1 mr-2 h-4 w-4 animate-spin"
          />
          {{
            currentLoading
              ? t('admin.providers.oauth.verifying')
              : t('admin.providers.oauth.completeAuth')
          }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import {
  useProviderOAuth,
  type AddMethod,
  type AuthInputMethod,
} from '@/composables/useProviderOAuth'
import { useOpenAIOAuth } from '@/composables/useOpenAIOAuth'
import { useGeminiOAuth } from '@/composables/useGeminiOAuth'
import { useAntigravityOAuth } from '@/composables/useAntigravityOAuth'
import type { Provider } from '@/api/admin/providers.types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import OAuthAuthorizationFlow from './OAuthAuthorizationFlow.vue'

// ReAuthProviderModal.vue replaces credentials for the provider shown in the shared
// reauthorization dialog. Platform-specific composables generate URLs and exchange codes,
// and the embedded flow exposes input drafts to the dialog. Anthropic supports OAuth and
// setup-token endpoints, while Gemini retains its selected OAuth variant.
//
// OpenAI and Anthropic apply credentials through the dedicated credential endpoint.
// Gemini and Antigravity first update the provider and then clear its error in separate
// requests, so the second step can fail after credentials are saved. Completion emits
// success and closes the dialog; requests have no local identity generation guard.

// OAuthFlowExposed describes the unwrapped draft values exposed by the mounted
// authorization flow.
interface OAuthFlowExposed {
  authCode: string // Entered or extracted authorization code.
  oauthState: string // Callback state retained for exchange.
  projectId: string // Optional project identifier draft.
  sessionKey: string // Cookie authorization input draft.
  inputMethod: AuthInputMethod // Selected credential input method.
  // reset clears the flow's credential and project drafts and callback state,
  // restores its initialInputMethod prop, and closes help. The owning dialog
  // decides when its authorization lifecycle should discard those entered values.
  reset: () => void
}

// Props identifies the current provider and parent-controlled visibility of this dialog.
interface Props {
  show: boolean // Parent-controlled dialog visibility.
  // Current provider supplied by the caller; pending requests may still complete after
  // replacement.
  provider: Provider | null
}

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<Props>()
// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  close: [] // Parent dialog close notification.
  // Successful reauthorization notification; some branches include the refreshed provider
  // and others omit it.
  reauthorized: [provider?: Provider]
}>()

// appStore displays operation feedback through the shared notification area used by the
// provider administration screens.
const appStore = useAppStore()
// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()

// claudeOAuth owns Anthropic OAuth and setup-token session state used by this shared
// reauthorization dialog and its cookie path.
const claudeOAuth = useProviderOAuth()
// openaiOAuth owns OpenAI URL generation, state, and credential conversion used when this
// dialog receives an OpenAI provider.
const openaiOAuth = useOpenAIOAuth()
// geminiOAuth owns Gemini authorization state and token conversion while the dialog
// retains the selected OAuth variant separately.
const geminiOAuth = useGeminiOAuth()
// antigravityOAuth owns Antigravity authorization state and token conversion for the
// shared dialog provider update sequence described below.
const antigravityOAuth = useAntigravityOAuth()

// oauthFlowRef exposes unwrapped drafts from the mounted input flow; optional access
// handles times when that child is absent.
const oauthFlowRef = ref<OAuthFlowExposed | null>(null)

// addMethod selects Anthropic OAuth or setup-token operations and resets to OAuth
// whenever this dialog state is cleared.
const addMethod = ref<AddMethod>('oauth')
// geminiOAuthType restores the saved supported variant on opening and defaults unknown
// variants to Code Assist for authorization.
const geminiOAuthType = ref<'code_assist' | 'google_one' | 'ai_studio'>('code_assist')

// isOpenAI selects the OpenAI branch from the current provider platform supplied by the
// parent dialog owner.
const isOpenAI = computed(() => props.provider?.platform === 'openai')
// isOpenAILike currently mirrors isOpenAI and selects the same authorization branch
// throughout this shared dialog implementation.
const isOpenAILike = computed(() => isOpenAI.value)
// isGemini selects the Gemini branch from the current provider platform supplied by the
// parent dialog owner.
const isGemini = computed(() => props.provider?.platform === 'gemini')
// isAnthropic selects Anthropic-specific mode restoration and cookie controls from the
// current provider platform supplied by the caller.
const isAnthropic = computed(() => props.provider?.platform === 'anthropic')
// isAntigravity selects the Antigravity authorization branch from the current provider
// platform supplied by the parent dialog owner.
const isAntigravity = computed(() => props.provider?.platform === 'antigravity')

// currentAuthUrl presents the selected platform composable URL, with the Anthropic flow
// used for the remaining platform values.
const currentAuthUrl = computed(() => {
  if (isOpenAILike.value) return openaiOAuth.authUrl.value
  if (isGemini.value) return geminiOAuth.authUrl.value
  if (isAntigravity.value) return antigravityOAuth.authUrl.value
  return claudeOAuth.authUrl.value
})
// currentSessionId presents the selected platform authorization session ID needed by the
// code-exchange action and its enabled state.
const currentSessionId = computed(() => {
  if (isOpenAILike.value) return openaiOAuth.sessionId.value
  if (isGemini.value) return geminiOAuth.sessionId.value
  if (isAntigravity.value) return antigravityOAuth.sessionId.value
  return claudeOAuth.sessionId.value
})
// currentLoading follows the selected platform composable so URL generation and exchange
// can disable their corresponding dialog controls.
const currentLoading = computed(() => {
  if (isOpenAILike.value) return openaiOAuth.loading.value
  if (isGemini.value) return geminiOAuth.loading.value
  if (isAntigravity.value) return antigravityOAuth.loading.value
  return claudeOAuth.loading.value
})
// currentError presents the selected platform composable failure text beside the shared
// authorization controls in this dialog.
const currentError = computed(() => {
  if (isOpenAILike.value) return openaiOAuth.error.value
  if (isGemini.value) return geminiOAuth.error.value
  if (isAntigravity.value) return antigravityOAuth.error.value
  return claudeOAuth.error.value
})

// isManualInputMethod selects code exchange for OpenAI, Gemini, and Antigravity, and
// otherwise follows the child-selected manual method.
const isManualInputMethod = computed(() => {
  return (
    isOpenAILike.value ||
    isGemini.value ||
    isAntigravity.value ||
    oauthFlowRef.value?.inputMethod === 'manual'
  )
})

// canExchangeCode requires nonblank code, an authorization session ID, and an idle
// platform flow before enabling code exchange.
const canExchangeCode = computed(() => {
  const authCode = oauthFlowRef.value?.authCode || ''
  const sessionId = currentSessionId.value
  const loading = currentLoading.value
  return authCode.trim() && sessionId && !loading
})

watch(
  () => props.show,
  (newVal) => {
    if (newVal && props.provider) {
      if (
        isAnthropic.value &&
        (props.provider.type === 'oauth' || props.provider.type === 'setup-token')
      ) {
        addMethod.value = props.provider.type as AddMethod
      }
      if (isGemini.value) {
        const creds = (props.provider.credentials || {}) as Record<string, unknown>
        geminiOAuthType.value =
          creds.oauth_type === 'google_one'
            ? 'google_one'
            : creds.oauth_type === 'ai_studio'
              ? 'ai_studio'
              : 'code_assist'
      }
    } else {
      resetState()
    }
  },
)

// resetState resets the selected authorization modes and each platform composable draft.
// It also resets the mounted input flow when present; pending platform requests follow
// their composable lifecycle behavior.
const resetState = () => {
  addMethod.value = 'oauth'
  geminiOAuthType.value = 'code_assist'
  claudeOAuth.resetState()
  openaiOAuth.resetState()
  geminiOAuth.resetState()
  antigravityOAuth.resetState()
  oauthFlowRef.value?.reset()
}

// handleClose asks the parent to close the shared reauthorization dialog.
const handleClose = () => {
  emit('close')
}

// handleGenerateUrl generates an authorization URL using the current provider platform
// and proxy. Gemini also supplies its OAuth variant, saved tier, and optional Code Assist
// project draft before the parent displays the result.
const handleGenerateUrl = async () => {
  if (!props.provider) return

  if (isOpenAILike.value) {
    await openaiOAuth.generateAuthUrl(props.provider.proxy_id)
  } else if (isGemini.value) {
    const creds = (props.provider.credentials || {}) as Record<string, unknown>
    const tierId = typeof creds.tier_id === 'string' ? creds.tier_id : undefined
    const projectId =
      geminiOAuthType.value === 'code_assist' ? oauthFlowRef.value?.projectId : undefined
    await geminiOAuth.generateAuthUrl(
      props.provider.proxy_id,
      projectId,
      geminiOAuthType.value,
      tierId,
    )
  } else if (isAntigravity.value) {
    await antigravityOAuth.generateAuthUrl(props.provider.proxy_id)
  } else {
    await claudeOAuth.generateAuthUrl(addMethod.value, props.provider.proxy_id)
  }
}

// handleExchangeCode exchanges the entered authorization code and persists credentials
// for the current provider. OpenAI, Gemini, and Antigravity require session state, using
// pasted callback state before the generated value. Anthropic chooses OAuth or
// setup-token exchange from the draft mode.
//
// OpenAI and Anthropic use applyOAuthCredentials to return the updated provider. Gemini
// and Antigravity perform update followed by clearError as separate requests, so a later
// failure can leave credentials already saved. Success feedback and close happen after
// each branch completes its required writes.
const handleExchangeCode = async () => {
  if (!props.provider) return

  const authCode = oauthFlowRef.value?.authCode || ''
  if (!authCode.trim()) return

  if (isOpenAILike.value) {
    const oauthClient = openaiOAuth
    const sessionId = oauthClient.sessionId.value
    if (!sessionId) return
    // Pasted callback state takes precedence over generated state because the entered code
    // must be exchanged with the state associated with its browser authorization.
    const stateToUse = (oauthFlowRef.value?.oauthState || oauthClient.oauthState.value || '').trim()
    if (!stateToUse) {
      oauthClient.error.value = t('admin.providers.oauth.authFailed')
      appStore.showError(oauthClient.error.value)
      return
    }

    const tokenInfo = await oauthClient.exchangeAuthCode(
      authCode.trim(),
      sessionId,
      stateToUse,
      props.provider.proxy_id,
      props.provider.tls_fingerprint_router_id,
    )
    if (!tokenInfo) return

    const credentials = oauthClient.buildCredentials(tokenInfo)
    const extra = oauthClient.buildExtraInfo(tokenInfo)

    try {
      // The credential application endpoint receives the exchanged identity and Extra
      // together before the parent is notified that reauthorization completed successfully.
      const updatedProvider = await adminAPI.providers.applyOAuthCredentials(props.provider.id, {
        type: 'oauth',
        credentials,
        extra,
      })

      appStore.showSuccess(t('admin.providers.reAuthorizedSuccess'))
      emit('reauthorized', updatedProvider)
      handleClose()
    } catch (error: any) {
      oauthClient.error.value =
        error.response?.data?.detail || t('admin.providers.oauth.authFailed')
      appStore.showError(oauthClient.error.value)
    }
  } else if (isGemini.value) {
    const sessionId = geminiOAuth.sessionId.value
    if (!sessionId) return

    const stateFromInput = oauthFlowRef.value?.oauthState || ''
    // Use callback state when supplied and generated state otherwise, retaining the
    // session proof needed by the Gemini exchange endpoint.
    const stateToUse = stateFromInput || geminiOAuth.state.value
    if (!stateToUse) return

    const tokenInfo = await geminiOAuth.exchangeAuthCode({
      code: authCode.trim(),
      sessionId,
      state: stateToUse,
      proxyId: props.provider.proxy_id,
      oauthType: geminiOAuthType.value,
      tierId:
        typeof (props.provider.credentials as any)?.tier_id === 'string'
          ? ((props.provider.credentials as any).tier_id as string)
          : undefined,
    })
    if (!tokenInfo) return

    const credentials = geminiOAuth.buildCredentials(tokenInfo)

    try {
      // Credential update commits before error clearing in this branch; a failure in
      // clearError can therefore leave the newly saved credentials in place.
      await adminAPI.providers.update(props.provider.id, {
        type: 'oauth',
        credentials,
      })
      await adminAPI.providers.clearError(props.provider.id)
      appStore.showSuccess(t('admin.providers.reAuthorizedSuccess'))
      emit('reauthorized')
      handleClose()
    } catch (error: any) {
      geminiOAuth.error.value =
        error.response?.data?.detail || t('admin.providers.oauth.authFailed')
      appStore.showError(geminiOAuth.error.value)
    }
  } else if (isAntigravity.value) {
    const sessionId = antigravityOAuth.sessionId.value
    if (!sessionId) return

    const stateFromInput = oauthFlowRef.value?.oauthState || ''
    // Use callback state when supplied and generated state otherwise, retaining the
    // session proof needed by the Antigravity exchange endpoint.
    const stateToUse = stateFromInput || antigravityOAuth.state.value
    if (!stateToUse) return

    const tokenInfo = await antigravityOAuth.exchangeAuthCode({
      code: authCode.trim(),
      sessionId,
      state: stateToUse,
      proxyId: props.provider.proxy_id,
    })
    if (!tokenInfo) return

    const credentials = antigravityOAuth.buildCredentials(tokenInfo)

    try {
      // Credential update commits before error clearing in this branch; a failure in
      // clearError can therefore leave the newly saved credentials in place.
      await adminAPI.providers.update(props.provider.id, {
        type: 'oauth',
        credentials,
      })
      await adminAPI.providers.clearError(props.provider.id)
      appStore.showSuccess(t('admin.providers.reAuthorizedSuccess'))
      emit('reauthorized')
      handleClose()
    } catch (error: any) {
      antigravityOAuth.error.value =
        error.response?.data?.detail || t('admin.providers.oauth.authFailed')
      appStore.showError(antigravityOAuth.error.value)
    }
  } else {
    const sessionId = claudeOAuth.sessionId.value
    if (!sessionId) return

    claudeOAuth.loading.value = true
    claudeOAuth.error.value = ''

    try {
      const proxyConfig = props.provider.proxy_id ? { proxy_id: props.provider.proxy_id } : {}
      const endpoint =
        addMethod.value === 'oauth'
          ? '/admin/providers/exchange-code'
          : '/admin/providers/exchange-setup-token-code'

      const tokenInfo = await adminAPI.providers.exchangeCode(endpoint, {
        session_id: sessionId,
        code: authCode.trim(),
        ...proxyConfig,
      })

      const extra = claudeOAuth.buildExtraInfo(tokenInfo)

      // The credential application endpoint receives the exchanged identity and Extra
      // together before the parent is notified that reauthorization completed successfully.
      const updatedProvider = await adminAPI.providers.applyOAuthCredentials(props.provider.id, {
        type: addMethod.value as 'oauth' | 'setup-token',
        credentials: tokenInfo as unknown as Record<string, unknown>,
        extra,
      })

      appStore.showSuccess(t('admin.providers.reAuthorizedSuccess'))
      emit('reauthorized', updatedProvider)
      handleClose()
    } catch (error: any) {
      claudeOAuth.error.value =
        error.response?.data?.detail || t('admin.providers.oauth.authFailed')
      appStore.showError(claudeOAuth.error.value)
    } finally {
      claudeOAuth.loading.value = false
    }
  }
}

// handleCookieAuth exchanges an Anthropic cookie draft and applies its credentials to the
// current provider. Missing providers and OpenAI skip the request; other platform
// eligibility is controlled by the template.
//
// The exchange endpoint follows the OAuth or setup-token draft mode. Credential
// persistence follows successful exchange, then emits the updated provider and closes.
// Errors remain in the Anthropic flow, and finally clears its loading flag even when a
// write fails.
const handleCookieAuth = async (sessionKey: string) => {
  if (!props.provider || isOpenAILike.value) return

  claudeOAuth.loading.value = true
  claudeOAuth.error.value = ''

  try {
    const proxyConfig = props.provider.proxy_id ? { proxy_id: props.provider.proxy_id } : {}
    const endpoint =
      addMethod.value === 'oauth'
        ? '/admin/providers/cookie-auth'
        : '/admin/providers/setup-token-cookie-auth'

    const tokenInfo = await adminAPI.providers.exchangeCode(endpoint, {
      session_id: '',
      code: sessionKey.trim(),
      ...proxyConfig,
    })

    const extra = claudeOAuth.buildExtraInfo(tokenInfo)

    // The credential application endpoint receives the exchanged identity and Extra
    // together before the parent is notified that reauthorization completed successfully.
    const updatedProvider = await adminAPI.providers.applyOAuthCredentials(props.provider.id, {
      type: addMethod.value as 'oauth' | 'setup-token',
      credentials: tokenInfo as unknown as Record<string, unknown>,
      extra,
    })

    appStore.showSuccess(t('admin.providers.reAuthorizedSuccess'))
    emit('reauthorized', updatedProvider)
    handleClose()
  } catch (error: any) {
    claudeOAuth.error.value =
      error.response?.data?.detail || t('admin.providers.oauth.cookieAuthFailed')
  } finally {
    claudeOAuth.loading.value = false
  }
}
</script>
