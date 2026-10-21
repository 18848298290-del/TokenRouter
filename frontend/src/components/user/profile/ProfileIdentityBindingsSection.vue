<template>
  <div :class="props.embedded ? 'space-y-4' : 'card overflow-hidden'">
    <div v-if="!props.embedded" class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-medium text-gray-900 dark:text-white">
        {{ t('profile.authBindings.title') }}
      </h2>
    </div>

    <div :class="props.embedded ? 'space-y-4' : 'divide-y divide-gray-100 dark:divide-dark-700'">
      <div v-if="props.embedded">
        <p class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('profile.authBindings.title') }}
        </p>
      </div>

      <div v-for="item in providerItems" :key="item.provider" :class="rowClass">
        <div class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
          <div class="flex min-w-0 flex-1 items-start gap-4">
            <div
              :class="providerIconClass(item.provider)"
              class="flex h-11 w-11 shrink-0 items-center justify-center rounded-control text-sm font-semibold"
            >
              <Icon v-if="item.provider === 'email'" name="mail" size="sm" class="text-current" />
              <span v-else>{{ providerInitial(item.provider) }}</span>
            </div>

            <div class="min-w-0 flex-1 space-y-3">
              <div class="flex flex-wrap items-center gap-2">
                <h3 class="font-medium text-gray-900 dark:text-white">
                  {{ item.label }}
                </h3>
                <span
                  :data-testid="`profile-binding-${item.provider}-status`"
                  :class="['badge', item.bound ? 'badge-success' : 'badge-gray']"
                >
                  {{
                    item.bound
                      ? t('profile.authBindings.status.bound')
                      : t('profile.authBindings.status.notBound')
                  }}
                </span>
              </div>

              <p
                v-if="providerSummary(item.provider)"
                class="text-sm text-gray-600 dark:text-gray-300"
              >
                {{ providerSummary(item.provider) }}
              </p>

              <div
                v-if="hasBindingDetails(item.provider, item.details)"
                class="grid gap-1 text-sm text-gray-500 dark:text-gray-400"
              >
                <p
                  v-if="item.provider !== 'email' && item.details?.display_name"
                  class="font-medium text-gray-700 dark:text-gray-200"
                >
                  {{ item.details.display_name }}
                </p>
                <p v-if="item.provider !== 'email' && item.details?.subject_hint">
                  {{ item.details.subject_hint }}
                </p>
                <p v-if="bindingCountLabel(item.details)">
                  {{ bindingCountLabel(item.details) }}
                </p>
                <p v-if="item.provider !== 'email' && bindingNote(item.details)">
                  {{ bindingNote(item.details) }}
                </p>
              </div>

              <div
                v-if="item.provider === 'email' && showEmailForm"
                data-testid="profile-binding-email-form"
                class="grid gap-2 sm:grid-cols-[minmax(0,1.4fr)_auto]"
              >
                <input
                  v-model.trim="emailBindingForm.email"
                  data-testid="profile-binding-email-input"
                  type="email"
                  class="input"
                  :placeholder="t('profile.authBindings.emailPlaceholder')"
                  :disabled="isSendingEmailCode || isBindingEmail"
                />
                <button
                  data-testid="profile-binding-email-send-code"
                  type="button"
                  class="btn btn-secondary btn-sm"
                  :disabled="isSendingEmailCode || isBindingEmail"
                  @click="sendEmailCode"
                >
                  {{
                    isSendingEmailCode
                      ? t('common.loading')
                      : t('profile.authBindings.sendCodeAction')
                  }}
                </button>
                <input
                  v-model.trim="emailBindingForm.verifyCode"
                  data-testid="profile-binding-email-code-input"
                  type="text"
                  inputmode="numeric"
                  maxlength="6"
                  class="input"
                  :placeholder="t('profile.authBindings.codePlaceholder')"
                  :disabled="isBindingEmail"
                />
                <input
                  v-model="emailBindingForm.password"
                  data-testid="profile-binding-email-password-input"
                  type="password"
                  class="input"
                  :placeholder="emailPasswordPlaceholder"
                  :disabled="isBindingEmail"
                />
                <button
                  data-testid="profile-binding-email-submit"
                  type="button"
                  class="btn btn-primary btn-sm sm:col-span-2"
                  :disabled="isBindingEmail"
                  @click="bindEmail"
                >
                  {{ isBindingEmail ? t('common.loading') : emailSubmitActionLabel }}
                </button>
              </div>
            </div>
          </div>

          <div class="flex shrink-0 flex-wrap items-center gap-3">
            <button
              v-if="item.provider === 'email' && compact && canManageEmail"
              data-testid="profile-binding-email-toggle"
              type="button"
              class="btn btn-secondary btn-sm"
              @click="toggleEmailForm"
            >
              {{
                showEmailForm
                  ? t('profile.authBindings.hideEmailFormAction')
                  : t('profile.authBindings.manageEmailAction')
              }}
            </button>
            <button
              v-if="item.canBind"
              :data-testid="`profile-binding-${item.provider}-action`"
              type="button"
              class="btn btn-primary btn-sm"
              @click="startBinding(item.provider)"
            >
              {{ t('profile.authBindings.bindAction', { providerName: item.label }) }}
            </button>
            <button
              v-if="item.canUnbind"
              :data-testid="`profile-binding-${item.provider}-unbind`"
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="unbindingProvider === item.provider"
              @click="handleUnbindForItem(item.provider, item.label)"
            >
              {{
                unbindingProvider === item.provider
                  ? t('common.loading')
                  : t('profile.authBindings.unbindAction')
              }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import {
  hasExplicitWeChatOAuthCapabilities,
  resolveWeChatOAuthStartStrict,
  type WeChatOAuthPublicSettings,
} from '@/api/auth'
import {
  bindEmailIdentity,
  sendEmailBindingCode,
  startOAuthBinding,
  unbindAuthIdentity,
} from '@/api/user'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore, useAuthStore } from '@/stores'
import type { User, UserAuthBindingStatus, UserAuthProvider } from '@/api/identity.types'

