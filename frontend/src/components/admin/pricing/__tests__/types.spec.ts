import { describe, expect, it } from 'vitest'
import {
  apiTimePricingToForm,
  createDefaultTimePricingForm,
  formTimePricingToAPI,
  hasExplicitPricing,
  isValidPositiveMultiplier,
  apiIntervalsToForm,
  formIntervalsToAPI,
  validateIntervals,
  validateTimePricing,
  type IntervalFormEntry,
  type PricingFormEntry,
  type TimePricingPeriodFormEntry,
} from '../types'

// types.spec.ts checks interval bounds, optional prices, schedules, and custom video labels
// through pure helper calls. Local object factories construct editable price fixtures, and
// key-returning translation callbacks expose validation codes alongside converted API amounts
// and schedule values.

// makeInterval creates an interval fixture merged with the requested test-specific field
// values.
function makeInterval(over: Partial<IntervalFormEntry>): IntervalFormEntry {
  return {
    min_tokens: 0,
    max_tokens: null,
    tier_label: '',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    input_multiplier: null,
    output_multiplier: null,
    cache_write_multiplier: null,
    cache_read_multiplier: null,
    per_request_price: null,
    sort_order: 0,
    ...over,
  }
}

// makePricingEntry creates a token price card merged with test-specific field values.
function makePricingEntry(over: Partial<PricingFormEntry>): PricingFormEntry {
  return {
    models: ['test-model'],
    billing_mode: 'token',
    price_multiplier: null,
    fast_mode_multiplier: null,
    fast_multiplier: null,
    flex_multiplier: null,
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
    time_pricing: createDefaultTimePricingForm(),
    ...over,
  }
}

// t exposes validation keys and supplied arguments directly for assertions against translated
// failures.
function t(key: string, params?: Record<string, unknown>): string {
  return `${key}${params ? ` ${JSON.stringify(params)}` : ''}`
}

describe('validateIntervals', () => {
  describe('token mode', () => {
    // An unbounded token interval is followed by another interval. Validation rejects that
    // ordering because the first interval would overlap every later token range.
    it('拒绝不在最后的无上限区间', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ min_tokens: 0, max_tokens: null, input_price: 1, output_price: 1 }),
        makeInterval({ min_tokens: 200000, max_tokens: 500000, input_price: 2, output_price: 2 }),
      ]
      expect(validateIntervals(intervals, 'token', t)).toContain('unboundedLast')
    })

    // The final token interval has no upper bound after finite earlier rows. Validation accepts
    // this ordering so the largest contexts remain covered by the last price tier.
    it('接受放在最后的无上限区间', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ min_tokens: 0, max_tokens: 200000, input_price: 1, output_price: 1 }),
        makeInterval({ min_tokens: 200000, max_tokens: null, input_price: 2, output_price: 2 }),
      ]
      expect(validateIntervals(intervals, 'token', t)).toBeNull()
    })

    // Two token intervals overlap after ordering by their lower bounds. Validation reports the
    // range conflict so the same context cannot select competing interval prices.
    it('拒绝重叠区间', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ min_tokens: 0, max_tokens: 250000, input_price: 1, output_price: 1 }),
        makeInterval({ min_tokens: 200000, max_tokens: 500000, input_price: 2, output_price: 2 }),
      ]
      expect(validateIntervals(intervals, 'token', t)).toContain('overlap')
    })

    // Token billing includes an unbounded interval before another row. Mode-aware validation
    // rejects the arrangement before the pricing form can submit conflicting token ranges.
    it('在 token 模式下拒绝不在最后的无上限区间', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ min_tokens: 0, max_tokens: null, input_price: 1, output_price: 1 }),
        makeInterval({ min_tokens: 100, max_tokens: 200, input_price: 2, output_price: 2 }),
      ]
      expect(validateIntervals(intervals, 'token', t)).toContain('unboundedLast')
    })
  })

  describe('image / per_request mode', () => {
    // Media intervals use distinct labels and unbounded context fields. Validation accepts
    // those rows because label-based media tiers do not use token-range overlap for selection.
    it('允许多个按标签识别的无上限层级', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ tier_label: '1K', per_request_price: 0.04 }),
        makeInterval({ tier_label: '2K', per_request_price: 0.06 }),
        makeInterval({ tier_label: '4K', per_request_price: 0.08 }),
      ]
      expect(validateIntervals(intervals, 'image', t)).toBeNull()
      expect(validateIntervals(intervals, 'per_request', t)).toBeNull()
    })

    // A media interval contains a negative amount despite label-based matching. Validation
    // still rejects the price so bypassing token overlap checks cannot bypass amount
    // validation.
    it('仍然拒绝负价格', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({ tier_label: '1K', per_request_price: -1 }),
      ]
      expect(validateIntervals(intervals, 'image', t)).toContain('negativePrice')
    })

    // A labeled interval has an upper bound equal to or below its lower bound. Individual bound
    // validation rejects the row even for media billing modes.
    it('仍然拒绝单个层级中 max <= min 的配置', () => {
      const intervals: IntervalFormEntry[] = [
        makeInterval({
          tier_label: '1K',
          min_tokens: 100,
          max_tokens: 50,
          per_request_price: 0.04,
        }),
      ]
      expect(validateIntervals(intervals, 'image', t)).toContain('maxGreaterThanMin')
    })
  })
})

