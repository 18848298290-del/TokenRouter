/**
 * usageServiceTier.ts normalizes service-tier identifiers and chooses usage labels.
 * Fast aliases priority, default aliases standard, and missing display values use
 * standard. Unknown nonempty tiers remain lowercased text so records carrying
 * additional provider values remain visible.
 */

/**
 * normalizeUsageServiceTier trims and lowercases a tier, mapping fast to priority and
 * default to standard. Missing or blank values return null, and other nonempty tier
 * names pass through normalized.
 */
export function normalizeUsageServiceTier(serviceTier?: string | null): string | null {
  const value = serviceTier?.trim().toLowerCase()
  if (!value) return null
  /**
   * Normalize recognized historical aliases before labeling so fast and default records
   * use the same display categories as priority and standard.
   */
  if (value === 'fast') return 'priority'
  if (value === 'default' || value === 'standard') return 'standard'
  if (value === 'priority' || value === 'flex' || value === 'ultrafast') return value
  return value
}

/**
 * formatUsageServiceTier returns the normalized tier for display, defaulting missing or
 * blank input to standard. Unknown nonempty values retain their normalized spelling so
 * exports can preserve their supplied category.
 */
export function formatUsageServiceTier(serviceTier?: string | null): string {
  const normalized = normalizeUsageServiceTier(serviceTier)
  if (!normalized) return 'standard'
  return normalized
}

/**
 * getUsageServiceTierLabel translates the four known display tiers using the caller's
 * translator. Missing input resolves to standard before translation, while unknown
 * normalized tiers are returned as readable raw text.
 */
export function getUsageServiceTierLabel(
  serviceTier: string | null | undefined,
  translate: (key: string) => string,
): string {
  const tier = formatUsageServiceTier(serviceTier)
  if (tier === 'priority') return translate('usage.serviceTierPriority')
  if (tier === 'ultrafast') return translate('usage.serviceTierUltrafast')
  if (tier === 'flex') return translate('usage.serviceTierFlex')
  if (tier === 'standard') return translate('usage.serviceTierStandard')
  return tier
}