// ProfileIdentityBindingsSection.vue manages email and external sign-in identities for the
// profile page. Binding status accepts direct flags and compatible nested records, while
// action availability combines current bindings, public provider settings, and server-
// provided permissions. Cached WeChat settings take precedence over props and retain
// explicit per-flow capabilities.
//
// Successful operations publish their returned user to local display state and the shared
// auth store. A later parent profile update clears that temporary local override. Email
// binding and replacement share a form but use different validation and success messaging
// based on the existing identity. OAuth binding carries the current route as its return
// location, and compact presentation retains the same account actions behind an expandable
// email editor.

// BindableProvider identifies external sign-in methods eligible for OAuth binding
// operations.
type BindableProvider = Exclude<UserAuthProvider, 'email'>
// ConfigurableBindingProvider identifies providers whose binding availability comes from
// public settings.
type ConfigurableBindingProvider = 'linuxdo' | 'dingtalk' | 'oidc' | 'wechat'

// Profile and provider-availability props determine the displayed account details and
// binding actions; explicit WeChat capabilities retain their distinction from legacy
// combined availability.
const props = withDefaults(
  defineProps<{
    user: User | null // Current account profile.
    linuxdoEnabled?: boolean // Linux DO binding availability.
    dingtalkEnabled?: boolean // DingTalk binding availability.
    oidcEnabled?: boolean // OIDC binding availability.
    oidcProviderName?: string // Configured OIDC display name.
    wechatEnabled?: boolean // Legacy combined WeChat availability.
    wechatOpenEnabled?: boolean // Optional Open Platform availability.
    wechatMpEnabled?: boolean // Optional public-account WeChat availability.
    embedded?: boolean // Parent supplies section framing.
    compact?: boolean // Compact expandable email form.
  }>(),
  {
    linuxdoEnabled: false, // Linux DO binding availability.
    dingtalkEnabled: false, // DingTalk binding availability.
    oidcEnabled: false, // OIDC binding availability.
    oidcProviderName: 'OIDC', // Configured OIDC display name.
    wechatEnabled: false, // Legacy combined WeChat availability.
    wechatOpenEnabled: undefined, // Optional Open Platform availability.
    wechatMpEnabled: undefined, // Optional public-account WeChat availability.
    embedded: false, // Parent supplies section framing.
    compact: false, // Compact expandable email form.
  },
)

