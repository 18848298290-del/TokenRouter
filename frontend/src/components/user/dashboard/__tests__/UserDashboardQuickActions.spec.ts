import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick, ref } from 'vue'
import { useAppStore } from '@/stores/app'
import type { PublicSettings } from '@/api/settings.types'
import UserDashboardQuickActions from '../UserDashboardQuickActions.vue'

// UserDashboardQuickActions.spec.ts mounts dashboard shortcuts with controlled feature
// availability. It checks visible destinations and navigation while the app store and batch-
// image access composable provide deterministic account permissions for each scenario.

// Router and access-refresh spies capture shortcut navigation and mount effects while the
// app store controls payment-entry visibility in each scenario.
const routerPush = vi.fn()
const refreshBatchImageAccess = vi.fn()

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/composables/useBatchImageAccess', () => ({
  useBatchImageAccess: () => ({
    canUseBatchImage: ref(false),
    refreshBatchImageAccess,
  }),
}))

describe('UserDashboardQuickActions', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // Public settings initially omit payment availability and later enable it. The test
  // protects conditional purchase visibility and verifies that its action navigates to the
  // purchase page.
  it('仅在支付系统启用时展示购买入口并跳转到购买页', async () => {
    const appStore = useAppStore()
    const wrapper = mount(UserDashboardQuickActions)

    expect(wrapper.find('[data-testid="purchase-quick-action"]').exists()).toBe(false)

    appStore.cachedPublicSettings = {
      payment_enabled: true,
    } as PublicSettings
    await nextTick()

    const purchaseAction = wrapper.find('[data-testid="purchase-quick-action"]')
    expect(purchaseAction.exists()).toBe(true)

    await purchaseAction.trigger('click')

    expect(routerPush).toHaveBeenCalledWith('/purchase')
    wrapper.unmount()
  })
})
