import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { AdminUser } from '@/api/admin/users.types'
import type { ApiKey } from '@/api/keys.types'

// UserApiKeysModal.spec.ts checks API-key group presentation for composite and regular
// keys in the administrator modal. The API supplies fixed fixtures, and child badges
// expose names directly. Tests inspect prefix mappings and selector visibility through
// the real open-dialog loading watcher.

// apiMocks supplies controlled key rows, group choices, and group-update responses for
// the modal's normal open watcher and selector rendering.
const apiMocks = vi.hoisted(() => ({
  getUserApiKeys: vi.fn(), // User-key request spy.
  getAllGroups: vi.fn(), // Group-catalog request spy.
  updateApiKeyGroup: vi.fn(), // Key-group mutation spy.
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: {
      getUserApiKeys: apiMocks.getUserApiKeys, // User-key request spy.
    }, // User endpoint substitutes.
    groups: {
      getAll: apiMocks.getAllGroups, // Group-catalog request substitute.
    }, // Group endpoint substitutes.
    apiKeys: {
      updateApiKeyGroup: apiMocks.updateApiKeyGroup, // Key-group mutation spy.
    }, // Key endpoint substitutes.
  }, // Administrator API substitute.
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(), // Error notification spy.
    showSuccess: vi.fn(), // Success notification spy.
  }), // Application notification-store substitute.
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key, // Translation key passthrough.
    }), // Translation composable substitute.
  }
})

import UserApiKeysModal from '../UserApiKeysModal.vue'

// createApiKey builds an active ungrouped key fixture and applies caller overrides
// last. Tests supply composite mappings or a regular group through those overrides
// while reusing the required quota and timing fields.
const createApiKey = (overrides: Partial<ApiKey> = {}): ApiKey => ({
  id: 1, // Fixture record identifier.
  user_id: 99, // Owning user identifier.
  key: 'sk-test-key', // Fixture API credential.
  name: '测试 Key', // Fixture display name.
  group_id: null, // Assigned group identifier.
  status: 'active', // Key availability status.
  fast_mode_policy: 'follow_request', // Request speed-policy fixture.
  ip_whitelist: [], // Allowed client addresses.
  ip_blacklist: [], // Blocked client addresses.
  last_used_at: null, // Last request timestamp.
  last_used_ip: null, // Last client address.
  quota: 0, // Key quota limit.
  quota_used: 0, // Consumed key quota.
  expires_at: null, // Key expiry timestamp.
  created_at: '2026-08-05T00:00:00Z', // Record creation timestamp.
  updated_at: '2026-08-05T00:00:00Z', // Record update timestamp.
  current_concurrency: 0, // Observed concurrent requests.
  rate_limit_5h: 0, // Five-hour spending limit.
  rate_limit_1d: 0, // Daily spending limit.
  rate_limit_7d: 0, // Weekly spending limit.
  usage_5h: 0, // Five-hour spending total.
  usage_1d: 0, // Daily spending total.
  usage_7d: 0, // Weekly spending total.
  window_5h_start: null, // Five-hour window start.
  window_1d_start: null, // Daily window start.
  window_7d_start: null, // Weekly window start.
  reset_5h_at: null, // Five-hour reset timestamp.
  reset_1d_at: null, // Daily reset timestamp.
  reset_7d_at: null, // Weekly reset timestamp.
  ...overrides,
})

// user supplies the selected account identity used by the modal header and its user-key
// request when visibility changes.
const user = {
  id: 99, // Fixture record identifier.
  email: 'admin@example.com', // User email address.
  username: 'admin', // User display name.
} as AdminUser

