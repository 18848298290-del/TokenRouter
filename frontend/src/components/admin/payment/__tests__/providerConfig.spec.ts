import { describe, expect, it } from 'vitest'
import {
  PAYMENT_CURRENCY_OPTIONS,
  PROVIDER_CONFIG_FIELDS,
  PROVIDER_SUPPORTED_TYPES,
  getAvailableTypes,
  parseEasyPayCustomMethods,
  serializeEasyPayCustomMethods,
} from '@/components/admin/payment/providerConfig'
import { isBuiltInAlipayMethod, isBuiltInWxpayMethod } from '@/utils/payment/paymentMethods'

// providerConfig.spec.ts checks credential field metadata, provider currency choices, and
// EasyPay method conversions. Direct assertions exercise the definitions consumed by the
// dialog and the serialized configuration representation, including malformed input and
// exact built-in payment aliases.

// findField retrieves a provider field definition by its configuration key. A missing
// definition returns undefined, allowing assertions to report absent or incorrectly
// configured inputs in the metadata.
function findField(providerKey: string, key: string) {
  const fields = PROVIDER_CONFIG_FIELDS[providerKey] || []
  return fields.find((field) => field.key === key)
}

describe('PROVIDER_CONFIG_FIELDS.wxpay', () => {
  // WeChat Pay field metadata is inspected as the administrator validation source.
  // Credentials required by the backend must remain required in the form definitions used
  // for saving.
  it('keeps admin form validation aligned with backend-required credentials', () => {
    expect(findField('wxpay', 'publicKeyId')?.optional).toBeFalsy()
    expect(findField('wxpay', 'certSerial')?.optional).toBeFalsy()
  })

  // WeChat Pay configuration exposes the supported credential set to administrators. The
  // metadata must omit mpAppId, h5AppName, and h5AppUrl from the credential controls exposed
  // to administrators.
  it('only keeps the simplified visible credential set in the admin form', () => {
    expect(findField('wxpay', 'mpAppId')).toBeUndefined()
    expect(findField('wxpay', 'h5AppName')).toBeUndefined()
    expect(findField('wxpay', 'h5AppUrl')).toBeUndefined()
  })
})

describe('PROVIDER_CONFIG_FIELDS.airwallex', () => {
  // Provider field definitions include a selectable payment currency input. Its default
  // must be CNY, its hint must use the payment-currency guidance key, and its options must
  // reference the shared currency list.
  it('adds currency config with CNY as the default', () => {
    const currency = findField('airwallex', 'currency')

    expect(currency?.defaultValue).toBe('CNY')
    expect(currency?.hintKey).toBe('admin.settings.payment.field_paymentCurrencyHint')
    expect(currency?.options).toBe(PAYMENT_CURRENCY_OPTIONS)
  })

  // Airwallex account selection can use the default account when no identifier is
  // supplied. Its field must be optional, clearable, and accompanied by the expected
  // explanatory hint.
  it('marks accountId as optional and explains when it can be left blank', () => {
    const accountId = findField('airwallex', 'accountId')

    expect(accountId?.optional).toBe(true)
    expect(accountId?.clearable).toBe(true)
    expect(accountId?.hintKey).toBe('admin.settings.payment.field_accountIdHint')
  })

  // Airwallex credentials belong to a particular API environment. The API base field must
  // expose the hint explaining that the configured base matches the environment of the
  // supplied API credentials.
  it('explains that apiBase must match the Airwallex key environment', () => {
    expect(findField('airwallex', 'apiBase')?.hintKey).toBe(
      'admin.settings.payment.field_airwallexApiBaseHint',
    )
  })
})

describe('PROVIDER_CONFIG_FIELDS.stripe', () => {
  // Provider currency metadata drives both default hydration and selectable form choices.
  // The field must default to CNY, provide the shared currency options, and expose the
  // payment-currency guidance key.
  it('adds currency config with CNY as the default', () => {
    const currency = findField('stripe', 'currency')

    expect(currency?.defaultValue).toBe('CNY')
    expect(currency?.hintKey).toBe('admin.settings.payment.field_paymentCurrencyHint')
    expect(currency?.options).toBe(PAYMENT_CURRENCY_OPTIONS)
  })

  // Stripe Checkout methods are controlled outside the administrator subtype selector. The
  // supported-type metadata must expose the single provider-level stripe option used by
  // configuration and limit controls.
  it('exposes only the top-level Stripe provider type in admin settings', () => {
    expect(PROVIDER_SUPPORTED_TYPES.stripe).toEqual(['stripe'])

    const types = getAvailableTypes(
      'stripe',
      [
        { value: 'stripe', label: 'Stripe' },
        { value: 'card', label: 'Card' },
        { value: 'link', label: 'Link' },
      ],
      'Redirect',
    )

    expect(types).toEqual([{ value: 'stripe', label: 'Stripe' }])
  })
})

describe('EasyPay custom methods config', () => {
  // EasyPay custom methods arrive as a JSON string inside provider configuration. Parsing
  // must retain their local code, upstream code, and display name as editable method
  // entries.
  it('parses customMethods from the JSON string stored in provider config', () => {
    expect(
      parseEasyPayCustomMethods(
        '[{"type":"ldc","upstreamType":"epay","displayName":"LDC"},{"type":"usdt_trc20","upstreamType":"usdt","displayName":"USDT-TRC20"}]',
      ),
    ).toEqual([
      { id: 'ldc', type: 'ldc', upstreamType: 'epay', displayName: 'LDC' },
      { id: 'usdt_trc20', type: 'usdt_trc20', upstreamType: 'usdt', displayName: 'USDT-TRC20' },
    ])
  })

  // Custom-method drafts include valid records and incomplete entries. Serialization must
  // trim the usable values and emit the complete methods in the JSON configuration string
  // format.
  it('serializes non-empty custom methods into the config string format', () => {
    expect(
      serializeEasyPayCustomMethods([
        { type: 'ldc', upstreamType: 'epay', displayName: 'LDC' },
        { type: '  ', upstreamType: 'ignored', displayName: 'Ignored' },
        { type: 'usdt_trc20', upstreamType: 'usdt', displayName: '' },
      ]),
    ).toBe(
      '[{"type":"ldc","upstreamType":"epay","displayName":"LDC"},{"type":"usdt_trc20","upstreamType":"usdt","displayName":""}]',
    )
  })

  // Custom-method input contains malformed JSON or lacks complete method codes. Parsing or
  // serialization must return its empty representation so invalid entries do not become
  // saved methods.
  it('returns an empty string for invalid or empty custom methods', () => {
    expect(parseEasyPayCustomMethods('not-json')).toEqual([])
    expect(
      serializeEasyPayCustomMethods([{ type: '', upstreamType: 'epay', displayName: 'LDC' }]),
    ).toBe('')
  })
})

describe('built-in payment method helpers', () => {
  // Payment method codes include built-in aliases and similarly prefixed custom values.
  // The shared classification must recognize exact aliases while leaving custom prefix
  // matches as distinct methods.
  it('only treats exact built-in aliases as Alipay or WeChat Pay', () => {
    expect(isBuiltInAlipayMethod('alipay')).toBe(true)
    expect(isBuiltInAlipayMethod('alipay_direct')).toBe(true)
    expect(isBuiltInAlipayMethod('card_alipay')).toBe(false)

    expect(isBuiltInWxpayMethod('wxpay')).toBe(true)
    expect(isBuiltInWxpayMethod('wxpay_direct')).toBe(true)
    expect(isBuiltInWxpayMethod('card_wxpay')).toBe(false)
  })
})
