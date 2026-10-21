import { describe, expect, it } from 'vitest'

import {
  ADMIN_UI_REQUEST_HEADER,
  USER_UI_REQUEST_HEADER,
  isUserTimingAPIPath,
  shouldMarkAdminUIRequest,
  shouldMarkUserUIRequest,
} from '@/api/adminUIRequest'

/**
 * adminUIRequest.spec.ts checks the stable header names and route classification
 * for browser timing markers. Parameterized cases cover absolute and relative
 * URLs, administrator page context, shared user routes and excluded public
 * payment or webhook paths.
 */

describe('Admin UI request marker', () => {
  /**
   * The administrator marker constant is inspected directly by its consumers'
   * test. Its exact header spelling remains stable for the server that
   * recognizes browser timing requests.
   */
  it('uses the stable request header name', () => {
    expect(ADMIN_UI_REQUEST_HEADER).toBe('X-Admin-UI-Request')
  })

  /**
   * Administrator targets are expressed as root, prefixed and absolute URLs
   * before page navigation. Each path independently qualifies for the
   * administrator marker from the login page.
   */
  it.each([
    '/admin',
    '/admin/users',
    '/api/v1/admin',
    '/api/v1/admin/providers?status=active',
    'https://api.example.test/api/v1/admin/dashboard',
  ])('marks Admin API request %s before page navigation', (requestURL) => {
    expect(shouldMarkAdminUIRequest(requestURL, '/login')).toBe(true)
  })

  /**
   * Shared user endpoints are requested while an administrator page is active.
   * Each case receives the administrator marker from page context regardless of
   * its target family.
   */
  it.each(['/keys', '/groups/available', '/auth/me', '/announcements'])(
    'marks shared request %s while an Admin page is active',
    (requestURL) => {
      expect(shouldMarkAdminUIRequest(requestURL, '/admin/dashboard')).toBe(true)
    },
  )

  /**
   * Ordinary pages and similarly prefixed route names are classified as negative
   * cases. The administrator marker stays absent unless a complete administrator
   * path segment matches.
   */
  it.each([
    ['/keys', '/dashboard'],
    ['/api/v1/administer', '/dashboard'],
    ['/keys', '/administrator'],
    ['', '/'],
  ])('does not mark request %s on page %s', (requestURL, pagePath) => {
    expect(shouldMarkAdminUIRequest(requestURL, pagePath)).toBe(false)
  })
})

describe('User UI request marker', () => {
  /**
   * The user marker constant is inspected directly by its consumers' test. Its
   * exact header spelling remains stable for the server that recognizes user
   * browser requests.
   */
  it('uses the stable request header name', () => {
    expect(USER_UI_REQUEST_HEADER).toBe('X-User-UI-Request')
  })

  /**
   * User timing endpoints are supplied in relative, prefixed and absolute forms.
   * Both public classification functions recognize each listed route as eligible
   * for the user marker.
   */
  it.each([
    '/auth/me',
    '/auth/revoke-all-sessions',
    '/auth/oauth/bind-token',
    '/user',
    '/user/profile',
    '/user/password',
    '/user/notify-email/send-code',
    '/user/totp/status',
    '/user/aff',
    '/user/platform-quotas',
    '/keys',
    '/keys/12',
    '/groups/available',
    '/groups/rates',
    '/channels/available',
    '/usage',
    '/usage/stats',
    '/usage/dashboard/snapshot-v2',
    '/announcements',
    '/announcements/3/read',
    '/redeem',
    '/redeem/history',
    '/subscriptions',
    '/subscriptions/active',
    '/channel-monitors',
    '/channel-monitors/9/status',
    '/payment/config',
    '/payment/plans',
    '/payment/orders',
    '/payment/orders/my',
    '/api/v1/auth/me',
    '/api/v1/keys?page=1',
    'https://api.example.test/api/v1/payment/orders/1',
  ])('marks user timing API %s', (requestURL) => {
    expect(shouldMarkUserUIRequest(requestURL)).toBe(true)
    expect(isUserTimingAPIPath(requestURL)).toBe(true)
  })

  /**
   * Login, public payment, webhook and unrelated paths are classified as
   * negative cases. Each request stays outside the user timing allowlist used to
   * attach the marker.
   */
  it.each([
    '/auth/login',
    '/settings/public',
    '/admin/users',
    '/groups',
    '/channels',
    '/payment/public/orders/verify',
    '/payment/webhook/stripe',
    '/api/v1/payment/public/orders/resolve',
    '',
  ])('does not mark non-user timing API %s', (requestURL) => {
    expect(shouldMarkUserUIRequest(requestURL)).toBe(false)
  })
})
