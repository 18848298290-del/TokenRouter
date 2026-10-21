import { ref, reactive, computed, nextTick } from 'vue'
import { type SettingsSegmentedOption } from '@/components/common/settings/SettingsSegmented.vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores'
import {
  isRegistrationEmailSuffixDomainValid,
  normalizeRegistrationEmailSuffixDomain,
  parseRegistrationEmailSuffixWhitelistInput,
} from '@/utils/registrationEmailPolicy'
import type { SettingsForm } from './settingsState'
import type { SettingsSnapshotKey } from './settingsPersistence'

/**
 * useSecuritySettings.ts manages registration domain tags, forwarded client IP
 * headers, CAPTCHA selection, and the panel rate-limit draft. The settings page owns
 * the shared bulk form and keeps this controller alive across hidden tabs. CAPTCHA
 * uses one remembered provider and a master switch to update three stored enable
 * flags together.
 *
 * Panel rate limits have their own API and clean snapshot. Loading or saving applies
 * returned values before waiting for Vue controls to settle, then records that
 * target as clean. A failed load retains the existing draft, while a failed save
 * reports an error and keeps the dirty state available for retry.
 */

/**
 * SecuritySettingsForm selects the shared fields edited by security controls and
 * their draft actions.
 */
export type SecuritySettingsForm = Pick<
  SettingsForm,
  | 'aliyun_captcha_access_key_id'
  | 'aliyun_captcha_access_key_secret'
  | 'aliyun_captcha_access_key_secret_configured'
  | 'aliyun_captcha_enabled'
  | 'aliyun_captcha_prefix'
  | 'aliyun_captcha_region'
  | 'aliyun_captcha_scene_id'
  | 'api_key_acl_trust_forwarded_ip'
  | 'audit_log_retention_days'
  | 'email_verify_enabled'
  | 'forwarded_client_ip_headers'
  | 'frontend_url'
  | 'invitation_code_enabled'
  | 'password_reset_enabled'
  | 'promo_code_enabled'
  | 'registration_email_domain_quota_enabled'
  | 'registration_email_normalization'
  | 'registration_enabled'
  | 'session_binding_enabled'
  | 'step_up_enabled'
  | 'tencent_captcha_app_id'
  | 'tencent_captcha_app_secret_key'
  | 'tencent_captcha_app_secret_key_configured'
  | 'tencent_captcha_cloud_secret_id'
  | 'tencent_captcha_cloud_secret_id_configured'
  | 'tencent_captcha_cloud_secret_key'
  | 'tencent_captcha_cloud_secret_key_configured'
  | 'tencent_captcha_enabled'
  | 'tencent_captcha_region'
  | 'totp_enabled'
  | 'totp_encryption_key_configured'
  | 'turnstile_enabled'
  | 'turnstile_secret_key'
  | 'turnstile_secret_key_configured'
  | 'turnstile_site_key'
  | 'user_email_change_enabled'
>

/**
 * CaptchaProviderSelection identifies the provider remembered by the master CAPTCHA
 * switch.
 */
export type CaptchaProviderSelection = 'turnstile' | 'tencent' | 'aliyun'

/**
 * ForwardedClientIpHeaderResult distinguishes insertion, duplicate suppression,
 * invalid tokens, and a full header list.
 */
export type ForwardedClientIpHeaderResult = 'added' | 'duplicate' | 'invalid' | 'full'
/**
 * useSecuritySettings creates security editors around a shared settings draft during
 * component setup. Call the returned loaders from the page lifecycle, and provide a
 * clean-marker callback for the independent panelRateLimit save target.
 */