describe('hasExplicitPricing', () => {
  // A token card has no configured amount or interval multiplier. Explicit-pricing detection
  // reports false so callers can enforce requirements for cards that claim a price override.
  it('拒绝没有任何价格的 token 定价', () => {
    expect(hasExplicitPricing(makePricingEntry({ price_multiplier: 1.5 }))).toBe(false)
    expect(hasExplicitPricing(makePricingEntry({ fast_mode_multiplier: 2 }))).toBe(false)
  })

  // Cards contain an explicit zero amount or a configured image interval amount. Presence
  // detection treats these as configured prices rather than confusing free pricing with
  // absence.
  it('把显式零价格和图片区间价格视为有效定价', () => {
    expect(hasExplicitPricing(makePricingEntry({ input_price: 0 }))).toBe(true)
    expect(hasExplicitPricing(makePricingEntry({ image_input_price: 0.01 }))).toBe(true)
    expect(
      hasExplicitPricing(
        makePricingEntry({
          billing_mode: 'image',
          intervals: [makeInterval({ tier_label: '1K', per_request_price: 0.04 })],
        }),
      ),
    ).toBe(true)
  })

  // A token interval has a configured multiplier with no direct amount. Explicit-pricing
  // detection accepts that supported interval configuration for callers that allow inherited
  // base-price scaling.
  it('把倍率-only token 区间视为有效定价', () => {
    expect(
      hasExplicitPricing(
        makePricingEntry({
          intervals: [makeInterval({ input_multiplier: 1.2 })],
        }),
      ),
    ).toBe(true)
  })

  // A card contains amounts belonging to a different billing mode. Presence detection ignores
  // those fields so stale hidden prices do not satisfy the selected mode's pricing requirement.
  it('不把当前计费模式无关的价格字段视为有效定价', () => {
    expect(
      hasExplicitPricing(
        makePricingEntry({
          billing_mode: 'image',
          input_price: 1,
        }),
      ),
    ).toBe(false)
    expect(
      hasExplicitPricing(
        makePricingEntry({
          billing_mode: 'token',
          per_request_price: 1,
        }),
      ),
    ).toBe(false)
  })
})

describe('tier multipliers', () => {
  // Multiplier validation receives a positive number, null, an empty string, zero, and a
  // negative number. It accepts the positive and inheritance values while rejecting zero and
  // the negative configured factor.
  it('只接受正数或空值', () => {
    expect(isValidPositiveMultiplier(null)).toBe(true)
    expect(isValidPositiveMultiplier('')).toBe(true)
    expect(isValidPositiveMultiplier(1.25)).toBe(true)
    expect(isValidPositiveMultiplier(0)).toBe(false)
    expect(isValidPositiveMultiplier(-1)).toBe(false)
  })

  // Interval multipliers and amounts pass through API-to-form and form-to-API conversion. Their
  // units and nullable values survive the round trip with token prices converted appropriately.
  it('区间倍率可在 API 与表单之间往返', () => {
    const api = formIntervalsToAPI([
      makeInterval({
        input_price: 2,
        input_multiplier: '1.2',
        cache_read_multiplier: 0.8,
      }),
    ])
    expect(api[0]).toMatchObject({
      input_price: 2e-6,
      input_multiplier: 1.2,
      cache_read_multiplier: 0.8,
    })
    expect(apiIntervalsToForm(api)[0]).toMatchObject({
      input_price: 2,
      input_multiplier: 1.2,
      cache_read_multiplier: 0.8,
    })
  })
})

