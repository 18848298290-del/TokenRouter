import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ProviderActionMenu from '../ProviderActionMenu.vue'
import type { Provider } from '@/api/admin/providers.types'

// ProviderActionMenu.spark_shadow.spec.ts checks provider action menu
// eligibility and emitted row identities. Teleported controls distinguish
// parent and shadow accounts, token-based credentials, and Qoder refresh
// availability while action assertions verify the provider attached to menu
// events.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

// makeProvider builds an OpenAI OAuth parent account with no limit timestamps.
// Overrides are applied last so menu tests can replace platform, credential
// type, parent identity, and credential availability.
function makeProvider(overrides: Partial<Provider>): Provider {
  return {
    id: 1,
    name: 'test-provider',
    platform: 'openai',
    type: 'oauth',
    proxy_id: null,
    concurrency: 3,
    priority: 50,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides,
  }
}

// position gives the teleported menu stable pixel coordinates for mounting,
// while assertions focus on action eligibility and event payloads rather than
// placement.
const position = { top: 100, left: 100 }

// getBodyText reads text from document.body, including the teleported action
// menu. Mount the menu first and unmount it after inspection so other mounted
// menus do not contaminate the observation.
const getBodyText = () => document.body.textContent ?? ''
// getBodyButtons returns document-body buttons in their rendered DOM order.
// The action menu uses Teleport, so callers inspect this collection after
// mounting and release their wrapper when assertions finish.
const getBodyButtons = () => Array.from(document.body.querySelectorAll('button'))