// Route and account stores supply the OAuth return location and shared profile, while app
// notifications report binding operations to the user.
const { t } = useI18n()
const route = useRoute()
const appStore = useAppStore()
const authStore = useAuthStore()

// localUser immediately exposes successful binding responses until the next parent profile
// update; request flags and the email draft belong to this section instance.
const localUser = ref<User | null>(null)
const isSendingEmailCode = ref(false)
const isBindingEmail = ref(false)
const isEmailFormExpanded = ref(!props.compact)
const unbindingProvider = ref<BindableProvider | null>(null)
const emailBindingForm = reactive({
  email: '', // Requested binding address.
  verifyCode: '', // Email confirmation code.
  password: '', // Binding or replacement password.
})

watch(
  () => props.user,
  (user) => {
    localUser.value = null
    if (!user) {
      return
    }
    if (typeof user.email === 'string' && !user.email.endsWith('.invalid')) {
      emailBindingForm.email = user.email
    }
  },
  { immediate: true },
)

watch(
  () => props.compact,
  (value) => {
    if (!value) {
      isEmailFormExpanded.value = true
    }
  },
  { immediate: true },
)

// currentUser prefers the locally returned profile; compact layout and row styling adapt the
// shared binding controls to their containing profile section.
const currentUser = computed(() => localUser.value ?? props.user)
const compact = computed(() => props.compact)
const rowClass = computed(() =>
  props.embedded
    ? compact.value
      ? 'rounded-surface border border-gray-100 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900/40'
      : 'rounded-surface border border-gray-100 bg-gray-50/70 p-4 dark:border-dark-700 dark:bg-dark-900/30'
    : 'px-6 py-5',
)
const emailBound = computed(() => getBindingStatus('email'))
// userEmailChangeEnabled permits replacing a bound email only when loaded public settings
// explicitly enable that operation for the account interface.
const userEmailChangeEnabled = computed(
  () => appStore.cachedPublicSettings?.user_email_change_enabled === true,
)
const canManageEmail = computed(() => !emailBound.value || userEmailChangeEnabled.value)
const showEmailForm = computed(
  () => canManageEmail.value && (!compact.value || isEmailFormExpanded.value),
)
const emailPasswordPlaceholder = computed(() =>
  emailBound.value
    ? t('profile.authBindings.replaceEmailPasswordPlaceholder')
    : t('profile.authBindings.passwordPlaceholder'),
)
const emailSubmitActionLabel = computed(() =>
  emailBound.value
    ? t('profile.authBindings.confirmEmailReplaceAction')
    : t('profile.authBindings.confirmEmailBindAction'),
)
// legacyBindingNoteKeys maps recognized server English notes to locale keys while preserving
// other server notes as their supplied text.
const legacyBindingNoteKeys: Record<string, string> = {
  'You can unbind this sign-in method.': 'profile.authBindings.notes.canUnbind',
  'Bind another sign-in method before unbinding.':
    'profile.authBindings.notes.bindAnotherBeforeUnbind',
}