describe('time pricing', () => {
  // An absent schedule and a legacy minute-precision schedule are converted for editing.
  // Defaults disable periods and legacy clock endpoints gain seconds before serialization back
  // to the API.
  it('默认关闭并可往返转换旧分钟精度配置', () => {
    const empty = createDefaultTimePricingForm()
    expect(empty).toEqual({ timezone: 'Asia/Shanghai', weekdays_only: false, periods: [] })
    expect(formTimePricingToAPI(empty)).toBeNull()

    const form = apiTimePricingToForm({
      timezone: 'Asia/Shanghai',
      periods: [{ start_time: '09:00', end_time: '12:00', multiplier: 2 }],
    })
    expect(form.periods[0]).toEqual({
      start_time: '09:00:00',
      end_time: '12:00:00',
      multiplier: '2.00',
    })
    expect(formTimePricingToAPI(form)?.periods[0].multiplier).toBe(2)
  })

  // Stored schedules omit or configure the weekday-only flag. Hydration supplies its default
  // and serialization retains explicit scope so schedule applicability survives the editable
  // representation.
  it('兼容缺省并往返工作日范围', () => {
    const legacy = apiTimePricingToForm({
      timezone: 'Asia/Shanghai',
      periods: [{ start_time: '09:00', end_time: '12:00', multiplier: 2 }],
    })
    expect(legacy.weekdays_only).toBe(false)

    const weekdays = apiTimePricingToForm({
      timezone: 'Asia/Shanghai',
      weekdays_only: true,
      periods: [{ start_time: '09:00', end_time: '12:00', multiplier: 2 }],
    })
    expect(weekdays.weekdays_only).toBe(true)
    expect(formTimePricingToAPI(weekdays)).toEqual({
      timezone: 'Asia/Shanghai',
      weekdays_only: true,
      periods: [{ start_time: '09:00:00', end_time: '12:00:00', multiplier: 2 }],
    })
  })

  // Parameterized schedule inputs vary clock ranges, overlap, and multiplier formatting.
  // Validation returns the expected failure for each invalid arrangement while retaining
  // supported adjacent daily periods.
  it.each([
    [
      '相邻区间',
      [
        { start_time: '09:00:00', end_time: '12:00:00', multiplier: '2.00' },
        { start_time: '12:00:00', end_time: '14:00:00', multiplier: '1.50' },
      ],
      null,
    ],
    [
      '午夜拆分',
      [
        { start_time: '22:00:00', end_time: '00:00:00', multiplier: '2.00' },
        { start_time: '00:00:00', end_time: '02:00:00', multiplier: '2.00' },
      ],
      null,
    ],
    [
      '重叠一秒',
      [
        { start_time: '09:00:00', end_time: '12:00:00', multiplier: '2.00' },
        { start_time: '11:59:59', end_time: '14:00:00', multiplier: '2.00' },
      ],
      'overlap',
    ],
    ['跨午夜', [{ start_time: '22:00:00', end_time: '02:00:00', multiplier: '2.00' }], 'range'],
    ['缺少秒', [{ start_time: '09:00', end_time: '12:00', multiplier: '2.00' }], 'format'],
    [
      '倍率过小',
      [{ start_time: '09:00:00', end_time: '12:00:00', multiplier: '0.001' }],
      'multiplier',
    ],
    [
      '倍率三位小数',
      [{ start_time: '09:00:00', end_time: '12:00:00', multiplier: '1.001' }],
      'multiplier',
    ],
  ])('%s', (_name, periods, errorKey) => {
    const result = validateTimePricing(
      {
        timezone: 'Asia/Shanghai',
        periods: periods as TimePricingPeriodFormEntry[],
      },
      t,
    )
    if (errorKey === null) expect(result).toBeNull()
    else expect(result).toContain(String(errorKey))
  })

  // A configured schedule names a timezone the browser cannot resolve. Validation returns the
  // timezone error before accepting daily periods that would lack a usable time basis.
  it('拒绝非 IANA 时区', () => {
    expect(
      validateTimePricing(
        {
          timezone: 'UTC+8',
          periods: [{ start_time: '09:00:00', end_time: '12:00:00', multiplier: '2.00' }],
        },
        t,
      ),
    ).toContain('timezone')
  })
})

describe('custom video resolutions', () => {
  // Video intervals contain custom dimensions and a free amount. Conversion retains their
  // labels and zero prices so administrator-defined resolutions survive the editable price
  // representation.
  it('round-trips custom labels and free amounts', () => {
    const rows = [
      makeInterval({ tier_label: '768P', per_request_price: 0.12 }),
      makeInterval({ tier_label: '2K', per_request_price: 0 }),
    ]
    expect(validateIntervals(rows, 'video', (key) => key)).toBeNull()
    expect(
      apiIntervalsToForm(formIntervalsToAPI(rows)).map((row) => [
        row.tier_label,
        row.per_request_price,
      ]),
    ).toEqual([
      ['768P', 0.12],
      ['2K', 0],
    ])
  })
  // Video labels contain canonical aliases, duplicate dimensions, or oversized text. Validation
  // compares normalized labels and rejects duplicate or unsupported label lengths before
  // persistence.
  it('rejects normalized duplicates and oversized resolution labels', () => {
    expect(
      validateIntervals(
        [makeInterval({ tier_label: 'HD' }), makeInterval({ tier_label: '720p' })],
        'video',
        (key) => key,
      ),
    ).toBe('admin.pricing.intervalValidation.duplicateVideoResolution')
    expect(
      validateIntervals([makeInterval({ tier_label: 'a'.repeat(51) })], 'video', (key) => key),
    ).toBe('admin.pricing.intervalValidation.invalidVideoResolution')
  })
})
