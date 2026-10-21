import { describe, it, expect } from 'vitest'
import {
  ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY,
  applyAntigravityProjectID,
  applyInterceptWarmup,
} from '@/components/provider/credentialsBuilder'
import {
  HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY,
  HEADER_OVERRIDES_CREDENTIAL_KEY,
  applyHeaderOverride,
  buildHeaderOverridesObject,
  isHeaderOverrideCapable,
  parseHeaderOverridesJson,
  serializeHeaderOverrideRows,
  splitHeaderOverridesObject,
  validateHeaderOverrideRows,
} from '@/components/provider/form/headerOverrides'
import {
  applyPlanType,
  buildPlanTypeOptions,
  planTypeDisplayLabel,
  readPlanType,
} from '@/components/provider/form/planType'
import {
  isCustomGrokBaseUrl,
  GROK_BASE_URL_PRESETS,
} from '@/components/provider/form/providerEndpoints'

// credentialsBuilder.spec.ts tests credential field updates, header override
// validation, endpoint classification, and plan display helpers. Cases distinguish
// creation from editing, preserve unrelated credential values, and exercise
// normalization, reserved headers, encoded-size limits, and exact stored plan
// identities.

describe('applyInterceptWarmup', () => {
  // Creation enables warmup interception on a credential object containing an access
  // token. The helper must add the enabled flag while leaving the existing credential
  // available.
  it('create + enabled=true: should set intercept_warmup_requests to true', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyInterceptWarmup(creds, true, 'create')
    expect(creds.intercept_warmup_requests).toBe(true)
  })

  // Creation leaves warmup interception disabled on a credential object containing an
  // access token. The helper must omit the optional interception key from the resulting
  // credentials.
  it('create + enabled=false: should not add the field', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyInterceptWarmup(creds, false, 'create')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })

  // Editing enables warmup interception on an existing API-key credential object. The
  // helper must write the enabled flag so the subsequent provider update can persist the
  // choice.
  it('edit + enabled=true: should set intercept_warmup_requests to true', () => {
    const creds: Record<string, unknown> = { api_key: 'sk' }
    applyInterceptWarmup(creds, true, 'edit')
    expect(creds.intercept_warmup_requests).toBe(true)
  })

  // Editing disables warmup interception when its key already exists in credentials. The
  // helper must delete that key so the saved configuration no longer carries the enabled
  // flag.
  it('edit + enabled=false + field exists: should delete the field', () => {
    const creds: Record<string, unknown> = { api_key: 'sk', intercept_warmup_requests: true }
    applyInterceptWarmup(creds, false, 'edit')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })

  // Editing disables warmup interception on credentials that never contained its key. The
  // helper must leave the key absent and complete successfully for this already-cleared
  // state.
  it('edit + enabled=false + field absent: should not throw', () => {
    const creds: Record<string, unknown> = { api_key: 'sk' }
    applyInterceptWarmup(creds, false, 'edit')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })

  // Credentials contain an API key and base URL alongside enabled warmup interception.
  // Disabling interception must remove its key while retaining the other credential
  // fields and values.
  it('should not affect other fields', () => {
    const creds: Record<string, unknown> = {
      api_key: 'sk',
      base_url: 'url',
      intercept_warmup_requests: true,
    }
    applyInterceptWarmup(creds, false, 'edit')
    expect(creds.api_key).toBe('sk')
    expect(creds.base_url).toBe('url')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })
})
describe('applyAntigravityProjectID', () => {
  // Creation supplies a project fallback with surrounding whitespace to the Antigravity
  // helper. The helper must trim the value and store it under the dedicated fallback
  // credential key.
  it('create + project id: trims and stores configured project fallback', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyAntigravityProjectID(creds, '  configured-project  ', 'create')
    expect(creds[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]).toBe('configured-project')
  })

  // Creation supplies only whitespace as the Antigravity project fallback draft. The
  // helper must omit the optional fallback key while preserving the provided
  // authentication credential object.
  it('create + empty project id: should not add the field', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyAntigravityProjectID(creds, '   ', 'create')
    expect(ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY in creds).toBe(false)
  })

  // Editing clears a previously configured Antigravity project fallback using an empty
  // string. The helper must delete the fallback key so the next update removes that
  // explicit value.
  it('edit + empty project id: deletes existing fallback', () => {
    const creds: Record<string, unknown> = {
      access_token: 'tok',
      [ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]: 'old-project',
    }
    applyAntigravityProjectID(creds, '', 'edit')
    expect(ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY in creds).toBe(false)
  })

  // Credentials already contain an onboard project ID and a model mapping before adding a
  // fallback. The helper must keep those fields distinct from the configured fallback
  // project.
  it('does not affect onboard project_id or other credentials', () => {
    const creds: Record<string, unknown> = {
      project_id: 'onboard-project',
      model_mapping: { 'gemini-*': 'gemini-2.5-flash' },
    }
    applyAntigravityProjectID(creds, 'configured-project', 'edit')
    expect(creds.project_id).toBe('onboard-project')
    expect(creds.model_mapping).toEqual({ 'gemini-*': 'gemini-2.5-flash' })
    expect(creds[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]).toBe('configured-project')
  })
})
describe('isHeaderOverrideCapable', () => {
  // Anthropic and OpenAI credentials are checked for header override eligibility across
  // API-key and OAuth types. The helper must enable the tested API-key combinations and
  // reject their OAuth variants.
  it('anthropic/openai only support apikey providers', () => {
    expect(isHeaderOverrideCapable('anthropic', 'apikey')).toBe(true)
    expect(isHeaderOverrideCapable('openai', 'apikey')).toBe(true)
    expect(isHeaderOverrideCapable('anthropic', 'oauth')).toBe(false)
    expect(isHeaderOverrideCapable('openai', 'oauth')).toBe(false)
  })

  // CN providers are checked for header override eligibility across API-key and OAuth
  // credential types. Each tested platform must accept API-key overrides and reject the
  // corresponding OAuth combination.
  it('kimi/zhipu/deepseek only support apikey providers', () => {
    for (const platform of ['kimi', 'zhipu', 'deepseek']) {
      expect(isHeaderOverrideCapable(platform, 'apikey')).toBe(true)
      expect(isHeaderOverrideCapable(platform, 'oauth')).toBe(false)
    }
  })

  // Grok provider credentials are checked across API-key, OAuth, and Bedrock types. The
  // helper must accept the first two forms and reject Bedrock in this capability
  // decision.
  it('grok supports both apikey and oauth providers', () => {
    expect(isHeaderOverrideCapable('grok', 'apikey')).toBe(true)
    expect(isHeaderOverrideCapable('grok', 'oauth')).toBe(true)
    expect(isHeaderOverrideCapable('grok', 'bedrock')).toBe(false)
  })

  // Platforms outside the header override allowlist are checked alongside an empty
  // platform name. The helper must reject these inputs rather than expose the unsupported
  // override editor.
  it('other platforms are not supported', () => {
    expect(isHeaderOverrideCapable('gemini', 'apikey')).toBe(false)
    expect(isHeaderOverrideCapable('antigravity', 'apikey')).toBe(false)
    expect(isHeaderOverrideCapable('', 'apikey')).toBe(false)
  })
})

