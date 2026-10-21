import { afterEach, describe, expect, it, vi } from 'vitest'
import { buildKeyFormPayload, type KeyRestrictionForm } from '../keyForm'

// keyForm.spec.ts verifies the restriction values emitted by API-key forms.
// Creation and editing use different expiry representations, while disabled
// controls must send the values that remove existing limits. Tests freeze the
// submission clock and exercise the actual shared model-mapping conversion.

// makeForm supplies disabled controls and empty inputs for a restriction scenario.
// Overrides select the fields under test; supplied mapping arrays stay shared so
// tests can verify that payload construction leaves the caller's data unchanged.
function makeForm(overrides: Partial<KeyRestrictionForm> = {}): KeyRestrictionForm {
  return {
    enable_ip_restriction: false,
    ip_whitelist: '',
    ip_blacklist: '',
    quota: null,
    enable_expiration: false,
    expiration_date: '',
    enable_rate_limit: false,
    rate_limit_5h: null,
    rate_limit_1d: null,
    rate_limit_7d: null,
    model_mapping_rows: [],
    ...overrides,
  }
}

afterEach(() => vi.useRealTimers())

describe('key form restriction payload', () => {
  // Disabled controls may retain previous field contents in the draft. Both
  // operations still send clearing arrays and zero spending windows to the API.
  it.each([false, true])('clears disabled restrictions when editing=%s', (editing) => {
    const result = buildKeyFormPayload(
      makeForm({
        ip_whitelist: '192.0.2.1',
        ip_blacklist: '198.51.100.1',
        rate_limit_5h: 12,
        rate_limit_1d: 24,
        rate_limit_7d: 48,
      }),
      editing,
    )
    expect(result.ipWhitelist).toEqual([])
    expect(result.ipBlacklist).toEqual([])
    expect(result.rateLimitData).toEqual({ rate_limit_5h: 0, rate_limit_1d: 0, rate_limit_7d: 0 })
    expect(result.quota).toBe(0)
  })

  // Enabled lists trim each line without changing order or deduplicating input.
  // Positive spending windows survive while empty and negative limits become zero.
  it('normalizes enabled addresses and numeric windows without mutating the draft', () => {
    const form = makeForm({
      enable_ip_restriction: true,
      ip_whitelist: ' 192.0.2.1 \n\n192.0.2.1\n 2001:db8::/32 ',
      ip_blacklist: ' 198.51.100.0/24\n',
      quota: 5.5,
      enable_rate_limit: true,
      rate_limit_5h: 1.25,
      rate_limit_1d: null,
      rate_limit_7d: -2,
      model_mapping_rows: [{ from: ' source ', to: ' target ' }],
    })
    const before = structuredClone(form)
    const result = buildKeyFormPayload(form, false)
    expect(result.ipWhitelist).toEqual(['192.0.2.1', '192.0.2.1', '2001:db8::/32'])
    expect(result.ipBlacklist).toEqual(['198.51.100.0/24'])
    expect(result.quota).toBe(5.5)
    expect(result.rateLimitData).toEqual({
      rate_limit_5h: 1.25,
      rate_limit_1d: 0,
      rate_limit_7d: 0,
    })
    expect(result.modelMapping).toEqual({ source: 'target' })
    expect(form).toEqual(before)
  })

  // Expiration-disabled drafts retain operation-specific API meanings during submission. Creation
  // omits its expiry, whereas editing emits the empty string that clears expiry.
  it('distinguishes omitted creation expiry from the update clearing value', () => {
    const form = makeForm({ expiration_date: '2027-01-01T00:00:00Z' })
    expect(buildKeyFormPayload(form, false)).toMatchObject({
      expiresInDays: undefined,
      expiresAt: undefined,
    })
    expect(buildKeyFormPayload(form, true)).toMatchObject({
      expiresInDays: undefined,
      expiresAt: '',
    })
    expect(buildKeyFormPayload(makeForm({ enable_expiration: true }), true).expiresAt).toBe('')
  })

  // Creation rounds partial elapsed days upward at the frozen submission time.
  // Past and invalid nonempty dates retain the existing minimum one-day result.
  it.each([
    ['2026-10-22T00:00:00Z', 1],
    ['2026-10-22T00:00:00.001Z', 2],
    ['2026-10-20T00:00:00Z', 1],
    ['invalid-date', 1],
  ])('converts creation date %s to %s days', (date, days) => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-21T00:00:00Z'))
    const result = buildKeyFormPayload(
      makeForm({ enable_expiration: true, expiration_date: date }),
      false,
    )
    expect(result.expiresInDays).toBe(days)
    expect(result.expiresAt).toBeUndefined()
  })

  // Editing forwards an absolute ISO instant instead of rounding remaining days.
  // An invalid nonempty date keeps the existing conversion error for the caller.
  it('preserves update instants and propagates invalid-date conversion errors', () => {
    const form = makeForm({ enable_expiration: true, expiration_date: '2026-10-21T10:30:00+08:00' })
    expect(buildKeyFormPayload(form, true)).toMatchObject({
      expiresAt: '2026-10-21T02:30:00.000Z',
      expiresInDays: undefined,
    })
    expect(() => buildKeyFormPayload({ ...form, expiration_date: 'invalid-date' }, true)).toThrow(
      RangeError,
    )
  })
})
