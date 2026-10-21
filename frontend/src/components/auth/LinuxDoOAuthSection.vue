<template>
  <div class="space-y-4">
    <button type="button" :disabled="disabled" class="btn btn-secondary w-full" @click="startLogin">
      <svg
        class="icon mr-2"
        viewBox="0 0 16 16"
        version="1.1"
        xmlns="http://www.w3.org/2000/svg"
        width="1em"
        height="1em"
        style="color: rgb(233, 84, 32); width: 20px; height: 20px"
        aria-hidden="true"
      >
        <g id="linuxdo_icon" data-name="linuxdo_icon">
          <path
            d="m7.44,0s.09,0,.13,0c.09,0,.19,0,.28,0,.14,0,.29,0,.43,0,.09,0,.18,0,.27,0q.12,0,.25,0t.26.08c.15.03.29.06.44.08,1.97.38,3.78,1.47,4.95,3.11.04.06.09.12.13.18.67.96,1.15,2.11,1.3,3.28q0,.19.09.26c0,.15,0,.29,0,.44,0,.04,0,.09,0,.13,0,.09,0,.19,0,.28,0,.14,0,.29,0,.43,0,.09,0,.18,0,.27,0,.08,0,.17,0,.25q0,.19-.08.26c-.03.15-.06.29-.08.44-.38,1.97-1.47,3.78-3.11,4.95-.06.04-.12.09-.18.13-.96.67-2.11,1.15-3.28,1.3q-.19,0-.26.09c-.15,0-.29,0-.44,0-.04,0-.09,0-.13,0-.09,0-.19,0-.28,0-.14,0-.29,0-.43,0-.09,0-.18,0-.27,0-.08,0-.17,0-.25,0q-.19,0-.26-.08c-.15-.03-.29-.06-.44-.08-1.97-.38-3.78-1.47-4.95-3.11q-.07-.09-.13-.18c-.67-.96-1.15-2.11-1.3-3.28q0-.19-.09-.26c0-.15,0-.29,0-.44,0-.04,0-.09,0-.13,0-.09,0-.19,0-.28,0-.14,0-.29,0-.43,0-.09,0-.18,0-.27,0-.08,0-.17,0-.25q0-.19.08-.26c.03-.15.06-.29.08-.44.38-1.97,1.47-3.78,3.11-4.95.06-.04.12-.09.18-.13C4.42.73,5.57.26,6.74.1,7,.07,7.15,0,7.44,0Z"
            fill="#EFEFEF"
          ></path>
          <path
            d="m1.27,11.33h13.45c-.94,1.89-2.51,3.21-4.51,3.88-1.99.59-3.96.37-5.8-.57-1.25-.7-2.67-1.9-3.14-3.3Z"
            fill="#FEB005"
          ></path>
          <path
            d="m12.54,1.99c.87.7,1.82,1.59,2.18,2.68H1.27c.87-1.74,2.33-3.13,4.2-3.78,2.44-.79,5-.47,7.07,1.1Z"
            fill="#1D1D1F"
          ></path>
        </g>
      </svg>
      {{ t('auth.linuxdo.signIn') }}
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

// LinuxDoOAuthSection.vue presents the LinuxDo sign-in entry and emits its start
// request to the parent authentication gate. A nonblank promotion code travels
// with the emitted request, while referral attribution is saved for the callback.
// The parent performs captcha checks and navigation after receiving the event.

// props supplies parent-owned configuration and draft input. emit reports
// authentication actions back to that owner, which decides whether to submit or
// navigate.
const props = withDefaults(
  defineProps<{
    disabled?: boolean // parent action disable flag

    affCode?: string // parent referral code

    promoCode?: string // optional promotion code

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

// startLogin emits the LinuxDo request with the current redirect or /dashboard
// fallback. It saves resolved referral attribution first, leaving consent checks,
// action captcha, and provider navigation to the parent.
function startLogin(): void {
  const redirectTo = (route.query.redirect as string) || '/dashboard'
  // Save referral attribution before leaving the current sign-in entry, allowing
  // the callback to include it if the provider requires account creation.
  storeOAuthAffiliateCode(
    resolveAffiliateCode(props.affCode, route.query.aff, route.query.aff_code),
  )
  const params: Record<string, string> = { redirect: redirectTo }
  const promoCode = props.promoCode?.trim()
  if (promoCode) {
    params.promo_code = promoCode
  }
  emit('start', { provider: 'linuxdo', params })
}
</script>
