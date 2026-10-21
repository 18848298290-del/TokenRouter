<template>
  <div class="space-y-4">
    <button type="button" :disabled="disabled" class="btn btn-secondary w-full" @click="startLogin">
      <svg
        class="icon mr-2"
        viewBox="0 0 24 24"
        xmlns="http://www.w3.org/2000/svg"
        width="20"
        height="20"
        aria-hidden="true"
        style="flex-shrink: 0"
      >
        <circle cx="12" cy="12" r="12" fill="#1677FF" />
        <text
          x="12"
          y="17"
          font-family="sans-serif"
          font-size="13"
          font-weight="bold"
          fill="white"
          text-anchor="middle"
        >
          {{ '钉' }}
        </text>
      </svg>
      {{ t('auth.dingtalk.signIn') }}
    </button>

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
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import type { OAuthLoginStart } from '@/api/auth'
import { resolveAffiliateCode, storeOAuthAffiliateCode } from '@/utils/oauthAffiliate'

// DingTalkOAuthSection.vue presents the DingTalk sign-in entry and emits its start
// request to the parent authentication gate. The parent receives the redirect and
// provider identity after referral attribution has been saved. The parent performs
// captcha checks and navigation after receiving the event.

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

// route and router connect callback or sign-in handlers to query values and
// navigation. Authentication stores and translators supply the session actions and
// notifications used by those handlers.
const route = useRoute() // current callback query context
const { t } = useI18n() // translated authentication message resolver

// startLogin emits the DingTalk request with the current redirect or /dashboard
// fallback. It saves resolved referral attribution first, leaving consent checks,
// action captcha, and provider navigation to the parent.
function startLogin(): void {
  const redirectTo = (route.query.redirect as string) || '/dashboard'
  // Save referral attribution before leaving the current sign-in entry, allowing
  // the callback to include it if the provider requires account creation.
  storeOAuthAffiliateCode(
    resolveAffiliateCode(props.affCode, route.query.aff, route.query.aff_code),
  )
  emit('start', { provider: 'dingtalk', params: { redirect: redirectTo } })
}
</script>
