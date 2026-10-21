import { afterEach, describe, expect, it } from 'vitest'

import {
  DEFAULT_TABLE_PAGE_SIZE,
  DEFAULT_TABLE_PAGE_SIZE_OPTIONS,
  getConfiguredTableDefaultPageSize,
  getConfiguredTablePageSizeOptions,
  normalizeTablePageSize,
} from '@/utils/tablePreferences'

/**
 * tablePreferences.spec.ts checks injected table pagination defaults and
 * selectable options independently. Fixtures verify invalid-option removal,
 * upward selection mapping, maximum-option clamping, and built-in fallback
 * lists so valid configured defaults are not silently rewritten to match the
 * option list.
 */

describe('tablePreferences', () => {
  afterEach(() => {
    delete window.__APP_CONFIG__
  })

  /**
   * No application configuration is injected into the browser window. Default
   * size and selectable options must use the built-in values exposed by the
   * pagination preference module.
   */
  it('returns built-in defaults when app config is missing', () => {
    expect(getConfiguredTableDefaultPageSize()).toBe(DEFAULT_TABLE_PAGE_SIZE)
    expect(getConfiguredTablePageSizeOptions()).toEqual(DEFAULT_TABLE_PAGE_SIZE_OPTIONS)
  })

  /**
   * Injected configuration supplies valid default size and selectable options.
   * Readers must return those configured values rather than replacing them with
   * the built-in pagination preferences.
   */
  it('uses configured defaults when app config is valid', () => {
    window.__APP_CONFIG__ = {
      table_default_page_size: 50,
      table_page_size_options: [20, 50, 100],
    } as any

    expect(getConfiguredTableDefaultPageSize()).toBe(50)
    expect(getConfiguredTablePageSizeOptions()).toEqual([20, 50, 100])
  })

  /**
   * A valid configured default exceeds every selectable option. Reading the
   * default must preserve it, while selection normalization clamps to the
   * largest option and rounds intermediate values upward.
   */
  it('allows default page size outside selectable options', () => {
    window.__APP_CONFIG__ = {
      table_default_page_size: 1000,
      table_page_size_options: [20, 50, 100],
    } as any

    expect(getConfiguredTableDefaultPageSize()).toBe(1000)
    expect(getConfiguredTablePageSizeOptions()).toEqual([20, 50, 100])
    expect(normalizeTablePageSize(1000)).toBe(100)
    expect(normalizeTablePageSize(35)).toBe(50)
  })

  /**
   * Configured options contain duplicates and out-of-range numbers beside a
   * valid off-list default. Option cleanup must retain valid sorted choices
   * while selection normalization rounds the default into them.
   */
  it('normalizes invalid options without rewriting the configured default itself', () => {
    window.__APP_CONFIG__ = {
      table_default_page_size: 35,
      table_page_size_options: [1001, 50, 10, 10, 2, 0],
    } as any

    expect(getConfiguredTableDefaultPageSize()).toBe(35)
    expect(getConfiguredTablePageSizeOptions()).toEqual([10, 50])
    expect(normalizeTablePageSize(undefined)).toBe(50)
  })

  /**
   * Selection requests include exact, intermediate, oversized, and missing
   * values. Each must map to the first sufficient option, the largest option, or
   * the configured default path.
   */
  it('normalizes page size against configured options by rounding up', () => {
    window.__APP_CONFIG__ = {
      table_default_page_size: 20,
      table_page_size_options: [20, 50, 1000],
    } as any

    expect(normalizeTablePageSize(20)).toBe(20)
    expect(normalizeTablePageSize(30)).toBe(50)
    expect(normalizeTablePageSize(100)).toBe(1000)
    expect(normalizeTablePageSize(1500)).toBe(1000)
    expect(normalizeTablePageSize(undefined)).toBe(20)
  })

  /**
   * Configuration supplies a valid large default without an option list. The
   * option reader must retain the built-in selectable values independently of
   * that configured default size.
   */
  it('keeps built-in selectable defaults at 10, 20, 50, 100', () => {
    window.__APP_CONFIG__ = {
      table_default_page_size: 1000,
    } as any

    expect(getConfiguredTablePageSizeOptions()).toEqual([10, 20, 50, 100])
  })
})