export function useSecuritySettings(
  form: SecuritySettingsForm,
  markSettingsClean: (key: SettingsSnapshotKey) => void,
) {
  /**
   * t and appStore provide translated validation feedback and save errors to the
   * settings page.
   */
  const { t } = useI18n()
  const appStore = useAppStore()

  /**
   * registrationEmailSuffixWhitelistTags stores normalized domains separately from
   * the bulk form, whose payload adds the API prefix.
   */
  const registrationEmailSuffixWhitelistTags = ref<string[]>([])

  /**
   * registrationEmailSuffixWhitelistDraft holds an unfinished domain until
   * separator keys or blur commit that input.
   */
  const registrationEmailSuffixWhitelistDraft = ref('')

  /**
   * forwardedClientIpHeaderDraft holds the unfinished header token; committing it
   * updates the shared bulk form list.
   */
  const forwardedClientIpHeaderDraft = ref('')

  /**
   * panelRateLimitLoading gates the initial form skeleton and is cleared after
   * either load success or failure.
   */
  const panelRateLimitLoading = ref(true)

  /**
   * panelRateLimitForm owns the separate rate-limit API draft and supplies the
   * independent panelRateLimit dirty snapshot.
   */
  const panelRateLimitForm = reactive({
    /** enabled bypasses both authenticated and public-IP counter checks when this draft is saved as false. */
    enabled: true, // panel limiter enabled
    // user_rpm, heavy_rpm, and public_ip_rpm use requests per minute. Zero disables
    // the corresponding backend bucket; the server validates every saved rate.
    user_rpm: 240, // authenticated requests per minute
    heavy_rpm: 60, // heavy requests per minute
    /** exempt_admin bypasses authenticated administrator buckets; the public-IP bucket has its own eligibility rules. */
    exempt_admin: true, // administrator rate-limit exemption
    public_ip_rpm: 300, // public requests per minute
  })

  /**
   * captchaProviderSelection remembers the selected vendor while all three stored
   * CAPTCHA enable flags are switched off.
   */
  const captchaProviderSelection = ref<CaptchaProviderSelection>('turnstile')

  /**
   * applyCaptchaSelection sets the three stored CAPTCHA flags from one provider
   * choice. Passing null disables every provider while leaving the remembered
   * selector value available for the next enable action.
   */
  function applyCaptchaSelection(provider: CaptchaProviderSelection | null): void {
    form.turnstile_enabled = provider === 'turnstile'
    form.tencent_captcha_enabled = provider === 'tencent'
    form.aliyun_captcha_enabled = provider === 'aliyun'
  }

  /**
   * captchaMasterEnabled reads any active vendor flag and writes all flags using the
   * remembered provider selection.
   */
  const captchaMasterEnabled = computed({
    get: () =>
      form.turnstile_enabled || form.tencent_captcha_enabled || form.aliyun_captcha_enabled,
    set: (enabled: boolean) =>
      applyCaptchaSelection(enabled ? captchaProviderSelection.value : null),
  })

  /**
   * captchaProviderOptions supplies translated labels and stable test identifiers
   * for the mutually exclusive vendor selector.
   */
  const captchaProviderOptions = computed<SettingsSegmentedOption<CaptchaProviderSelection>[]>(
    () => [
      {
        value: 'turnstile',
        label: t('admin.settings.captcha.providerTurnstile'),
        testid: 'captcha-provider-turnstile',
      },
      {
        value: 'tencent',
        label: t('admin.settings.captcha.providerTencent'),
        testid: 'captcha-provider-tencent',
      },
      {
        value: 'aliyun',
        label: t('admin.settings.captcha.providerAliyun'),
        testid: 'captcha-provider-aliyun',
      },
    ],
  )

  /**
   * tencentCaptchaRegionOptions labels the stored regional values used by the
   * Tencent console and documentation links.
   */
  const tencentCaptchaRegionOptions = computed<SettingsSegmentedOption<'cn' | 'intl'>[]>(() => [
    {
      value: 'cn',
      label: t('admin.settings.tencentCaptcha.regionCn'),
      testid: 'tencent-captcha-region-cn',
    },
    {
      value: 'intl',
      label: t('admin.settings.tencentCaptcha.regionIntl'),
      testid: 'tencent-captcha-region-intl',
    },
  ])

  /**
   * aliyunCaptchaRegionOptions labels the China and Singapore regional values
   * accepted by the Aliyun settings field.
   */
  const aliyunCaptchaRegionOptions = computed<SettingsSegmentedOption<'cn' | 'sgp'>[]>(() => [
    { value: 'cn', label: t('admin.settings.aliyunCaptcha.regionCn') },
    { value: 'sgp', label: t('admin.settings.aliyunCaptcha.regionSgp') },
  ])

  /**
   * selectCaptchaProvider remembers the selected vendor and enables it in the shared
   * form. Choosing a provider clears the other provider flags even if the master
   * switch was previously off.
   */
  function selectCaptchaProvider(provider: CaptchaProviderSelection): void {
    captchaProviderSelection.value = provider
    applyCaptchaSelection(provider)
  }

  /**
   * tencentCaptchaLinks selects regional console, cloud credential, and web
   * documentation addresses from the current Tencent region field.
   */
  const tencentCaptchaLinks = computed(() =>
    form.tencent_captcha_region === 'intl'
      ? {
          console: 'https://console.tencentcloud.com/captcha/graphical',
          cloudKeys: 'https://console.tencentcloud.com/cam/capi',
          webDocs: 'https://www.tencentcloud.com/document/product/1159/49680',
        }
      : {
          console: 'https://console.cloud.tencent.com/captcha',
          cloudKeys: 'https://console.cloud.tencent.com/cam/capi',
          webDocs: 'https://cloud.tencent.com/document/product/1110/36841',
        },
  )

  /**
   * syncCaptchaProviderSelection restores the selector after settings hydration
   * using Tencent, Aliyun, then Turnstile priority. When all stored flags are false
   * it keeps the current selection for a later enable action.
   */
  function syncCaptchaProviderSelection(): void {
    // Hydrated flags can contain multiple active providers; select the established
    // display priority without rewriting those stored draft flags.
    if (form.tencent_captcha_enabled) {
      captchaProviderSelection.value = 'tencent'
    } else if (form.aliyun_captcha_enabled) {
      captchaProviderSelection.value = 'aliyun'
    } else if (form.turnstile_enabled) {
      captchaProviderSelection.value = 'turnstile'
    }
  }

  /**
   * registrationEmailSuffixWhitelistSeparatorKeys commits typed domains on
   * delimiters while the key handler leaves composition events alone.
   */
  const registrationEmailSuffixWhitelistSeparatorKeys = new Set([' ', ',', '，', 'Enter', 'Tab'])

  /**
   * removeRegistrationEmailSuffixWhitelistTag removes the matching normalized domain
   * from the editable whitelist.
   */
  function removeRegistrationEmailSuffixWhitelistTag(suffix: string) {
    registrationEmailSuffixWhitelistTags.value = registrationEmailSuffixWhitelistTags.value.filter(
      (item) => item !== suffix,
    )
  }

  /**
   * addRegistrationEmailSuffixWhitelistTag normalizes and appends a valid domain to
   * the whitelist. Invalid domains and existing entries are ignored, and callers
   * receive no validation error from this helper.
   */
  function addRegistrationEmailSuffixWhitelistTag(raw: string) {
    // The tag editor stores bare normalized domains; invalid and repeated entries do
    // not become additional API whitelist entries.
    const suffix = normalizeRegistrationEmailSuffixDomain(raw)
    if (
      !isRegistrationEmailSuffixDomainValid(suffix) ||
      registrationEmailSuffixWhitelistTags.value.includes(suffix)
    ) {
      return
    }
    registrationEmailSuffixWhitelistTags.value = [
      ...registrationEmailSuffixWhitelistTags.value,
      suffix,
    ]
  }

  /**
   * commitRegistrationEmailSuffixWhitelistDraft attempts to add the pending domain
   * and clears that draft. Empty drafts are ignored; invalid or duplicate nonempty
   * drafts are cleared after the insertion attempt.
   */
  function commitRegistrationEmailSuffixWhitelistDraft() {
    if (!registrationEmailSuffixWhitelistDraft.value) {
      return
    }
    addRegistrationEmailSuffixWhitelistTag(registrationEmailSuffixWhitelistDraft.value)
    registrationEmailSuffixWhitelistDraft.value = ''
  }

  /**
   * handleRegistrationEmailSuffixWhitelistDraftInput normalizes the visible domain
   * draft as input changes.
   */
  function handleRegistrationEmailSuffixWhitelistDraftInput() {
    registrationEmailSuffixWhitelistDraft.value = normalizeRegistrationEmailSuffixDomain(
      registrationEmailSuffixWhitelistDraft.value,
    )
  }

  /**
   * handleRegistrationEmailSuffixWhitelistDraftKeydown commits delimiter keys and
   * removes the last tag on empty Backspace. Composition events pass through
   * unchanged, while delimiter events prevent the browser default before committing.
   */
  function handleRegistrationEmailSuffixWhitelistDraftKeydown(event: KeyboardEvent) {
    if (event.isComposing) {
      return
    }

    if (registrationEmailSuffixWhitelistSeparatorKeys.has(event.key)) {
      event.preventDefault()
      commitRegistrationEmailSuffixWhitelistDraft()
      return
    }

    if (
      event.key === 'Backspace' &&
      !registrationEmailSuffixWhitelistDraft.value &&
      registrationEmailSuffixWhitelistTags.value.length > 0
    ) {
      registrationEmailSuffixWhitelistTags.value.pop()
    }
  }

  /**
   * handleRegistrationEmailSuffixWhitelistPaste splits nonempty clipboard text into
   * normalized domain tags. It prevents native insertion for that text, while empty
   * or whitespace-only clipboard contents keep the browser behavior.
   */
  function handleRegistrationEmailSuffixWhitelistPaste(event: ClipboardEvent) {
    const text = event.clipboardData?.getData('text') || ''
    if (!text.trim()) {
      return
    }
    event.preventDefault()
    const tokens = parseRegistrationEmailSuffixWhitelistInput(text)
    for (const token of tokens) {
      addRegistrationEmailSuffixWhitelistTag(token)
    }
  }

  /**
   * forwardedClientIpHeaderSeparatorKeys commits header drafts on these delimiters
   * before the browser processes their default action.
   */
  const forwardedClientIpHeaderSeparatorKeys = new Set([' ', ',', '，', 'Enter', 'Tab'])

  /**
   * forwardedClientIpHeaderTokenPattern restricts draft names to HTTP token
   * characters before canonical casing and duplicate checks.
   */
  const forwardedClientIpHeaderTokenPattern = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/

  /**
   * maxForwardedClientIpHeaders caps the ordered candidate headers accepted by both
   * bulk normalization and interactive tag insertion.
   */
  const maxForwardedClientIpHeaders = 16

  /**
   * normalizeForwardedClientIpHeader trims and canonicalizes a valid HTTP header
   * token for display. Invalid input returns an empty string, allowing list
   * normalization and interactive insertion to share the same rejection rule.
   */
  function normalizeForwardedClientIpHeader(raw: string): string {
    const header = raw.trim()
    if (!forwardedClientIpHeaderTokenPattern.test(header)) {
      return ''
    }

    // Canonical display casing lets tags look consistent while insertion and bulk-
    // save checks compare header names case-insensitively.
    return header
      .toLowerCase()
      .split('-')
      .map((part) => `${part.charAt(0).toUpperCase()}${part.slice(1)}`)
      .join('-')
  }

  /**
   * normalizeForwardedClientIpHeaders returns canonical header names in first-
   * occurrence order for bulk saving. Nonarrays become empty lists; nonstrings,
   * invalid tokens, case-insensitive duplicates, and entries beyond the configured
   * cap are skipped.
   */
  function normalizeForwardedClientIpHeaders(value: unknown): string[] {
    // A malformed list cannot supply candidate headers; return an empty collection
    // that the bulk payload can serialize.
    if (!Array.isArray(value)) {
      return []
    }

    const headers: string[] = []
    const seen = new Set<string>()
    for (const raw of value) {
      if (typeof raw !== 'string') {
        continue
      }
      const header = normalizeForwardedClientIpHeader(raw)
      const key = header.toLowerCase()
      // Keep the first usable occurrence within the supported cap so save
      // normalization preserves the administrator's header priority.
      if (!header || seen.has(key) || headers.length >= maxForwardedClientIpHeaders) {
        continue
      }
      seen.add(key)
      headers.push(header)
    }
    return headers
  }

  /**
   * removeForwardedClientIpHeader removes the selected display value from the shared
   * header list.
   */
  function removeForwardedClientIpHeader(header: string) {
    form.forwarded_client_ip_headers = form.forwarded_client_ip_headers.filter(
      (item) => item !== header,
    )
  }

  /**
   * addForwardedClientIpHeader canonicalizes a token and appends it when space
   * remains. Its result distinguishes invalid input, case-insensitive duplicates,
   * and a full list so callers choose the appropriate feedback.
   */
  function addForwardedClientIpHeader(raw: string): ForwardedClientIpHeaderResult {
    const header = normalizeForwardedClientIpHeader(raw)
    if (!header) {
      return 'invalid'
    }
    if (
      form.forwarded_client_ip_headers.some((item) => item.toLowerCase() === header.toLowerCase())
    ) {
      return 'duplicate'
    }
    if (form.forwarded_client_ip_headers.length >= maxForwardedClientIpHeaders) {
      return 'full'
    }
    form.forwarded_client_ip_headers = [...form.forwarded_client_ip_headers, header]
    return 'added'
  }

  /**
   * showForwardedClientIpHeaderError reports invalid tokens and list-cap violations
   * through translated toasts. Added and duplicate results produce no message, which
   * lets callers pass every insertion result directly to this helper.
   */
  function showForwardedClientIpHeaderError(result: ForwardedClientIpHeaderResult) {
    if (result === 'invalid') {
      appStore.showError(t('admin.settings.apiKeyAcl.forwardedClientIpHeaderInvalid'))
    } else if (result === 'full') {
      appStore.showError(
        t('admin.settings.apiKeyAcl.forwardedClientIpHeadersLimit', {
          max: maxForwardedClientIpHeaders,
        }),
      )
    }
  }

  /**
   * commitForwardedClientIpHeaderDraft inserts the pending token and reports invalid
   * or full-list results. It clears every nonempty draft after the attempt,
   * including duplicate input, while empty input has no effect.
   */
  function commitForwardedClientIpHeaderDraft() {
    const draft = forwardedClientIpHeaderDraft.value
    if (!draft) {
      return
    }
    const result = addForwardedClientIpHeader(draft)
    showForwardedClientIpHeaderError(result)
    forwardedClientIpHeaderDraft.value = ''
  }

  /**
   * handleForwardedClientIpHeaderKeydown commits separator keys and removes the last
   * header on empty Backspace. IME composition events pass through, and recognized
   * separators prevent native browser handling before the commit.
   */
  function handleForwardedClientIpHeaderKeydown(event: KeyboardEvent) {
    if (event.isComposing) {
      return
    }
    if (forwardedClientIpHeaderSeparatorKeys.has(event.key)) {
      event.preventDefault()
      commitForwardedClientIpHeaderDraft()
      return
    }
    if (
      event.key === 'Backspace' &&
      !forwardedClientIpHeaderDraft.value &&
      form.forwarded_client_ip_headers.length > 0
    ) {
      form.forwarded_client_ip_headers.pop()
    }
  }

  /**
   * handleForwardedClientIpHeaderPaste appends delimited clipboard tokens and
   * reports the last insertion error. Empty clipboard text keeps native behavior;
   * accepted earlier tokens remain when a later token is invalid or exceeds the cap.
   */
  function handleForwardedClientIpHeaderPaste(event: ClipboardEvent) {
    const text = event.clipboardData?.getData('text') || ''
    if (!text.trim()) {
      return
    }
    event.preventDefault()

    // Paste accepts usable tokens as it proceeds; one final toast reports the last
    // failure while retaining earlier additions.
    let error: ForwardedClientIpHeaderResult | undefined
    for (const token of text.split(/[,，;\r\n]+/)) {
      if (!token.trim()) {
        continue
      }
      const result = addForwardedClientIpHeader(token)
      if (result === 'invalid' || result === 'full') {
        error = result
      }
    }
    if (error) {
      showForwardedClientIpHeaderError(error)
    }
  }

  /**
   * loadPanelRateLimitSettings loads the independent panel limiter draft and records
   * its clean snapshot. Failed reads retain the current values, and both outcomes
   * wait for Vue input normalization before marking the target clean.
   */
  async function loadPanelRateLimitSettings() {
    panelRateLimitLoading.value = true
    try {
      const settings = await adminAPI.settings.getPanelRateLimitSettings()
      Object.assign(panelRateLimitForm, settings)
    } catch (_error: unknown) {
      // Keep the existing rate-limit draft when reading fails so its controls remain
      // usable with the last values.
    } finally {
      panelRateLimitLoading.value = false
    }
    // Wait for mounted input controls to normalize the loaded draft before the dirty
    // tracker records its baseline.
    await nextTick()
    markSettingsClean('panelRateLimit')
  }

  /**
   * savePanelRateLimitSettings writes the limiter draft and returns whether the
   * request succeeded. Success applies the server response before recording a
   * settled clean snapshot; failure shows an error and leaves that snapshot
   * unchanged.
   */
  async function savePanelRateLimitSettings(): Promise<boolean> {
    try {
      const updated = await adminAPI.settings.updatePanelRateLimitSettings({
        enabled: panelRateLimitForm.enabled,
        user_rpm: panelRateLimitForm.user_rpm,
        heavy_rpm: panelRateLimitForm.heavy_rpm,
        exempt_admin: panelRateLimitForm.exempt_admin,
        public_ip_rpm: panelRateLimitForm.public_ip_rpm,
      })
      // Apply the accepted server values before waiting for controls, so the clean
      // snapshot describes the successful save.
      Object.assign(panelRateLimitForm, updated)
      await nextTick()
      markSettingsClean('panelRateLimit')
      return true
    } catch (error: unknown) {
      appStore.showError(
        extractApiErrorMessage(error, t('admin.settings.panelRateLimit.saveFailed')),
      )
      return false
    }
  }
  /**
   * The returned refs and actions are shared with SecuritySettings and the page
   * hydration, validation, and save routines.
   */
  return {
    registrationEmailSuffixWhitelistTags,
    registrationEmailSuffixWhitelistDraft,
    forwardedClientIpHeaderDraft,
    panelRateLimitLoading,
    panelRateLimitForm,
    captchaProviderSelection,
    applyCaptchaSelection,
    captchaMasterEnabled,
    captchaProviderOptions,
    tencentCaptchaRegionOptions,
    aliyunCaptchaRegionOptions,
    selectCaptchaProvider,
    tencentCaptchaLinks,
    syncCaptchaProviderSelection,
    registrationEmailSuffixWhitelistSeparatorKeys,
    removeRegistrationEmailSuffixWhitelistTag,
    addRegistrationEmailSuffixWhitelistTag,
    commitRegistrationEmailSuffixWhitelistDraft,
    handleRegistrationEmailSuffixWhitelistDraftInput,
    handleRegistrationEmailSuffixWhitelistDraftKeydown,
    handleRegistrationEmailSuffixWhitelistPaste,
    forwardedClientIpHeaderSeparatorKeys,
    forwardedClientIpHeaderTokenPattern,
    maxForwardedClientIpHeaders,
    normalizeForwardedClientIpHeader,
    normalizeForwardedClientIpHeaders,
    removeForwardedClientIpHeader,
    addForwardedClientIpHeader,
    showForwardedClientIpHeaderError,
    commitForwardedClientIpHeaderDraft,
    handleForwardedClientIpHeaderKeydown,
    handleForwardedClientIpHeaderPaste,
    loadPanelRateLimitSettings,
    savePanelRateLimitSettings,
  }
}
