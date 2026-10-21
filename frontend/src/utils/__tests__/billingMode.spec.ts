import { describe, expect, it } from 'vitest'
import {
  BILLING_MODE_IMAGE,
  BILLING_MODE_TOKEN,
  BILLING_MODE_VIDEO,
  getDisplayBillingMode,
  isImageUsage,
} from '../billingMode'

/**
 * billingMode.spec.ts tests image-count inference against explicit token and
 * video billing modes. The fixtures exercise absent mode metadata and
 * conflicting image counts so display classification retains the intended unit
 * for each recorded row.
 */

describe('billingMode helpers', () => {
  /**
   * An explicitly video-billed row also records a positive image count. Its displayed
   * mode stays video and image inference remains false for the same row.
   */
  it('prefers explicit video mode over image_count', () => {
    expect(getDisplayBillingMode({ image_count: 1, billing_mode: BILLING_MODE_VIDEO })).toBe(
      BILLING_MODE_VIDEO,
    )
    expect(isImageUsage({ image_count: 1, billing_mode: BILLING_MODE_VIDEO })).toBe(false)
  })

  /**
   * A row records images while its billing mode is null. The display infers image
   * billing from that count so historical image records keep their unit.
   */
  it('infers image when image_count set and mode missing', () => {
    expect(getDisplayBillingMode({ image_count: 2, billing_mode: null })).toBe(BILLING_MODE_IMAGE)
  })

  /**
   * A token-billed row records an image count as well. The explicit token mode takes
   * precedence and remains the displayed billing mode for that record.
   */
  it('keeps token mode even with image_count', () => {
    expect(getDisplayBillingMode({ image_count: 1, billing_mode: BILLING_MODE_TOKEN })).toBe(
      BILLING_MODE_TOKEN,
    )
  })
})
