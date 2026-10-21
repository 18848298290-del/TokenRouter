import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import EmailOAuthButtons from '@/components/auth/EmailOAuthButtons.vue'

// EmailOAuthButtons.spec.ts checks email-provider request emission and single
// versus multiple provider presentation. Router and translation fixtures isolate
// callback metadata, letting assertions inspect referral storage and emitted start
// parameters without navigating to a provider.

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
    // t resolves the translation keys needed by this test fixture. Unhandled keys
    // remain visible as their identifiers so assertions can inspect copy selection
    // without loading locale catalogs.
    t: (key: string, params?: Record<string, string>) => {
      if (key === 'auth.emailOAuth.signIn') {
        return `使用 ${params?.providerName ?? ''} 登录`
      }
      return key
    },
  }),
}))

describe('EmailOAuthButtons', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case. Clear
  // stored authentication or registration values before seeding the next
  // continuation.
  beforeEach(() => {
    routeState.query = { redirect: '/billing?plan=pro', aff: 'REF123' }
    window.localStorage.clear()
    window.sessionStorage.clear()
  })

  // The GitHub button receives a redirect and referral code. Its event must retain
  // those parameters and session storage must remember both attribution and
  // provider identity.
  it('emits the GitHub OAuth request with redirect and affiliate parameters', async () => {
    const wrapper = mount(EmailOAuthButtons, {
      props: {
        githubEnabled: true,
        googleEnabled: false,
        promoCode: ' PROMO123 ',
      },
      global: {
        stubs: {
          GitHubMark: true,
          GoogleMark: true,
        },
      },
    })

    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('start')).toEqual([
      [
        {
          provider: 'github',
          params: { redirect: '/billing?plan=pro', aff_code: 'REF123', promo_code: 'PROMO123' },
        },
      ],
    ])
    expect(window.sessionStorage.getItem('oauth_aff_code')).toBe('REF123')
    expect(window.sessionStorage.getItem('email_oauth_pending_provider')).toBe('github')
  })

  // The Google button is clicked with a configured destination. The emitted
  // request must identify Google while the browser location remains unchanged for
  // the parent gate.
  it('emits the Google provider without navigating directly', async () => {
    const originalHref = window.location.href
    const wrapper = mount(EmailOAuthButtons, {
      props: {
        githubEnabled: false,
        googleEnabled: true,
        promoCode: 'PROMO456',
      },
      global: {
        stubs: {
          GitHubMark: true,
          GoogleMark: true,
        },
      },
    })

    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('start')?.[0]?.[0]).toEqual({
      provider: 'google',
      params: { redirect: '/billing?plan=pro', aff_code: 'REF123', promo_code: 'PROMO456' },
    })
    expect(window.location.href).toBe(originalHref)
  })

  // Only GitHub is enabled in the provider list. The button must use descriptive
  // sign-in copy and the grid must omit the two-column layout class.
  it('uses a full-width descriptive button when only GitHub is enabled', () => {
    const wrapper = mount(EmailOAuthButtons, {
      props: {
        githubEnabled: true,
        googleEnabled: false,
      },
      global: {
        stubs: {
          GitHubMark: true,
          GoogleMark: true,
        },
      },
    })

    expect(wrapper.find('.grid').classes()).not.toContain('sm:grid-cols-2')
    expect(wrapper.get('button').text()).toContain('使用 GitHub 登录')
  })

  // GitHub and Google are both enabled for sign-in. The test checks two provider
  // labels and the responsive two-column class on their shared grid.
  it('uses compact labels and two columns when GitHub and Google are both enabled', () => {
    const wrapper = mount(EmailOAuthButtons, {
      props: {
        githubEnabled: true,
        googleEnabled: true,
      },
      global: {
        stubs: {
          GitHubMark: true,
          GoogleMark: true,
        },
      },
    })

    expect(wrapper.find('.grid').classes()).toContain('sm:grid-cols-2')
    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(2)
    expect(buttons[0].text()).toContain('GitHub')
    expect(buttons[1].text()).toContain('Google')
  })
})