describe('parseHeaderOverridesJson', () => {
  // A flat JSON object contains strings, numeric values, and booleans with mixed
  // whitespace. Parsing must return sorted rows with normalized string values suitable
  // for the editor.
  it('parses a flat object and normalizes values to trimmed strings', () => {
    expect(
      parseHeaderOverridesJson('{"User-Agent": " my-client/1.0 ", "x-num": 3, "x-flag": true}'),
    ).toEqual([
      { name: 'User-Agent', value: 'my-client/1.0' },
      { name: 'x-flag', value: 'true' },
      { name: 'x-num', value: '3' },
    ])
  })

  // Imported JSON contains a blank header name alongside a valid named entry. Parsing
  // must discard the blank-name entry and retain the normalized valid header row.
  it('drops entries with blank names', () => {
    expect(parseHeaderOverridesJson('{"  ": "v", "x-app": "cli"}')).toEqual([
      { name: 'x-app', value: 'cli' },
    ])
  })

  // Imported JSON includes malformed text, arrays, primitives, nested objects, and null
  // values. Parsing must return null for each invalid structure so the editor can report
  // rejection.
  it('rejects invalid JSON, arrays, primitives and nested values', () => {
    expect(parseHeaderOverridesJson('not json')).toBeNull()
    expect(parseHeaderOverridesJson('[1,2]')).toBeNull()
    expect(parseHeaderOverridesJson('"str"')).toBeNull()
    expect(parseHeaderOverridesJson('null')).toBeNull()
    expect(parseHeaderOverridesJson('{"a": {"b": 1}}')).toBeNull()
    expect(parseHeaderOverridesJson('{"a": null}')).toBeNull()
  })

  // An empty JSON object is imported into the header row parser. It must produce an empty
  // array, distinguishing an intentional clear operation from rejected JSON input.
  it('parses an empty object to an empty row list', () => {
    expect(parseHeaderOverridesJson('{}')).toEqual([])
  })
})