describe('ProviderActionMenu — spark shadow 按钮可见性', () => {
  // An API Key provider opens the action menu. Delete is the final button, and
  // clicking it emits the current provider followed by one close event.
  it('删除位于菜单末尾，传递当前提供商并关闭菜单', async () => {
    const provider = makeProvider({ type: 'apikey' })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    const deleteButton = getBodyButtons().at(-1)!
    expect(deleteButton.textContent).toContain('common.delete')
    deleteButton.click()
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('delete')?.[0]).toEqual([provider])
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  // A Gemini OAuth provider opens advanced scheduler scoring from its menu.
  // The emitted score event carries that provider's ID and name to the page.
  it('点击高级调度评分入口会携带提供商触发事件', async () => {
    const provider = makeProvider({ platform: 'gemini', type: 'oauth', parent_provider_id: null })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })

    const scoreButton = getBodyButtons().find((button) =>
      button.textContent?.includes('admin.providers.advancedSchedulerScore.action'),
    )
    expect(scoreButton).toBeDefined()

    scoreButton!.click()
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('advanced-scheduler-score')?.[0]?.[0]).toMatchObject({
      id: provider.id,
      name: provider.name,
    })
    wrapper.unmount()
  })

  // An Anthropic API Key provider has no parent account. Its open menu
  // includes the duplicate action, allowing independent key credentials to be
  // copied through page wiring.
  it('普通提供商显示「复制提供商」按钮', () => {
    const provider = makeProvider({
      platform: 'anthropic',
      type: 'apikey',
      parent_provider_id: null,
    })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    expect(getBodyText()).toContain('admin.providers.duplicateProvider')
    wrapper.unmount()
  })

  // An OpenAI shadow account opens its action menu. The duplicate action is
  // hidden because this row represents credentials inherited from its parent
  // provider.
  it('影子提供商隐藏「复制提供商」按钮', () => {
    const provider = makeProvider({ platform: 'openai', type: 'oauth', parent_provider_id: 42 })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    expect(getBodyText()).not.toContain('admin.providers.duplicateProvider')
    wrapper.unmount()
  })

  // An OpenAI parent account uses OAuth or setup-token credentials. The
  // duplicate action is absent for both types, preventing the menu from
  // offering copies of rotating token accounts.
  it.each(['oauth', 'setup-token'] as const)(
    '%s 提供商隐藏「复制提供商」按钮，避免共享可轮换令牌',
    (type) => {
      const provider = makeProvider({ platform: 'openai', type, parent_provider_id: null })
      const wrapper = mount(ProviderActionMenu, {
        props: { show: true, provider, position },
        attachTo: document.body,
      })
      expect(getBodyText()).not.toContain('admin.providers.duplicateProvider')
      wrapper.unmount()
    },
  )

  // An eligible API Key provider activates the duplicate menu item. The
  // emitted duplicate event carries the same provider ID and name for
  // page-level handling.
  it('点击「复制提供商」触发 duplicate 事件并携带 provider', async () => {
    const provider = makeProvider({
      platform: 'anthropic',
      type: 'apikey',
      parent_provider_id: null,
    })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })

    const duplicateBtn = getBodyButtons().find((b) =>
      b.textContent?.includes('admin.providers.duplicateProvider'),
    )
    expect(duplicateBtn).toBeDefined()

    duplicateBtn!.click()
    await wrapper.vm.$nextTick()

    const emitted = wrapper.emitted('duplicate')
    expect(emitted).toBeTruthy()
    expect(emitted![0][0]).toMatchObject({ id: provider.id, name: provider.name })
    wrapper.unmount()
  })

  // An OpenAI OAuth provider has no parent identifier. Its menu exposes the
  // Spark shadow creation action for the page to create a linked child
  // account.
  it('OpenAI OAuth 母提供商（无 parent_provider_id）显示「创建 spark 影子」按钮', () => {
    const provider = makeProvider({ platform: 'openai', type: 'oauth', parent_provider_id: null })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    expect(getBodyText()).toContain('admin.providers.createSparkShadow')
    wrapper.unmount()
  })

  // An OpenAI OAuth row already refers to a parent provider. Its menu hides
  // Spark shadow creation so another shadow is not offered from a shadow row.
  it('影子提供商（parent_provider_id 非 null）隐藏「创建 spark 影子」按钮', () => {
    const provider = makeProvider({ platform: 'openai', type: 'oauth', parent_provider_id: 42 })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    expect(getBodyText()).not.toContain('admin.providers.createSparkShadow')
    wrapper.unmount()
  })

  // An Antigravity OAuth provider has no parent identifier. Spark shadow
  // creation remains absent because the operation is specific to OpenAI parent
  // accounts in this menu.
  it('非 OpenAI 提供商隐藏「创建 spark 影子」按钮', () => {
    const provider = makeProvider({
      platform: 'antigravity',
      type: 'oauth',
      parent_provider_id: null,
    })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    expect(getBodyText()).not.toContain('admin.providers.createSparkShadow')
    wrapper.unmount()
  })

  // A shadow account opens its menu with inherited credentials.
  // Reauthorization, token refresh, and privacy actions remain hidden instead
  // of appearing as editable child-account operations.
  it('影子提供商隐藏凭据/隐私类操作(重授权/刷新token/隐私)— 外审 G4', () => {
    const provider = makeProvider({ platform: 'openai', type: 'oauth', parent_provider_id: 42 })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    const body = getBodyText()
    expect(body).not.toContain('admin.providers.reAuthorize')
    expect(body).not.toContain('admin.providers.refreshToken')
    expect(body).not.toContain('admin.providers.setPrivacy')
    wrapper.unmount()
  })

  // An OpenAI OAuth parent opens its menu without a parent identifier.
  // Reauthorization and privacy actions remain visible for the account that
  // owns its credentials.
  it('普通 OpenAI OAuth 母提供商仍显示凭据/隐私类操作', () => {
    const provider = makeProvider({ platform: 'openai', type: 'oauth', parent_provider_id: null })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    const body = getBodyText()
    expect(body).toContain('admin.providers.reAuthorize')
    expect(body).toContain('admin.providers.setPrivacy')
    wrapper.unmount()
  })

  // A Qoder COSY provider reports an available refresh token. Its menu offers
  // token refresh while keeping the reauthorization action absent for this
  // account type.
  it('Qoder COSY 提供商显示刷新 token 但不显示重授权入口', () => {
    const provider = makeProvider({
      platform: 'qoder',
      type: 'cosy',
      parent_provider_id: null,
      credentials_status: { has_refresh_token: true },
    })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    const body = getBodyText()
    expect(body).toContain('admin.providers.refreshToken')
    expect(body).not.toContain('admin.providers.reAuthorize')
    wrapper.unmount()
  })

  // A Qoder COSY provider reports a PAT without a refresh token. Its menu
  // omits token refresh because the stored credential status cannot support
  // that operation.
  it('无 refresh_token 的 Qoder COSY 提供商隐藏刷新 token 入口', () => {
    const provider = makeProvider({
      platform: 'qoder',
      type: 'cosy',
      parent_provider_id: null,
      credentials_status: { has_pat: true },
    })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })
    expect(getBodyText()).not.toContain('admin.providers.refreshToken')
    wrapper.unmount()
  })

  // An eligible OpenAI parent activates Spark shadow creation from the
  // teleported menu. The emitted event contains the parent ID and OpenAI
  // platform for the page handler.
  it('点击按钮触发 create-spark-shadow 事件并携带 provider', async () => {
    const provider = makeProvider({ platform: 'openai', type: 'oauth', parent_provider_id: null })
    const wrapper = mount(ProviderActionMenu, {
      props: { show: true, provider, position },
      attachTo: document.body,
    })

    // The action lives under document.body after Teleport, so querying wrapper
    // descendants would miss the control used by the administrator.
    const sparkBtn = getBodyButtons().find((b) =>
      b.textContent?.includes('admin.providers.createSparkShadow'),
    )
    expect(sparkBtn).toBeDefined()

    sparkBtn!.click()
    await wrapper.vm.$nextTick()

    const emitted = wrapper.emitted('create-spark-shadow')
    expect(emitted).toBeTruthy()
    expect(emitted![0][0]).toMatchObject({ id: provider.id, platform: 'openai' })

    wrapper.unmount()
  })
})
