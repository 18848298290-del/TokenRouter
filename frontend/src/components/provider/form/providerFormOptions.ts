import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OpenAIOAuthClientPolicy } from '@/api/admin/providers.types'
import { RESPONSES_WS_POOLED, RESPONSES_WS_PER_SESSION } from '@/utils/responsesWsConnection'

// providerFormOptions.ts provides shared option values and localized labels for provider
// configuration controls. Composable factories run in component setup so labels follow
// the active locale, while cache duration choices use fixed wire values shared by create,
// edit, and bulk forms.

// CodexFingerprintMode selects disabled, device, session, or combined fingerprint fields.
export type CodexFingerprintMode = 'off' | 'device' | 'session' | 'full'
// RpmStrategy selects tiered request limits or sticky-session exemption behavior.
export type RpmStrategy = 'tiered' | 'sticky_exempt'
// AnthropicAPIKeyAuthScheme selects the header used to forward the API credential.
export type AnthropicAPIKeyAuthScheme = 'x_api_key' | 'authorization_bearer'

// useCodexFingerprintModeOptions returns reactive localized choices for Codex fingerprint
// scope. Call it during component setup so useI18n resolves the component context and
// computed labels follow later locale changes.
export function useCodexFingerprintModeOptions() {
  const { t } = useI18n()
  return computed(() => [
    {
      value: 'off' as CodexFingerprintMode,
      label: t('admin.providers.openai.codexFingerprintOff'),
    },
    {
      value: 'device' as CodexFingerprintMode,
      label: t('admin.providers.openai.codexFingerprintDevice'),
    },
    {
      value: 'session' as CodexFingerprintMode,
      label: t('admin.providers.openai.codexFingerprintSession'),
    },
    {
      value: 'full' as CodexFingerprintMode,
      label: t('admin.providers.openai.codexFingerprintFull'),
    },
  ])
}

// useResponsesWSConnectionModeOptions returns reactive localized choices for Responses
// WebSocket connection ownership. Call it during component setup so useI18n resolves the
// component context and computed labels follow later locale changes.
export function useResponsesWSConnectionModeOptions() {
  const { t } = useI18n()
  return computed(() => [
    { value: RESPONSES_WS_POOLED, label: t('admin.providers.openai.wsConnectionPooled') },
    { value: RESPONSES_WS_PER_SESSION, label: t('admin.providers.openai.wsConnectionPerSession') },
  ])
}

// useOpenAIOAuthClientPolicyOptions returns reactive localized choices for OpenAI OAuth
// client admission. Call it during component setup so useI18n resolves the component
// context and computed labels follow later locale changes.
export function useOpenAIOAuthClientPolicyOptions() {
  const { t } = useI18n()
  return computed(() => [
    { value: 'any' as OpenAIOAuthClientPolicy, label: t('admin.providers.openai.clientPolicyAny') },
    {
      value: 'codex_only' as OpenAIOAuthClientPolicy,
      label: t('admin.providers.openai.clientPolicyCodexOnly'),
    },
    {
      value: 'tls_router_matched_only' as OpenAIOAuthClientPolicy,
      label: t('admin.providers.openai.clientPolicyTLSRouterMatchedOnly'),
    },
  ])
}

// useUserMsgQueueModeOptions returns reactive localized choices for Anthropic
// user-message queue handling. Call it during component setup so useI18n resolves the
// component context and computed labels follow later locale changes.
export function useUserMsgQueueModeOptions() {
  const { t } = useI18n()
  return computed(() => [
    { value: '', label: t('admin.providers.quotaControl.rpmLimit.umqModeOff') },
    { value: 'throttle', label: t('admin.providers.quotaControl.rpmLimit.umqModeThrottle') },
    { value: 'serialize', label: t('admin.providers.quotaControl.rpmLimit.umqModeSerialize') },
  ])
}

// useWebSearchEmulationOptions returns reactive localized choices for Anthropic
// web-search emulation. Call it during component setup so useI18n resolves the component
// context and computed labels follow later locale changes.
export function useWebSearchEmulationOptions() {
  const { t } = useI18n()
  return computed(() => [
    { value: 'default', label: t('admin.providers.anthropic.webSearchDefault') },
    { value: 'enabled', label: t('admin.providers.anthropic.webSearchEnabled') },
    { value: 'disabled', label: t('admin.providers.anthropic.webSearchDisabled') },
  ])
}

// useAnthropicAPIKeyAuthSchemeOptions returns reactive localized choices for Anthropic
// API-key header authentication. Call it during component setup so useI18n resolves the
// component context and computed labels follow later locale changes.
export function useAnthropicAPIKeyAuthSchemeOptions() {
  const { t } = useI18n()
  return computed(() => [
    {
      value: 'x_api_key' as AnthropicAPIKeyAuthScheme,
      label: t('admin.providers.anthropic.apiKeyAuthSchemeXApiKey'),
    },
    {
      value: 'authorization_bearer' as AnthropicAPIKeyAuthScheme,
      label: t('admin.providers.anthropic.apiKeyAuthSchemeBearer'),
    },
  ])
}

// CACHE_TTL_OVERRIDE_TARGET_OPTIONS supplies the five-minute and one-hour wire durations
// supported by the shared cache override selector.
export const CACHE_TTL_OVERRIDE_TARGET_OPTIONS = [
  { value: '5m', label: '5m' },
  { value: '1h', label: '1h' },
]