describe('serializeHeaderOverrideRows', () => {
  // Header drafts contain padded names, padded values, and an unnamed placeholder row.
  // Serialization must normalize named rows and omit the placeholder while retaining an
  // empty named value.
  it('serializes named rows and skips empty placeholder rows', () => {
    const text = serializeHeaderOverrideRows([
      { name: ' user-agent ', value: ' my-client/1.0 ' },
      { name: '', value: 'ignored' },
      { name: 'x-app', value: '' },
    ])
    expect(JSON.parse(text)).toEqual({ 'user-agent': 'my-client/1.0', 'x-app': '' })
  })

  // Valid header rows are serialized and then parsed back into editor drafts. The round
  // trip must preserve both normalized header names and their associated string values.
  it('round-trips with parseHeaderOverridesJson', () => {
    const rows = [
      { name: 'a-header', value: '1' },
      { name: 'b-header', value: '2' },
    ]
    expect(parseHeaderOverridesJson(serializeHeaderOverrideRows(rows))).toEqual(rows)
  })
})

describe('isCustomGrokBaseUrl', () => {
  // Grok base URLs point at the default CLI gateway using different URL casing and port
  // syntax. The helper must recognize those normalized destinations as the default.
  it('treats only the default CLI gateway host as not customized', () => {
    expect(isCustomGrokBaseUrl('https://cli-chat-proxy.grok.com/v1')).toBe(false)
    expect(isCustomGrokBaseUrl('HTTPS://CLI-CHAT-PROXY.GROK.COM:443/')).toBe(false)
  })

  // Grok base URLs point at official and regional API destinations selected manually. The
  // helper must classify these addresses as customized so edit hydration keeps them
  // visible.
  it('treats manually switched official/regional endpoints as customized (must echo back)', () => {
    expect(isCustomGrokBaseUrl('https://api.x.ai/v1')).toBe(true)
    expect(isCustomGrokBaseUrl('https://us-west-2.api.x.ai/v1')).toBe(true)
    expect(isCustomGrokBaseUrl('https://eu-west-1.api.x.ai/v1')).toBe(true)
  })

  // Grok base URL inputs are empty, non-string, or unparseable values. The helper must
  // classify these cases as unconfigured rather than expose an unusable custom address
  // draft.
  it('treats empty, non-string and unparseable values as not customized', () => {
    expect(isCustomGrokBaseUrl('')).toBe(false)
    expect(isCustomGrokBaseUrl('   ')).toBe(false)
    expect(isCustomGrokBaseUrl(undefined)).toBe(false)
    expect(isCustomGrokBaseUrl(42)).toBe(false)
    expect(isCustomGrokBaseUrl('not a url')).toBe(false)
  })

  // Grok base URLs point at third-party hosts with varied paths and schemes. The helper
  // must classify each supplied relay address as customized for edit-form restoration.
  it('treats third-party hosts as customized', () => {
    expect(isCustomGrokBaseUrl('https://relay.example.com/v1')).toBe(true)
    expect(isCustomGrokBaseUrl('https://relay.example.com/xai/v1')).toBe(true)
    expect(isCustomGrokBaseUrl('http://relay.example.com/v1')).toBe(true)
  })
})

