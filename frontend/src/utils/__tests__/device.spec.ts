import { describe, expect, it } from 'vitest'
import { detectIOSDevice, detectMobileDevice } from '../device'

/**
 * device.spec.ts checks injected browser observations for mobile and iOS
 * classification. Fixtures cover client hints, user-agent tokens, touch
 * capabilities, and desktop-mode iPad behavior so classification can be tested
 * without relying on the machine running the suite.
 */

describe('detectMobileDevice', () => {
  /**
   * The navigator exposes an affirmative mobile client hint without a mobile
   * user-agent token. Mobile detection must accept that hint as sufficient
   * evidence for the interface classification.
   */
  it('prefers userAgentData.mobile when available', () => {
    expect(
      detectMobileDevice({
        navigator: {
          userAgent: 'Mozilla/5.0',
          userAgentData: { mobile: true },
        },
      }),
    ).toBe(true)
  })

  /**
   * The navigator contains an Android handheld user-agent and touch capability.
   * Mobile detection must recognize the mobile token without requiring
   * additional media-query evidence in this fixture.
   */
  it('recognizes handheld browsers from the mobile UA token', () => {
    expect(
      detectMobileDevice({
        navigator: {
          userAgent:
            'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/136.0 Mobile Safari/537.36',
          maxTouchPoints: 5,
        },
      }),
    ).toBe(true)
  })

  /**
   * An iPad presents a desktop-style Mac user-agent with multiple touch points.
   * Mobile detection must retain tablet classification despite the absence of an
   * iPad token.
   */
  it('recognizes iPadOS desktop mode via touch capability', () => {
    expect(
      detectMobileDevice({
        navigator: {
          userAgent:
            'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15',
          platform: 'MacIntel',
          maxTouchPoints: 5,
        },
      }),
    ).toBe(true)
  })

  /**
   * A generic user-agent supplies touch capability, coarse pointer, and no
   * hover. The combined input observations must enable mobile classification
   * without a recognized handheld token.
   */
  it('falls back to input capability detection for touch-first devices', () => {
    expect(
      detectMobileDevice({
        navigator: {
          userAgent: 'Mozilla/5.0',
          maxTouchPoints: 10,
        },
        matchMedia: (query) => ({
          matches: query === '(pointer: coarse)' || query === '(hover: none)',
        }),
      }),
    ).toBe(true)
  })

  /**
   * A desktop Mac has no touch points and unmatched input queries. Mobile
   * detection must return false instead of treating its platform name as tablet
   * evidence.
   */
  it('keeps desktop environments as non-mobile', () => {
    expect(
      detectMobileDevice({
        navigator: {
          userAgent:
            'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/136.0 Safari/537.36',
          platform: 'MacIntel',
          maxTouchPoints: 0,
        },
        matchMedia: () => ({ matches: false }),
      }),
    ).toBe(false)
  })
})

describe('detectIOSDevice', () => {
  /**
   * The user-agent identifies an iPhone and supplies touch capability. iOS
   * detection must recognize that platform directly from its token without
   * requiring the desktop-iPad fallback.
   */
  it('recognizes iPhone from the UA token', () => {
    expect(
      detectIOSDevice({
        navigator: {
          userAgent:
            'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 Version/17.5 Mobile/15E148 Safari/604.1',
          maxTouchPoints: 5,
        },
      }),
    ).toBe(true)
  })

  /**
   * A desktop-mode iPad reports MacIntel and several touch points. iOS detection
   * must recognize that combination even though the user-agent itself resembles
   * ordinary macOS.
   */
  it('recognizes iPadOS desktop mode via touch capability', () => {
    expect(
      detectIOSDevice({
        navigator: {
          userAgent:
            'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15',
          platform: 'MacIntel',
          maxTouchPoints: 5,
        },
      }),
    ).toBe(true)
  })

  /**
   * An Android handheld has touch points but no iOS platform signature. iOS
   * detection must return false rather than equating all mobile devices with
   * iOS.
   */
  it('keeps Android devices as non-iOS', () => {
    expect(
      detectIOSDevice({
        navigator: {
          userAgent:
            'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/136.0 Mobile Safari/537.36',
          maxTouchPoints: 5,
        },
      }),
    ).toBe(false)
  })

  /**
   * A desktop Mac reports MacIntel but no touch points. iOS detection must
   * reject it so the desktop-mode iPad fallback does not absorb ordinary Macs.
   */
  it('keeps desktop macOS without touch as non-iOS', () => {
    expect(
      detectIOSDevice({
        navigator: {
          userAgent:
            'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/136.0 Safari/537.36',
          platform: 'MacIntel',
          maxTouchPoints: 0,
        },
      }),
    ).toBe(false)
  })
})