// mountAndOpen mounts the modal hidden before opening it and awaiting API completion.
// This exercises the visibility watcher; lightweight child stubs expose group names
// while keeping selector markup available for assertions.
const mountAndOpen = async () => {
  const wrapper = mount(UserApiKeysModal, {
    props: {
      show: false, // Parent-controlled dialog visibility.
      user, // Selected user record.
    }, // Accepted component props.
    global: {
      stubs: {
        BaseDialog: {
          props: ['show'], // Accepted component props.
          template: '<div v-if="show"><slot /></div>', // Rendered stub markup.
        }, // Dialog rendering substitute.
        GroupBadge: {
          props: ['name'], // Accepted component props.
          template: '<span data-testid="group-badge">{{ name }}</span>', // Rendered stub markup.
        }, // Group badge substitute.
        GroupOptionItem: {
          props: ['name'], // Accepted component props.
          template: '<span>{{ name }}</span>', // Rendered stub markup.
        }, // Group option substitute.
        Teleport: true, // Inline teleport substitute.
      }, // Replaced child components.
    }, // Mount-wide component overrides.
  })

  await wrapper.setProps({
    show: true, // Parent-controlled dialog visibility.
  })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  apiMocks.getAllGroups.mockResolvedValue([])
  apiMocks.updateApiKeyGroup.mockResolvedValue({
    api_key: createApiKey(), // Updated key fixture.
    auto_granted_group_access: false, // Additional access-grant flag.
  })
})

describe('UserApiKeysModal', () => {
  // A composite key contains two prefix-to-group mappings in its API response. The
  // modal must display both prefixes and group names while hiding the regular group
  // selector.
  it('展示复合 Key 的全部前缀分组映射且不提供普通换组入口', async () => {
    apiMocks.getUserApiKeys.mockResolvedValue({
      items: [
        createApiKey({
          is_composite: true, // Prefix-routing key flag.
          composite_groups: [
            {
              group_id: 10, // Assigned group identifier.
              prefix: 'GPT', // Model routing prefix.
              group: {
                id: 10, // Fixture record identifier.
                name: 'OpenAI 主组', // Fixture display name.
                platform: 'openai', // Group provider platform.
              } as any, // Resolved group record.
            },
            {
              group_id: 20, // Assigned group identifier.
              prefix: 'Claude', // Model routing prefix.
              group: {
                id: 20, // Fixture record identifier.
                name: 'Anthropic 主组', // Fixture display name.
                platform: 'anthropic', // Group provider platform.
              } as any, // Resolved group record.
            },
          ], // Prefix-to-group mapping entries.
        }),
      ], // Returned response records.
      total: 1, // Matching record count.
      page: 1, // One-based result page.
      page_size: 20, // Rows per page.
      pages: 1, // Available page count.
    })

    const wrapper = await mountAndOpen()
    const mappings = wrapper.get('[data-testid="composite-group-mappings"]')

    expect(mappings.text()).toContain('GPT')
    expect(mappings.text()).toContain('OpenAI 主组')
    expect(mappings.text()).toContain('Claude')
    expect(mappings.text()).toContain('Anthropic 主组')
    expect(mappings.text()).not.toContain('admin.users.none')
    expect(wrapper.find('[data-testid="api-key-group-selector"]').exists()).toBe(false)
    expect(apiMocks.updateApiKeyGroup).not.toHaveBeenCalled()
  })

  // A regular key has one current group and receives that group in the catalog. The
  // modal must render its group badge and ordinary selector, with no composite-mapping
  // section.
  it('普通 Key 继续显示并使用原有换组入口', async () => {
    const currentGroup = {
      id: 10, // Fixture record identifier.
      name: 'OpenAI 主组', // Fixture display name.
      platform: 'openai', // Group provider platform.
    } as any
    apiMocks.getUserApiKeys.mockResolvedValue({
      items: [
        createApiKey({
          group_id: 10, // Assigned group identifier.
          group: currentGroup, // Resolved group record.
        }),
      ], // Returned response records.
      total: 1, // Matching record count.
      page: 1, // One-based result page.
      page_size: 20, // Rows per page.
      pages: 1, // Available page count.
    })
    apiMocks.getAllGroups.mockResolvedValue([currentGroup])

    const wrapper = await mountAndOpen()

    expect(wrapper.get('[data-testid="group-badge"]').text()).toBe('OpenAI 主组')
    expect(wrapper.get('[data-testid="api-key-group-selector"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="composite-group-mappings"]').exists()).toBe(false)
  })
})
