<template>
  <AppLayout>
    <!-- The profile card fills the main region and arranges its internal sections in two columns on wide screens. -->
    <div data-testid="profile-shell" class="mx-auto w-full min-w-0 max-w-screen-2xl">
      <ProfileInfoCard
        :user="user"
        :linuxdo-enabled="linuxdoOAuthEnabled"
        :dingtalk-enabled="dingtalkOAuthEnabled"
        :oidc-enabled="oidcOAuthEnabled"
        :oidc-provider-name="oidcOAuthProviderName"
        :wechat-enabled="wechatOAuthEnabled"
        :wechat-open-enabled="wechatOAuthOpenEnabled"
        :wechat-mp-enabled="wechatOAuthMPEnabled"
      >
        <!-- Security and notification settings occupy the companion column. -->
        <template #side>
          <ProfilePasswordForm />
          <!-- The TOTP card decides visibility from the feature status returned by its own endpoint. -->
          <ProfileTotpCard />
          <ProfilePasskeyCard v-if="passkeyEnabled" :enabled="passkeyEnabled" />

          <ProfileBalanceNotifyCard
            v-if="user && balanceLowNotifyEnabled"
            :enabled="user.balance_notify_enabled ?? true"
            :threshold="user.balance_notify_threshold"
            :extra-emails="user.balance_notify_extra_emails ?? []"
            :system-default-threshold="systemDefaultThreshold"
            :user-email="user.email"
          />
        </template>
      </ProfileInfoCard>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import ProfileBalanceNotifyCard from '@/components/user/profile/ProfileBalanceNotifyCard.vue'
import ProfileInfoCard from '@/components/user/profile/ProfileInfoCard.vue'
import ProfilePasswordForm from '@/components/user/profile/ProfilePasswordForm.vue'
import ProfileTotpCard from '@/components/user/profile/ProfileTotpCard.vue'
import ProfilePasskeyCard from '@/components/user/profile/ProfilePasskeyCard.vue'
import { isWeChatWebOAuthEnabled } from '@/api/auth'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'

// ProfileView.vue assembles profile details, balance notifications, and account security
// sections. It refreshes the user and public settings independently at mount, then passes
// feature availability and configured provider labels to the sections that own their editing
// flows.

// The app store supplies public feature settings while the auth store supplies the shared
// profile consumed by editable account sections.
const appStore = useAppStore()
const authStore = useAuthStore()
const user = computed(() => authStore.user)

// Feature flags start disabled until settings load; optional WeChat capability flags remain
// undefined when the server supplies no explicit per-flow value.
const balanceLowNotifyEnabled = ref(false)
const systemDefaultThreshold = ref(0)
const linuxdoOAuthEnabled = ref(false)
const dingtalkOAuthEnabled = ref(false)
const wechatOAuthEnabled = ref(false)
const wechatOAuthOpenEnabled = ref<boolean | undefined>(undefined)
const wechatOAuthMPEnabled = ref<boolean | undefined>(undefined)
const oidcOAuthEnabled = ref(false)
const oidcOAuthProviderName = ref('OIDC')
const passkeyEnabled = ref(false)

// Load the profile and public settings concurrently so an unavailable settings response does
// not prevent the account record from refreshing independently.
onMounted(async () => {
  const profileRefresh = authStore.refreshUser().catch((error) => {
    console.error('Failed to refresh profile:', error)
  })

  const settingsLoad = appStore
    .fetchPublicSettings()
    .then((settings) => {
      if (!settings) {
        return
      }
      balanceLowNotifyEnabled.value = settings.balance_low_notify_enabled ?? false
      systemDefaultThreshold.value = settings.balance_low_notify_threshold ?? 0
      linuxdoOAuthEnabled.value = settings.linuxdo_oauth_enabled ?? false
      dingtalkOAuthEnabled.value = settings.dingtalk_oauth_enabled ?? false
      wechatOAuthEnabled.value = isWeChatWebOAuthEnabled(settings)
      wechatOAuthOpenEnabled.value =
        typeof settings.wechat_oauth_open_enabled === 'boolean'
          ? settings.wechat_oauth_open_enabled
          : undefined
      wechatOAuthMPEnabled.value =
        typeof settings.wechat_oauth_mp_enabled === 'boolean'
          ? settings.wechat_oauth_mp_enabled
          : undefined
      oidcOAuthEnabled.value = settings.oidc_oauth_enabled ?? false
      oidcOAuthProviderName.value = settings.oidc_oauth_provider_name || 'OIDC'
      passkeyEnabled.value = settings.passkey_enabled === true
    })
    .catch((error) => {
      console.error('Failed to load settings:', error)
    })

  await Promise.all([profileRefresh, settingsLoad])
})
</script>