// resolveLegacyCompatibleWeChatSettings normalizes the older combined WeChat switch into
// both capability flags. Explicit capability data passes through unchanged so disabled
// individual flows are not re-enabled by the legacy switch. Missing settings or a legacy
// value without a boolean leave capabilities unresolved. The strict OAuth resolver then
// chooses an available binding mode from this normalized input, with cached public settings
// taking precedence over props.
function resolveLegacyCompatibleWeChatSettings(
  settings: WeChatOAuthPublicSettings | null | undefined,
):
  | (WeChatOAuthPublicSettings & {
      wechat_oauth_open_enabled: boolean /* WeChat Open Platform availability. */
      wechat_oauth_mp_enabled: boolean /* WeChat public-account OAuth availability. */
    })
  | null {
  if (!settings) {
    return null
  }

  if (hasExplicitWeChatOAuthCapabilities(settings)) {
    return settings
  }

  if (typeof settings.wechat_oauth_enabled !== 'boolean') {
    return null
  }

  return {
    ...settings,
    wechat_oauth_open_enabled: settings.wechat_oauth_enabled,
    wechat_oauth_mp_enabled: settings.wechat_oauth_enabled,
  }
}

// wechatOAuthSettings prefers usable cached public settings, then derives compatible
// capabilities from props for the strict WeChat binding resolver.
const wechatOAuthSettings = computed<WeChatOAuthPublicSettings | null>(() => {
  const cachedSettings = resolveLegacyCompatibleWeChatSettings(appStore.cachedPublicSettings)
  if (cachedSettings) {
    return cachedSettings
  }

  return resolveLegacyCompatibleWeChatSettings({
    wechat_oauth_enabled: props.wechatEnabled,
    wechat_oauth_open_enabled: props.wechatOpenEnabled,
    wechat_oauth_mp_enabled: props.wechatMpEnabled,
  })
})

// resolvedWeChatBinding selects the available WeChat authorization mode consumed by both row
// availability and the binding launch action.
const resolvedWeChatBinding = computed(() =>
  resolveWeChatOAuthStartStrict(wechatOAuthSettings.value),
)

// normalizeBindingStatus converts boolean and detailed identity records into a nullable
// bound flag. An explicit bound boolean wins even when subject, issuer, or provider-key
// metadata is present, preventing an unbound detailed record from becoming bound through
// leftover identity fields. A missing record returns null so the caller can apply its own
// absence rule. Detailed records without an explicit flag use the presence of those identity
// fields as their compatible bound indication.
function normalizeBindingStatus(
  binding: boolean | UserAuthBindingStatus | undefined,
): boolean | null {
  if (typeof binding === 'boolean') {
    return binding
  }
  if (!binding) {
    return null
  }
  if (typeof binding.bound === 'boolean') {
    return binding.bound
  }
  return Boolean(binding.provider_subject || binding.issuer || binding.provider_key)
}

// getBindingStatus returns the resolved bound flag for the currently displayed account.
function getBindingStatus(provider: UserAuthProvider): boolean {
  return getBindingStatusForUser(currentUser.value, provider)
}

// getBindingStatusForUser resolves one provider's bound state across direct flags and two
// nested account representations. An explicit direct boolean wins, including false, before
// any nested metadata is considered. Otherwise auth_bindings takes precedence over
// identity_bindings and normalizeBindingStatus interprets either its boolean or detailed
// record. Unresolved data becomes false for row availability, so missing account identity
// information does not by itself expose an unbind action.
function getBindingStatusForUser(
  user: User | null | undefined,
  provider: UserAuthProvider,
): boolean {
  if (provider === 'email') {
    if (typeof user?.email_bound === 'boolean') {
      return user.email_bound
    }
    const nested = user?.auth_bindings?.email ?? user?.identity_bindings?.email
    const normalized = normalizeBindingStatus(nested)
    return normalized ?? false
  }

  const directFlag = user?.[`${provider}_bound` as keyof User]
  if (typeof directFlag === 'boolean') {
    return directFlag
  }

  const nested = user?.auth_bindings?.[provider] ?? user?.identity_bindings?.[provider]
  const normalized = normalizeBindingStatus(nested)
  return normalized ?? false
}