describe('GROK_BASE_URL_PRESETS', () => {
  // The preset list exposes the CLI gateway, official API, and three regional
  // destinations. Each entry must carry an appropriate translation key or literal
  // regional label for display.
  it('covers the CLI gateway, official API and regional endpoints', () => {
    const urls = GROK_BASE_URL_PRESETS.map((p) => p.url)
    expect(urls).toEqual([
      'https://cli-chat-proxy.grok.com/v1',
      'https://api.x.ai/v1',
      'https://us-east-1.api.x.ai/v1',
      'https://us-west-2.api.x.ai/v1',
      'https://eu-west-1.api.x.ai/v1',
    ])
    for (const preset of GROK_BASE_URL_PRESETS) {
      expect(Boolean(preset.labelKey) || Boolean(preset.label)).toBe(true)
      if (preset.labelKey) {
        expect(['cli', 'official']).toContain(preset.labelKey)
      }
    }

    expect(GROK_BASE_URL_PRESETS[2].label).toBe('us-east-1')
    expect(GROK_BASE_URL_PRESETS[3].label).toBe('us-west-2')
    expect(GROK_BASE_URL_PRESETS[4].label).toBe('eu-west-1')
  })
})

describe('validateHeaderOverrideRows', () => {
  // Header validation receives valid rows together with an entirely empty placeholder
  // draft. It must accept the list so an unused editor row does not prevent saving.
  it('accepts valid rows and empty placeholder rows', () => {
    expect(
      validateHeaderOverrideRows([
        { name: 'user-agent', value: 'my-agent/1.0' },
        { name: 'x-app', value: '' },
        { name: '', value: '' },
      ]),
    ).toBeNull()
  })

  // A header draft has a nonempty value but no corresponding name. Validation must report
  // invalidName so the value cannot silently disappear during provider credential
  // construction.
  it('rejects empty name with non-empty value', () => {
    expect(validateHeaderOverrideRows([{ name: '', value: 'v' }])).toBe('invalidName')
  })

  // Header draft names contain whitespace, separators, or non-ASCII characters outside
  // token syntax. Validation must report invalidName for each malformed input supplied by
  // this test.
  it('rejects invalid header names', () => {
    expect(validateHeaderOverrideRows([{ name: 'bad name', value: '' }])).toBe('invalidName')
    expect(validateHeaderOverrideRows([{ name: 'bad:name', value: '' }])).toBe('invalidName')
    expect(validateHeaderOverrideRows([{ name: '名称', value: '' }])).toBe('invalidName')
  })

  // Header names target authentication, host, content framing, or other protected request
  // fields. Validation must reject the case-insensitive names with blockedName before
  // credentials are built.
  it('rejects blocked header names case-insensitively', () => {
    expect(validateHeaderOverrideRows([{ name: 'Authorization', value: '' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'X-Api-Key', value: '' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'host', value: '' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'Content-Length', value: '' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'Content-Type', value: '' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'Cookie', value: '' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'x-goog-api-key', value: '' }])).toBe('blockedName')
  })

  // Two header drafts use different casing for the same HTTP header name. Validation must
  // report duplicateName so the case-insensitive destination cannot receive conflicting
  // configured values.
  it('rejects duplicate names case-insensitively', () => {
    expect(
      validateHeaderOverrideRows([
        { name: 'User-Agent', value: 'a' },
        { name: 'user-agent', value: 'b' },
      ]),
    ).toBe('duplicateName')
  })
})

describe('buildHeaderOverridesObject / splitHeaderOverridesObject', () => {
  // Header construction receives padded names and values together with an unnamed row. It
  // must lowercase names, trim values, and exclude entries that have no usable name.
  it('lowercases names, trims values and drops empty-name rows', () => {
    expect(
      buildHeaderOverridesObject([
        { name: ' User-Agent ', value: ' my-agent ' },
        { name: 'X-App', value: '' },
        { name: '', value: 'ignored' },
      ]),
    ).toEqual({ 'user-agent': 'my-agent', 'x-app': '' })
  })

  // Saved header configuration mixes strings with invalid values and unsupported
  // container shapes. Splitting must return sorted valid rows or an empty list for
  // invalid outer structures.
  it('splits an object into sorted rows and ignores non-string values', () => {
    expect(splitHeaderOverridesObject({ 'x-app': 'cli', 'user-agent': 'ua', bogus: 42 })).toEqual([
      { name: 'user-agent', value: 'ua' },
      { name: 'x-app', value: 'cli' },
    ])
    expect(splitHeaderOverridesObject(null)).toEqual([])
    expect(splitHeaderOverridesObject(['a'])).toEqual([])
    expect(splitHeaderOverridesObject('str')).toEqual([])
  })

  // Normalized header rows are converted to a credential object and split back again. The
  // resulting editor rows must retain the same names, values, and stable sorted order.
  it('roundtrips through build and split', () => {
    const rows = [
      { name: 'user-agent', value: 'ua' },
      { name: 'x-app', value: 'cli' },
    ]
    expect(splitHeaderOverridesObject(buildHeaderOverridesObject(rows))).toEqual(rows)
  })
})

