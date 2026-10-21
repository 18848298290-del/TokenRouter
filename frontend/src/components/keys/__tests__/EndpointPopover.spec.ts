import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

// The clipboard mock reports success while localized fixture messages let assertions
// distinguish the normal hint from copied feedback.
const copyToClipboard = vi.fn().mockResolvedValue(true)

const messages: Record<string, string> = {
  'keys.endpoints.title': 'API 端点',
  'keys.endpoints.default': '默认',
  'keys.endpoints.copied': '已复制',
  'keys.endpoints.copiedHint': '已复制到剪贴板',
  'keys.endpoints.clickToCopy': '点击可复制此端点',
  'keys.endpoints.speedTest': '测速',
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => messages[key] ?? key,
  }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard,
  }),
}))

import EndpointPopover from '../EndpointPopover.vue'

// EndpointPopover.spec.ts mounts the endpoint popover with deterministic translations and
// clipboard feedback. The cases inspect where endpoint descriptions appear and how a
// successful URL copy changes the visible hint and accessible copy action.

describe('EndpointPopover', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  // A configured alternate endpoint includes descriptive text and a copy hint. The test
  // protects their visible placement and rejects the obsolete title-only tooltip
  // presentation.
  it('将说明提示渲染到 URL 上方而不是旧的 title 图标上', () => {
    const wrapper = mount(EndpointPopover, {
      props: {
        apiBaseUrl: 'https://default.example.com/v1',
        customEndpoints: [
          {
            name: '备用线路',
            endpoint: 'https://backup.example.com/v1',
            description: '自定义说明',
          },
        ],
      },
    })

    expect(wrapper.text()).toContain('自定义说明')
    expect(wrapper.text()).toContain('点击可复制此端点')
    expect(wrapper.find('[role="button"]').attributes('title')).toBeUndefined()
    expect(wrapper.find('[title="自定义说明"]').exists()).toBe(false)
  })

  // A successful clipboard operation follows a URL click in the default endpoint row. The
  // test checks the copied payload and the resulting visible and accessible confirmation.
  it('点击 URL 后会复制并切换为已复制提示', async () => {
    const wrapper = mount(EndpointPopover, {
      props: {
        apiBaseUrl: 'https://default.example.com/v1',
        customEndpoints: [],
      },
    })

    await wrapper.find('[role="button"]').trigger('click')
    await flushPromises()

    expect(copyToClipboard).toHaveBeenCalledWith('https://default.example.com/v1', '已复制')
    expect(wrapper.text()).toContain('已复制到剪贴板')
    expect(wrapper.find('button[aria-label="已复制到剪贴板"]').exists()).toBe(true)
  })
})
