import { describe, expect, it } from 'vitest'

import {
  formatUsageServiceTier,
  getUsageServiceTierLabel,
  normalizeUsageServiceTier,
} from '@/utils/usageServiceTier'

/**
 * usageServiceTier.spec.ts tests normalization and labeling of recorded service
 * tiers. Alias, casing, whitespace, default, and custom-value fixtures check
 * which values receive translated labels and which retain their normalized
 * provider-supplied text for display.
 */

describe('usageServiceTier utils', () => {
  /**
   * Fast, default, and uppercase standard spellings enter normalization. Aliases and
   * whitespace resolve to priority or standard, keeping historical spellings in the
   * expected display categories.
   */
  it('normalizes fast/default aliases', () => {
    expect(normalizeUsageServiceTier('fast')).toBe('priority')
    expect(normalizeUsageServiceTier(' default ')).toBe('standard')
    expect(normalizeUsageServiceTier('STANDARD')).toBe('standard')
  })

  /**
   * The priority, flex, and ultrafast service tiers are already recognized.
   * Normalization retains each value so known provider categories keep their intended
   * display identity.
   */
  it('preserves supported tiers', () => {
    expect(normalizeUsageServiceTier('priority')).toBe('priority')
    expect(normalizeUsageServiceTier('flex')).toBe('flex')
    expect(normalizeUsageServiceTier('ultrafast')).toBe('ultrafast')
  })

  /**
   * Tier values are missing or empty when formatted for display. Both receive standard,
   * establishing the visible default used for records with no tier text.
   */
  it('formats empty values as standard', () => {
    expect(formatUsageServiceTier()).toBe('standard')
    expect(formatUsageServiceTier('')).toBe('standard')
  })

  /**
   * A custom nonempty tier is passed through normalization and display formatting. Its
   * spelling remains available so an additional provider category is not collapsed into
   * standard.
   */
  it('passes through unknown non-empty tiers for display fallback', () => {
    expect(normalizeUsageServiceTier('custom-tier')).toBe('custom-tier')
    expect(formatUsageServiceTier('custom-tier')).toBe('custom-tier')
  })

  /**
   * A translator fixture covers known tier labels while a custom tier remains unknown.
   * Label selection translates aliases and defaults, then returns custom normalized
   * text directly.
   */
  it('maps tiers to translated labels', () => {
    /**
     * translate resolves the service-tier label fixture or returns the unknown key. The
     * test supplies this local dictionary to distinguish translation of known
     * categories from direct custom-tier display.
     */
    const translate = (key: string) =>
      ({
        'usage.serviceTierPriority': 'Fast',
        'usage.serviceTierUltrafast': 'Ultrafast',
        'usage.serviceTierFlex': 'Flex',
        'usage.serviceTierStandard': 'Standard',
      })[key] ?? key

    expect(getUsageServiceTierLabel('fast', translate)).toBe('Fast')
    expect(getUsageServiceTierLabel('ultrafast', translate)).toBe('Ultrafast')
    expect(getUsageServiceTierLabel('flex', translate)).toBe('Flex')
    expect(getUsageServiceTierLabel(undefined, translate)).toBe('Standard')
    expect(getUsageServiceTierLabel('custom-tier', translate)).toBe('custom-tier')
  })
})