describe('applyHeaderOverride', () => {
  // Creation enables header overrides with a configured User-Agent row. The helper must
  // write the enabled flag and normalized overrides object alongside existing credentials
  // for the provider.
  it('create + enabled: writes enabled flag and overrides object', () => {
    const creds: Record<string, unknown> = { api_key: 'sk' }
    applyHeaderOverride(creds, true, [{ name: 'User-Agent', value: 'ua' }], 'create')
    expect(creds[HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY]).toBe(true)
    expect(creds[HEADER_OVERRIDES_CREDENTIAL_KEY]).toEqual({ 'user-agent': 'ua' })
  })

  // Creation disables header overrides while supplied drafts still contain a header row.
  // The helper must omit both override-related keys so disabled drafts do not enter
  // credentials.
  it('create + disabled: does not add fields', () => {
    const creds: Record<string, unknown> = { api_key: 'sk' }
    applyHeaderOverride(creds, false, [{ name: 'user-agent', value: 'ua' }], 'create')
    expect(HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY in creds).toBe(false)
    expect(HEADER_OVERRIDES_CREDENTIAL_KEY in creds).toBe(false)
  })

  // Editing disables header overrides on credentials already carrying both related keys.
  // The helper must remove those keys while retaining the independent API credential in
  // the object.
  it('edit + disabled: deletes existing fields', () => {
    const creds: Record<string, unknown> = {
      api_key: 'sk',
      [HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY]: true,
      [HEADER_OVERRIDES_CREDENTIAL_KEY]: { 'user-agent': 'ua' },
    }
    applyHeaderOverride(creds, false, [], 'edit')
    expect(HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY in creds).toBe(false)
    expect(HEADER_OVERRIDES_CREDENTIAL_KEY in creds).toBe(false)
    expect(creds.api_key).toBe('sk')
  })

  // Editing enables header overrides with a replacement row after old overrides were
  // saved. The helper must replace the override object completely rather than retain the
  // previous header.
  it('edit + enabled: replaces overrides object wholesale', () => {
    const creds: Record<string, unknown> = {
      [HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY]: true,
      [HEADER_OVERRIDES_CREDENTIAL_KEY]: { 'x-old': 'old' },
    }
    applyHeaderOverride(creds, true, [{ name: 'x-new', value: 'new' }], 'edit')
    expect(creds[HEADER_OVERRIDES_CREDENTIAL_KEY]).toEqual({ 'x-new': 'new' })
  })
})

describe('validateHeaderOverrideRows value/entry limits', () => {
  // A configured header attempts to replace a WebSocket handshake field. Validation must
  // report blockedName so administrator overrides cannot supply the per-connection
  // handshake value in this case.
  it('rejects websocket handshake headers', () => {
    expect(validateHeaderOverrideRows([{ name: 'Sec-WebSocket-Key', value: '' }])).toBe(
      'blockedName',
    )
  })

  // A header value contains a vertical-tab control character within otherwise ordinary
  // text. Validation must report invalidValue before that value can be serialized into
  // the outbound configuration.
  it('rejects control characters in values', () => {
    expect(validateHeaderOverrideRows([{ name: 'x-app', value: 'a\x0bb' }])).toBe('invalidValue')
  })

  // A header value exceeds the supported limit using ASCII characters alone. Validation
  // must report invalidValue for the oversized draft before the provider form can save
  // it.
  it('rejects oversized values', () => {
    expect(validateHeaderOverrideRows([{ name: 'x-app', value: 'a'.repeat(8193) }])).toBe(
      'invalidValue',
    )
  })

  // Header values contain CJK characters whose UTF-8 byte length exceeds their JavaScript
  // string length. Validation must reject the oversized byte count while accepting the
  // smaller encoded value.
  it('measures value length in UTF-8 bytes to match backend', () => {
    // Three thousand CJK characters occupy nine thousand UTF-8 bytes, exceeding the byte
    // limit despite their shorter JavaScript string length.
    expect(validateHeaderOverrideRows([{ name: 'x-app', value: '测'.repeat(3000) }])).toBe(
      'invalidValue',
    )
    expect(validateHeaderOverrideRows([{ name: 'x-app', value: '测'.repeat(2000) }])).toBeNull()
  })

  // The editor supplies sixty-five named header entries in one override list. Validation
  // must report tooManyEntries because the configured list exceeds the supported maximum
  // entry count.
  it('rejects too many entries', () => {
    const rows = Array.from({ length: 65 }, (_, i) => ({ name: `x-h-${i}`, value: 'v' }))
    expect(validateHeaderOverrideRows(rows)).toBe('tooManyEntries')
  })
})

