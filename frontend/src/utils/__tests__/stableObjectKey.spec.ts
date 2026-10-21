import { describe, expect, it } from 'vitest'
import { createStableObjectKeyResolver } from '@/utils/stableObjectKey'

/**
 * stableObjectKey.spec.ts tests resolver-local object identity keys with custom
 * prefixes. Reusing one object returns its assigned key, distinct objects with
 * equal contents remain distinct, and separate resolvers retain their own
 * prefixed identities.
 */

describe('createStableObjectKeyResolver', () => {
  /**
   * A resolver is called twice with the same object and a custom prefix. It returns one
   * stable key with that prefix for both identity lookups.
   */
  it('对同一对象返回稳定 key', () => {
    const resolve = createStableObjectKeyResolver<{ value: string /* arbitrary fixture text */ }>(
      'rule',
    )
    const obj = { value: 'a' }

    const key1 = resolve(obj)
    const key2 = resolve(obj)

    expect(key1).toBe(key2)
    expect(key1.startsWith('rule-')).toBe(true)
  })

  /**
   * Two separate objects contain equal field values and use one resolver. They receive
   * different keys, protecting object identity as the lookup criterion instead of
   * structural equality.
   */
  it('不同对象返回不同 key', () => {
    const resolve = createStableObjectKeyResolver<{ value: string /* arbitrary fixture text */ }>(
      'rule',
    )

    const key1 = resolve({ value: 'a' })
    const key2 = resolve({ value: 'a' })

    expect(key1).not.toBe(key2)
  })

  /**
   * Two resolvers receive the same object with different prefixes. Each returns its own
   * prefixed key, confirming resolver-local caching even when the input identity is
   * shared.
   */
  it('不同 resolver 互不影响', () => {
    const resolveA = createStableObjectKeyResolver<{ id: number /* fixture object identifier */ }>(
      'a',
    )
    const resolveB = createStableObjectKeyResolver<{ id: number /* fixture object identifier */ }>(
      'b',
    )
    const obj = { id: 1 }

    const keyA = resolveA(obj)
    const keyB = resolveB(obj)

    expect(keyA).not.toBe(keyB)
    expect(keyA.startsWith('a-')).toBe(true)
    expect(keyB.startsWith('b-')).toBe(true)
  })
})
