<template>
  <div class="space-y-4">
    <button
      type="button"
      :disabled="buttonDisabled"
      class="btn btn-secondary w-full"
      @click="startLogin"
    >
      <span
        class="mr-2 inline-flex h-5 w-5 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-700 dark:bg-green-900/30 dark:text-green-300"
      >
        W
      </span>
      {{ t('auth.oidc.signIn', { providerName }) }}
    </button>

    <p
      v-if="disabledHint"
      data-testid="wechat-oauth-hint"
      class="text-sm text-amber-600 dark:text-amber-400"
    >
      {{ disabledHint }}
    </p>

    <div v-if="showDivider" class="flex items-center gap-3">
      <div class="h-px flex-1 bg-gray-200 dark:bg-dark-700"></div>
      <span class="text-xs text-gray-500 dark:text-dark-400">
        {{ t('auth.oauthOrContinue') }}
      </span>
      <div class="h-px flex-1 bg-gray-200 dark:bg-dark-700"></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { resolveWeChatOAuthStart, type OAuthLoginStart } from '@/api/auth'
import { useAppStore } from '@/stores'
import { resolveAffiliateCode, storeOAuthAffiliateCode } from '@/utils/oauthAffiliate'

// WechatOAuthSection.vue presents the WeChat sign-in entry and emits its start
// request to the parent authentication gate. Public capability settings and
// browser context determine whether a usable mode exists, and unavailable modes
// show a localized explanation. The parent performs captcha checks and navigation
// after receiving the event.

// props supplies parent-owned configuration and draft input. emit reports
// authentication actions back to that owner, which decides whether to submit or
// navigate.
const props = withDefaults(
  defineProps<{
    disabled?: boolean // parent action disable flag

    affCode?: string // parent referral code

    // showDivider defaults to true when omitted. Parents can hide the separator
    // when composing several provider sections in the same authentication form.
    showDivider?: boolean // provider separator visibility
  }>(),
  {
    showDivider: true,
  },
) // parent component inputs
const emit = defineEmits<{
  start: [request: OAuthLoginStart] // parent authentication start request
}>() // parent component event dispatcher

// appStore supplies cached public settings for browser-capability selection. The
// route and translator provide the destination and visible explanations emitted
// alongside the selected WeChat mode.
const appStore = useAppStore() // public settings and toast owner
const route = useRoute() // current callback query context
const { t, locale } = useI18n() // message resolver and active locale
// The provider display values supply translated button or callback labels. They
// remain separate from the fixed provider identifier sent in authentication
// requests.
const providerName = computed(() => t('auth.wechatProviderName')) // displayed authentication provider name

// localizeWeChatHint chooses Chinese copy for Chinese locale prefixes and English
// otherwise.
function localizeWeChatHint(zh: string, en: string): string {
  return locale.value.startsWith('zh') ? zh : en
}

// resolvedStart selects a usable WeChat mode from browser context and cached
// settings. buttonDisabled combines that result with the parent gate, and
// disabledHint explains capability rejection.
const resolvedStart = computed(() => resolveWeChatOAuthStart(appStore.cachedPublicSettings)) // configured WeChat browser capability
const buttonDisabled = computed(() => props.disabled || resolvedStart.value.mode === null) // combined parent and capability gate
const disabledHint = computed(() => {
  if (props.disabled) {
    return ''
  }
  switch (resolvedStart.value.unavailableReason) {
    case 'external_browser_required':
      return t('auth.oauthFlow.wechatSystemBrowserOnly')
    case 'wechat_browser_required':
      return t('auth.oauthFlow.wechatBrowserOnly')
    case 'native_app_required':
      return localizeWeChatHint(
        '当前仅配置微信移动应用登录，需要在原生 App 中通过微信 SDK 发起授权。',
        'This site only has WeChat mobile app login configured. Continue from the native app through the WeChat SDK.',
      )
    case 'not_configured':
      return t('auth.oauthFlow.wechatNotConfigured')
    default:
      return ''
  }
}) // unavailable WeChat mode explanation

// Fetch public settings only when no cached snapshot or completed load exists,
// allowing capability selection to update without repeating an already completed
// settings request.
onMounted(() => {
  if (!appStore.cachedPublicSettings && !appStore.publicSettingsLoaded) {
    appStore.fetchPublicSettings()
  }
})

// startLogin emits the Wechat request with the current redirect or /dashboard
// fallback. It saves resolved referral attribution first, leaving consent checks,
// action captcha, and provider navigation to the parent. Disabled or unavailable
// modes return without emitting a request.
function startLogin(): void {
  if (buttonDisabled.value || !resolvedStart.value.mode) {
    return
  }
  const redirectTo = (route.query.redirect as string) || '/dashboard'
  // Save referral attribution before leaving the current sign-in entry, allowing
  // the callback to include it if the provider requires account creation.
  storeOAuthAffiliateCode(
    resolveAffiliateCode(props.affCode, route.query.aff, route.query.aff_code),
  )
  const mode = resolvedStart.value.mode
  emit('start', {
    provider: 'wechat',
    params: { mode, redirect: redirectTo },
  })
}
</script>