// getBindingDetails returns the detailed provider record when the nested binding has object
// form. Legacy boolean entries and missing bindings return null so rows omit details
// unavailable in that representation.
function getBindingDetails(provider: UserAuthProvider): UserAuthBindingStatus | null {
  const binding =
    currentUser.value?.auth_bindings?.[provider] ?? currentUser.value?.identity_bindings?.[provider]
  if (!binding || typeof binding === 'boolean') {
    return null
  }
  return binding
}

// getDisplayableEmail returns a trimmed email suitable for the binding summary. Synthetic
// .invalid addresses remain hidden while email is unbound, and missing account or email data
// returns empty text.
function getDisplayableEmail(user: User | null | undefined): string {
  const email = user?.email?.trim() || ''
  if (!email) {
    return ''
  }
  if (email.endsWith('.invalid') && !getBindingStatusForUser(user, 'email')) {
    return ''
  }
  return email
}

// isProviderEnabledForBinding reads provider-specific availability from props or the strict
// WeChat mode. Existing binding status and server can_bind permissions are applied
// separately by the row construction.
function isProviderEnabledForBinding(provider: ConfigurableBindingProvider): boolean {
  if (provider === 'linuxdo') {
    return props.linuxdoEnabled
  }
  if (provider === 'dingtalk') {
    return props.dingtalkEnabled
  }
  if (provider === 'oidc') {
    return props.oidcEnabled
  }
  return resolvedWeChatBinding.value.mode !== null
}

// providerItems combines bound state, public availability, and server action permissions so
// each provider row exposes the applicable bind or unbind controls.
const providerItems = computed(() => [
  {
    provider: 'email' as const,
    label: t('profile.authBindings.providers.email'),
    bound: getBindingStatus('email'),
    canBind: false,
    canUnbind: false,
    details: getBindingDetails('email'),
  },
  {
    provider: 'linuxdo' as const,
    label: t('profile.authBindings.providers.linuxdo'),
    bound: getBindingStatus('linuxdo'),
    canBind:
      !getBindingStatus('linuxdo') &&
      isProviderEnabledForBinding('linuxdo') &&
      (getBindingDetails('linuxdo')?.can_bind ?? true),
    canUnbind: Boolean(getBindingStatus('linuxdo') && getBindingDetails('linuxdo')?.can_unbind),
    details: getBindingDetails('linuxdo'),
  },
  {
    provider: 'dingtalk' as const,
    label: t('profile.authBindings.providers.dingtalk'),
    bound: getBindingStatus('dingtalk'),
    canBind:
      !getBindingStatus('dingtalk') &&
      isProviderEnabledForBinding('dingtalk') &&
      (getBindingDetails('dingtalk')?.can_bind ?? true),
    canUnbind: Boolean(getBindingStatus('dingtalk') && getBindingDetails('dingtalk')?.can_unbind),
    details: getBindingDetails('dingtalk'),
  },
  {
    provider: 'oidc' as const,
    label: t('profile.authBindings.providers.oidc', { providerName: props.oidcProviderName }),
    bound: getBindingStatus('oidc'),
    canBind:
      !getBindingStatus('oidc') &&
      isProviderEnabledForBinding('oidc') &&
      (getBindingDetails('oidc')?.can_bind ?? true),
    canUnbind: Boolean(getBindingStatus('oidc') && getBindingDetails('oidc')?.can_unbind),
    details: getBindingDetails('oidc'),
  },
  {
    provider: 'wechat' as const,
    label: t('profile.authBindings.providers.wechat'),
    bound: getBindingStatus('wechat'),
    canBind:
      !getBindingStatus('wechat') &&
      isProviderEnabledForBinding('wechat') &&
      (getBindingDetails('wechat')?.can_bind ?? true),
    canUnbind: Boolean(getBindingStatus('wechat') && getBindingDetails('wechat')?.can_unbind),
    details: getBindingDetails('wechat'),
  },
])