describe('validateHeaderOverrideRows session isolation headers', () => {
  // Configured headers target request-specific session and conversation identity fields
  // using varied casing. Validation must reject each reserved field so overrides cannot
  // replace those request-owned identities.
  it('rejects per-request session headers', () => {
    expect(validateHeaderOverrideRows([{ name: 'session_id', value: '' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'Conversation_ID', value: '' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'x-codex-turn-state', value: '' }])).toBe(
      'blockedName',
    )
    expect(validateHeaderOverrideRows([{ name: 'X-Claude-Code-Session-Id', value: '' }])).toBe(
      'blockedName',
    )
    expect(validateHeaderOverrideRows([{ name: 'x-client-request-id', value: '' }])).toBe(
      'blockedName',
    )
  })

  // A header value contains a horizontal tab between ordinary characters. Validation must
  // accept that allowed whitespace while the surrounding tests separately cover
  // prohibited control characters.
  it('allows tab inside value', () => {
    expect(validateHeaderOverrideRows([{ name: 'x-app', value: 'a\tb' }])).toBeNull()
  })

  // A header name exceeds the supported length using valid token characters. Validation
  // must report invalidName based on its size even though each individual character is
  // otherwise acceptable.
  it('rejects oversized names', () => {
    expect(validateHeaderOverrideRows([{ name: 'x'.repeat(201), value: 'v' }])).toBe('invalidName')
  })
})

