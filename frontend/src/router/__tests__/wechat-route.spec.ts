import { beforeEach, describe, expect, it } from 'vitest'
import { getRouter, resetRouterHarness, runGuard } from './routerHarness'

// wechat-route.spec.ts checks the public registration of WeChat identity and
// payment callback destinations. Both callbacks need to resolve before browser
// authentication succeeds, and their names and titles identify separate pages
// during navigation and payment completion.

beforeEach(resetRouterHarness)

describe('router WeChat OAuth routes', () => {
  // WeChat redirects unauthenticated browsers into separate identity and
  // payment callback pages. Their registered names, paths, and titles remain
  // public and the installed authentication guard permits access.
  it.each([
    ['WeChatOAuthCallback', '/auth/wechat/callback', 'WeChat OAuth Callback'],
    ['WeChatPaymentOAuthCallback', '/auth/wechat/payment/callback', 'WeChat Payment Callback'],
  ])('registers %s as a public callback', async (name, path, title) => {
    const route = getRouter()
      .getRoutes()
      .find((record) => record.name === name)
    expect(route?.path).toBe(path)
    expect(route?.meta.requiresAuth).toBe(false)
    expect(route?.meta.title).toBe(title)
    const { navigation, next } = runGuard(path)
    await navigation
    expect(next).toHaveBeenCalledExactlyOnceWith()
  })
})