// providerInitial supplies the single-letter emblem used by the displayed provider row.
function providerInitial(provider: UserAuthProvider): string {
  if (provider === 'linuxdo') {
    return 'L'
  }
  if (provider === 'dingtalk') {
    return 'D'
  }
  if (provider === 'wechat') {
    return 'W'
  }
  if (provider === 'oidc') {
    return 'O'
  }
  return 'E'
}

// providerIconClass supplies the provider row's light and dark emblem colors.
function providerIconClass(provider: UserAuthProvider): string {
  if (provider === 'linuxdo') {
    return 'bg-orange-100 text-orange-600 dark:bg-orange-900/20 dark:text-orange-300'
  }
  if (provider === 'dingtalk') {
    return 'bg-blue-100 text-blue-600 dark:bg-blue-900/20 dark:text-blue-300'
  }
  if (provider === 'wechat') {
    return 'bg-green-100 text-green-600 dark:bg-green-900/20 dark:text-green-300'
  }
  if (provider === 'oidc') {
    return 'bg-sky-100 text-sky-600 dark:bg-sky-900/20 dark:text-sky-300'
  }
  return 'bg-primary-100 text-primary-600 dark:bg-primary-900/20 dark:text-primary-300'
}

// providerSummary returns the displayable account email or empty text for external
// providers.
function providerSummary(provider: UserAuthProvider): string {
  if (provider === 'email') {
    return getDisplayableEmail(currentUser.value)
  }
  return ''
}

// bindingCountLabel formats multiplicity only when detailed binding data contains a numeric
// count above one. Missing, nonnumeric, or single-binding counts contribute no label to the
// provider row.
function bindingCountLabel(details: UserAuthBindingStatus | null): string {
  if (!details || typeof details.bound_count !== 'number' || details.bound_count <= 1) {
    return ''
  }
  return t('profile.authBindings.boundCount', { count: details.bound_count })
}

// bindingNote resolves a server translation key before compatible legacy-note mappings. A
// key with no translation falls back to the trimmed server note, while missing details
// return empty text.
function bindingNote(details: UserAuthBindingStatus | null): string {
  if (!details) {
    return ''
  }

  const noteKey =
    details.note_key?.trim() || legacyBindingNoteKeys[details.note?.trim() || ''] || ''
  if (noteKey) {
    const translated = t(noteKey)
    if (translated !== noteKey) {
      return translated
    }
  }

  return details.note?.trim() || ''
}

// hasBindingDetails reports whether a row has identity details, multiplicity, or an
// explanatory note. Email rows omit external display-name and subject fields but may still
// show a multiple-binding count.
function hasBindingDetails(
  provider: UserAuthProvider,
  details: UserAuthBindingStatus | null,
): boolean {
  if (!details) {
    return false
  }

  const showsProviderIdentityDetails =
    provider !== 'email' && Boolean(details.display_name || details.subject_hint)
  const showsBindingNote = provider !== 'email' && Boolean(bindingNote(details))

  return Boolean(showsProviderIdentityDetails || bindingCountLabel(details) || showsBindingNote)
}

// toggleEmailForm expands or collapses the compact email binding editor.
function toggleEmailForm(): void {
  isEmailFormExpanded.value = !isEmailFormExpanded.value
}

// startBinding launches external OAuth binding using the current route as its return
// location. Email is ignored, and WeChat receives the resolved capability settings used to
// select its authorization mode.
function startBinding(provider: UserAuthProvider): void {
  if (provider === 'email') {
    return
  }
  startOAuthBinding(provider, {
    redirectTo: route.fullPath || '/profile',
    wechatOAuthSettings: provider === 'wechat' ? wechatOAuthSettings.value : null,
  })
}

// applyUpdatedUser publishes a binding response to local display state and the auth store.
function applyUpdatedUser(user: User): void {
  localUser.value = user
  authStore.user = user
}

