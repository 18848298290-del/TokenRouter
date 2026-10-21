import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LinuxDoOAuthSection from '@/components/auth/LinuxDoOAuthSection.vue'
import DingTalkOAuthSection from '@/components/auth/DingTalkOAuthSection.vue'
import OidcOAuthSection from '@/components/auth/OidcOAuthSection.vue'

// OAuthLoginSections.spec.ts checks the provider buttons shared by authentication
// pages. Stubbed routing supplies the intended destination and referral data,
// while emitted requests prove the parent retains control of captcha gating and
// navigation.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
})) // test-controlled route state fixture

vi.mock('vue-router', () => ({
  // useRoute supplies the callback query fixture read by the component.
  useRoute: () => routeState,
}))

vi.mock('vue-i18n', () => ({
  // useI18n supplies the translation fixture used by rendered authentication
  // labels.
  useI18n: () => ({
    // t returns translation keys directly for stable component text assertions.
    t: (key: string) => key,
  }),
}))

describe('OAuth login sections', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case. Clear
  // stored authentication or registration values before seeding the next
  // continuation.
  beforeEach(() => {
    routeState.query = { redirect: '/billing?plan=pro', aff: 'AFF123' }
    window.sessionStorage.clear()
  })

  // Each provider button receives the same destination and referral fixture.
  // Clicking it must emit the selected provider request, retain attribution, and
  // leave navigation to its parent.
  it.each([
    ['linuxdo', LinuxDoOAuthSection],
    ['dingtalk', DingTalkOAuthSection],
    ['oidc', OidcOAuthSection],
  ] as const)('emits a %s start request from the original button', async (provider, component) => {
    const originalHref = window.location.href
    const wrapper = mount(component, { props: { affCode: 'AFF456' } })

    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('start')?.[0]?.[0]).toEqual({
      provider,
      params: { redirect: '/billing?plan=pro' },
    })
    expect(window.sessionStorage.getItem('oauth_aff_code')).toBe('AFF456')
    expect(window.location.href).toBe(originalHref)
  })

  // The LinuxDo entry receives promotion text with surrounding whitespace. Its
  // emitted start request must contain the trimmed promotion code beside the
  // configured redirect destination.
  it('includes a trimmed promo code in the LinuxDo OAuth request', async () => {
    const wrapper = mount(LinuxDoOAuthSection, {
      props: {
        affCode: 'AFF456',
        promoCode: ' PROMO789 ',
      },
    })

    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('start')?.[0]?.[0]).toEqual({
      provider: 'linuxdo',
      params: {
        redirect: '/billing?plan=pro',
        promo_code: 'PROMO789',
      },
    })
  })
})