describe('plan_type helpers', () => {
  describe('planTypeDisplayLabel', () => {
    // Plan values include canonical labels, aliases, and an uppercase variant of a known
    // value. Display formatting must resolve each tested name to its expected friendly
    // label.
    it('maps canonical + alias values to friendly labels', () => {
      expect(planTypeDisplayLabel('plus')).toBe('Plus')
      expect(planTypeDisplayLabel('pro')).toBe('Pro')
      expect(planTypeDisplayLabel('chatgptpro')).toBe('Pro')
      expect(planTypeDisplayLabel('free')).toBe('Free')
      expect(planTypeDisplayLabel('team')).toBe('Team')
      expect(planTypeDisplayLabel('CHATGPTPRO')).toBe('Pro')
    })
    // A plan value is unknown to the friendly-label mapping used by provider forms.
    // Display formatting must return the exact stored value so custom plans remain
    // identifiable.
    it('returns unknown values verbatim', () => {
      expect(planTypeDisplayLabel('self_serve_business')).toBe('self_serve_business')
    })
  })

  describe('readPlanType', () => {
    // Credentials contain string plan_type values with and without surrounding
    // whitespace. Reading must return trimmed strings so forms can select the stored plan
    // value consistently.
    it('reads a string plan_type', () => {
      expect(readPlanType({ plan_type: 'plus' })).toBe('plus')
      expect(readPlanType({ plan_type: '  chatgptpro  ' })).toBe('chatgptpro')
    })
    // Credentials omit plan_type or provide non-string values, and some credential
    // containers are absent. Reading must return an empty string for each unusable plan
    // value tested here.
    it('treats non-string / missing values as empty', () => {
      expect(readPlanType({ plan_type: 42 })).toBe('')
      expect(readPlanType({ plan_type: true })).toBe('')
      expect(readPlanType({})).toBe('')
      expect(readPlanType(undefined)).toBe('')
      expect(readPlanType(null)).toBe('')
    })
  })

  describe('buildPlanTypeOptions', () => {
    const clear = 'Clear'
    // The plan selector receives an empty current value and a localized clear label. Its
    // options must include the clear choice followed by the standard plan presets.
    it('returns clear + presets when current is empty', () => {
      expect(buildPlanTypeOptions('', clear)).toEqual([
        { value: '', label: clear },
        { value: 'plus', label: 'Plus' },
        { value: 'pro', label: 'Pro' },
        { value: 'free', label: 'Free' },
      ])
    })
    // The stored chatgptpro alias shares the Pro label with a preset. Option construction
    // must keep one Pro option whose wire value exactly matches the stored alias.
    it('keeps canonical chatgptpro under a single friendly "Pro" option (no duplicate)', () => {
      const opts = buildPlanTypeOptions('chatgptpro', clear)
      const pros = opts.filter((o) => o.label === 'Pro')
      expect(pros).toHaveLength(1)
      expect(pros[0].value).toBe('chatgptpro')
      expect(opts.map((o) => o.value)).toEqual(['', 'plus', 'chatgptpro', 'free'])
    })
    // The stored team plan has a known label but is outside the standard presets. Option
    // construction must append its distinct value while retaining the preset choices.
    it('appends an unknown-but-labeled value (team) as its own option', () => {
      const opts = buildPlanTypeOptions('team', clear)
      expect(opts.find((o) => o.value === 'team')).toEqual({ value: 'team', label: 'Team' })

      expect(opts.map((o) => o.value)).toEqual(['', 'plus', 'pro', 'free', 'team'])
    })
    // The stored plan is a custom value with no friendly-label mapping. Option
    // construction must append that value using its raw identifier as the visible option
    // label.
    it('appends a fully custom value with a raw label', () => {
      const opts = buildPlanTypeOptions('weird_x', clear)
      expect(opts.at(-1)).toEqual({ value: 'weird_x', label: 'weird_x' })
    })
    // The stored plan already exactly matches a standard preset value. Option
    // construction must retain one instance of that preset and avoid appending a
    // duplicate choice.
    it('does not duplicate an exact preset value', () => {
      const opts = buildPlanTypeOptions('pro', clear)
      expect(opts.filter((o) => o.value === 'pro')).toHaveLength(1)
      expect(opts.map((o) => o.value)).toEqual(['', 'plus', 'pro', 'free'])
    })
    // The stored plan uses uppercase spelling for a known friendly label. Option
    // construction must retain that exact wire value so the Select control can match the
    // current selection.
    it('preserves the exact value for case variants so Select can match it', () => {
      const opts = buildPlanTypeOptions('PLUS', clear)
      expect(opts.filter((o) => o.label === 'Plus')).toEqual([{ value: 'PLUS', label: 'Plus' }])
      expect(opts.map((o) => o.value)).toEqual(['', 'PLUS', 'pro', 'free'])
    })
  })

  describe('applyPlanType', () => {
    // Credentials contain account metadata and model mappings before a plan is applied.
    // The helper must set plan_type while retaining every unrelated credential key and
    // its value.
    it('sets plan_type and preserves all other credential keys', () => {
      const creds = {
        chatgpt_account_id: 'acc',
        email: 'a@b.c',
        subscription_expires_at: '2026-01-01',
        model_mapping: { x: 'y' },
      }
      const out = applyPlanType({ ...creds }, 'plus')
      expect(out).toEqual({ ...creds, plan_type: 'plus' })
    })
    // The plan draft includes whitespace around a supported value during application. The
    // helper must store the trimmed wire value in the returned credential object for
    // persistence.
    it('trims the value', () => {
      expect(applyPlanType({}, '  pro  ')).toEqual({ plan_type: 'pro' })
    })
    // The plan draft is cleared while credentials still contain a saved plan and email.
    // The helper must delete plan_type and retain the independent email value in the
    // result.
    it('deletes the key when cleared (empty), keeping other keys', () => {
      const out = applyPlanType({ plan_type: 'pro', email: 'a@b.c' }, '')
      expect(out).toEqual({ email: 'a@b.c' })
      expect('plan_type' in out).toBe(false)
    })
  })
})