// handleUnbind removes an external sign-in identity and publishes the returned profile. It
// marks the active provider during the request, reports errors through the app store, and
// clears that marker afterward.
async function handleUnbind(provider: BindableProvider, providerLabel: string): Promise<void> {
  unbindingProvider.value = provider
  try {
    const user = await unbindAuthIdentity(provider)
    applyUpdatedUser(user)
    appStore.showSuccess(t('profile.authBindings.unbindSuccess', { providerName: providerLabel }))
  } catch (error) {
    appStore.showError(
      (error as { message?: string /* Optional API failure explanation. */ }).message ||
        t('common.tryAgain'),
    )
  } finally {
    unbindingProvider.value = null
  }
}

// handleUnbindForItem forwards an external-provider row action to the asynchronous unbind
// handler. Email rows return immediately because their management flow binds or replaces
// addresses through the dedicated form.
function handleUnbindForItem(provider: UserAuthProvider, providerLabel: string): void {
  if (provider === 'email') {
    return
  }
  void handleUnbind(provider, providerLabel)
}

// validateEmailBindingForm validates email syntax and, when requested, confirmation
// credentials. Binding a previously unbound email also requires the local minimum password
// length; failures show the first validation error and return false.
function validateEmailBindingForm(requireCode: boolean): boolean {
  if (!emailBindingForm.email) {
    appStore.showError(t('auth.emailRequired'))
    return false
  }
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(emailBindingForm.email)) {
    appStore.showError(t('auth.invalidEmail'))
    return false
  }
  if (requireCode && !emailBindingForm.verifyCode) {
    appStore.showError(t('auth.codeRequired'))
    return false
  }
  if (requireCode && !emailBindingForm.password) {
    appStore.showError(t('auth.passwordRequired'))
    return false
  }
  if (requireCode && !emailBound.value && emailBindingForm.password.length < 6) {
    appStore.showError(t('auth.passwordMinLength'))
    return false
  }
  return true
}

// sendEmailCode validates the address and requests its binding confirmation code. It tracks
// the send action independently from binding and reports completion or failure while
// retaining the current form.
async function sendEmailCode(): Promise<void> {
  if (!validateEmailBindingForm(false)) {
    return
  }

  isSendingEmailCode.value = true
  try {
    await sendEmailBindingCode(emailBindingForm.email)
    appStore.showSuccess(t('profile.authBindings.codeSentTo', { email: emailBindingForm.email }))
  } catch (error) {
    appStore.showError(
      (error as { message?: string /* Optional API failure explanation. */ }).message ||
        t('auth.sendCodeFailed'),
    )
  } finally {
    isSendingEmailCode.value = false
  }
}

// bindEmail validates and submits the address, verification code, and password for binding
// or replacement. The returned profile updates local and shared account state before
// sensitive draft fields are cleared. The previous binding state is captured first so the
// success message describes the operation that occurred. Compact mode closes the form after
// success; request failures leave its values available for correction and reset the request
// indicator.
async function bindEmail(): Promise<void> {
  if (!validateEmailBindingForm(true)) {
    return
  }

  isBindingEmail.value = true
  try {
    const user = await bindEmailIdentity({
      email: emailBindingForm.email,
      verify_code: emailBindingForm.verifyCode,
      password: emailBindingForm.password,
    })
    const replacingBoundEmail = emailBound.value
    applyUpdatedUser(user)
    emailBindingForm.verifyCode = ''
    emailBindingForm.password = ''
    if (compact.value) {
      isEmailFormExpanded.value = false
    }
    appStore.showSuccess(
      replacingBoundEmail
        ? t('profile.authBindings.replaceSuccess')
        : t('profile.authBindings.bindSuccess'),
    )
  } catch (error) {
    appStore.showError(
      (error as { message?: string /* Optional API failure explanation. */ }).message ||
        t('common.tryAgain'),
    )
  } finally {
    isBindingEmail.value = false
  }
}
</script>
