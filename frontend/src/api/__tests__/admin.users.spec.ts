import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { post } provide the controlled request fixtures used by admin.users tests to
 * inspect their observable results.
 */
const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    post,
  },
}))

import {
  batchUpdateLimits,
  bindUserAuthIdentity,
  type AdminBindAuthIdentityRequest,
  type AdminBoundAuthIdentity,
  type BatchUpdateUserLimitsRequest,
  type BatchUpdateUserLimitsResponse,
} from '@/api/admin/users'

/**
 * admin.users.spec.ts checks administrator identity binding and batch limit
 * updates. Request mocks protect the transmitted nested identity and limit
 * fields, while compile-time assertions record the expected request and response
 * type shapes alongside runtime tests.
 */

/**
 * Assert rejects a compile-time test when its argument is not the literal true
 * type.
 */
type Assert<T extends true> = T

/**
 * IsExact compares assignability in both directions for the identity-binding
 * type assertions.
 */
type IsExact<T, U> =
  (<G>() => G extends T ? 1 : 2) extends <G>() => G extends U ? 1 : 2
    ? (<G>() => G extends U ? 1 : 2) extends <G>() => G extends T ? 1 : 2
      ? true
      : false
    : false

/**
 * ExpectedAdminBindAuthIdentityRequest defines the expected submitted provider
 * and channel identity shape for type assertions.
 */
type ExpectedAdminBindAuthIdentityRequest = {
  provider_type: string // Provider credential type.
  provider_key: string // Configured identity-provider key.
  provider_subject: string // Provider-scoped subject identifier.
  issuer?: string // Authentication issuer identity.
  metadata?: Record<string, unknown> // Additional structured metadata.
  channel?: {
    channel: string // Authentication channel details.
    channel_app_id: string // Channel application identifier.
    channel_subject: string // Channel-scoped subject identifier.
    metadata?: Record<string, unknown> // Additional structured metadata.
  } // Authentication channel details.
}

/**
 * ExpectedAdminBoundAuthIdentity defines the expected returned identity and
 * optional channel shape for type assertions.
 */
type ExpectedAdminBoundAuthIdentity = {
  user_id: number // Owning user identifier.
  provider_type: string // Provider credential type.
  provider_key: string // Configured identity-provider key.
  provider_subject: string // Provider-scoped subject identifier.
  verified_at?: string | null // Recorded verified at.
  issuer?: string | null // Authentication issuer identity.
  metadata: Record<string, unknown> | null // Additional structured metadata.
  created_at: string // Record creation timestamp.
  updated_at: string // Last modification timestamp.
  channel?: {
    channel: string // Authentication channel details.
    channel_app_id: string // Channel application identifier.
    channel_subject: string // Channel-scoped subject identifier.
    metadata: Record<string, unknown> | null // Additional structured metadata.
    created_at: string // Record creation timestamp.
    updated_at: string // Last modification timestamp.
  } | null // Authentication channel details.
}

/**
 * requestContractExact provides the controlled compile-time type assertions used
 * by admin.users tests to inspect their observable results.
 */
const requestContractExact: Assert<
  IsExact<AdminBindAuthIdentityRequest, ExpectedAdminBindAuthIdentityRequest>
> = true
/**
 * responseContractExact provides the controlled compile-time type assertions
 * used by admin.users tests to inspect their observable results.
 */
const responseContractExact: Assert<
  IsExact<AdminBoundAuthIdentity, ExpectedAdminBoundAuthIdentity>
> = true
/**
 * batchRequestContractExact provides the controlled compile-time type assertions
 * used by admin.users tests to inspect their observable results.
 */
const batchRequestContractExact: Assert<
  IsExact<
    BatchUpdateUserLimitsRequest,
    {
      user_ids: number[] // Recorded user ids.
      all?: boolean // All-users selection flag.
      concurrency?: number // Concurrent request limit.
      rpm_limit?: number // Configured requests-per-minute limit.
    }
  >
> = true
/**
 * batchResponseContractExact provides the controlled compile-time type
 * assertions used by admin.users tests to inspect their observable results.
 */
const batchResponseContractExact: Assert<
  IsExact<
    BatchUpdateUserLimitsResponse,
    {
      affected: number // Affected record count.
    }
  >
> = true

describe('admin users api auth identity binding', () => {
  beforeEach(() => {
    post.mockReset()
  })

  /**
   * An administrator binds a provider identity with nested channel metadata. The
   * wrapper sends the supplied structure to the selected user and returns the
   * server's identity response.
   */
  it('posts the backend-compatible auth identity bind payload and returns the backend response shape', async () => {
    const payload: AdminBindAuthIdentityRequest = {
      provider_type: 'wechat',
      provider_key: 'wechat-main',
      provider_subject: 'union-123',
      metadata: { source: 'admin-repair' },
      channel: {
        channel: 'open',
        channel_app_id: 'wx-open',
        channel_subject: 'openid-123',
        metadata: { scene: 'migration' },
      },
    }

    const response: AdminBoundAuthIdentity = {
      user_id: 9,
      provider_type: 'wechat',
      provider_key: 'wechat-main',
      provider_subject: 'union-123',
      verified_at: '2026-04-22T00:00:00Z',
      issuer: null,
      metadata: { source: 'admin-repair' },
      created_at: '2026-04-22T00:00:00Z',
      updated_at: '2026-04-22T00:00:00Z',
      channel: {
        channel: 'open',
        channel_app_id: 'wx-open',
        channel_subject: 'openid-123',
        metadata: { scene: 'migration' },
        created_at: '2026-04-22T00:00:00Z',
        updated_at: '2026-04-22T00:00:00Z',
      },
    }
    post.mockResolvedValue({ data: response })

    const result = await bindUserAuthIdentity(9, payload)

    expect(post).toHaveBeenCalledWith('/admin/users/9/auth-identities', payload)
    expect(result).toEqual(response)
  })

  /**
   * Compile-time assignments compare identity-binding types with expected
   * request and response shapes. Runtime assertions retain those assignments in
   * the test while the TypeScript checker owns structural validation.
   */
  it('keeps bind auth identity request and response types aligned with the backend contract', () => {
    expect(requestContractExact).toBe(true)
    expect(responseContractExact).toBe(true)
  })

  /**
   * A batch limit request supplies an explicit zero RPM limit and selected
   * users. One POST carries exactly those fields, and the returned affected
   * count remains available.
   */
  it('posts batch limit updates once with only the supplied limit fields', async () => {
    const request: BatchUpdateUserLimitsRequest = {
      user_ids: [4, 7],
      all: false,
      rpm_limit: 0,
    }
    post.mockResolvedValue({ data: { affected: 2 } satisfies BatchUpdateUserLimitsResponse })

    const result = await batchUpdateLimits(request)

    expect(post).toHaveBeenCalledWith('/admin/users/batch-limits', request)
    expect(result).toEqual({ affected: 2 })
    expect(batchRequestContractExact).toBe(true)
    expect(batchResponseContractExact).toBe(true)
  })
})
