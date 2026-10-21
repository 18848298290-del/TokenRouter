import type { Provider } from '@/api/admin/providers.types'

/**
 * upstreamUsage.ts shares API-key upstream usage eligibility between provider
 * presentation and manual query controls. Zhipu requires coding mode, while
 * other API-key providers pass the support predicate. An explicit enabled flag
 * overrides the platform default, with Jev disabled when no flag is
 * configured.
 */

/**
 * UpstreamUsageProvider selects provider fields needed for query support and
 * enabled-state checks.
 */
type UpstreamUsageProvider = Pick<Provider, 'type' | 'platform' | 'credentials' | 'extra'>

// @project-doc docs/interfaces/upstream_usage.md#frontend_lifecycle
/**
 * supportsUpstreamUsageQuery checks API-key eligibility and the Zhipu
 * provider-mode exception. It does not inspect the query enabled setting, so
 * callers use isUpstreamUsageQueryEnabled when deciding whether to expose a
 * query action.
 */
export function supportsUpstreamUsageQuery(provider: UpstreamUsageProvider): boolean {
  return (
    provider.type === 'apikey' &&
    !(provider.platform === 'zhipu' && provider.credentials?.provider_mode !== 'coding')
  )
}

/**
 * isUpstreamUsageQueryEnabled combines provider support with the saved query
 * switch. Missing or null flags use the platform default, which is disabled
 * for Jev; nonboolean configured values do not count as enabled.
 */
export function isUpstreamUsageQueryEnabled(provider: UpstreamUsageProvider): boolean {
  const config = provider.extra?.upstream_usage_query as Record<string, unknown> | undefined
  return (
    supportsUpstreamUsageQuery(provider) &&
    (config?.enabled ?? provider.platform !== 'jev') === true
  )
}
