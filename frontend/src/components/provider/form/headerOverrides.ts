// headerOverrides.ts validates, imports, exports, and stores provider
// request-header overrides. The editor represents headers as ordered rows,
// while credentials store a name-to-value object. Validation reports the
// first row error before the entry limit, and object conversion trims values
// and lowercases names.
//
// Import and export have different purposes. JSON import accepts scalar
// values and exposes them as editable strings; export keeps the entered name
// casing for copying. Credential construction uses normalized names and
// depends on callers validating first. Edit-mode disabling removes both
// stored fields, while disabled creation leaves the supplied draft
// untouched.

// HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY names the credential flag read when
// deciding whether configured overrides apply.
export const HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY = 'header_override_enabled'

// HEADER_OVERRIDES_CREDENTIAL_KEY names the credential object whose entries
// contain the configured outbound header overrides.
export const HEADER_OVERRIDES_CREDENTIAL_KEY = 'header_overrides'

// HeaderOverrideRow represents one editable name and value pair in provider
// forms.
export interface HeaderOverrideRow {
  name: string // Entered header name.
  value: string // Entered header value.
}

// isHeaderOverrideCapable reports whether a platform and credential type
// support overrides. Grok accepts API keys and OAuth credentials; the listed
// API platforms accept API keys only.
export function isHeaderOverrideCapable(platform: string, type: string): boolean {
  if (
    platform === 'anthropic' ||
    platform === 'openai' ||
    platform === 'kimi' ||
    platform === 'zhipu' ||
    platform === 'deepseek' ||
    platform === 'jev'
  ) {
    return type === 'apikey'
  }
  if (platform === 'grok') {
    return type === 'apikey' || type === 'oauth'
  }
  return false
}

// HEADER_OVERRIDE_BLOCKED_NAMES mirrors backend restrictions that reserve
// transport, authentication, session identity, and request-correlation
// headers.
const HEADER_OVERRIDE_BLOCKED_NAMES = new Set([
  'host',
  'content-length',
  'content-type',
  'transfer-encoding',
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'proxy-connection',
  'te',
  'trailer',
  'upgrade',
  'authorization',
  'x-api-key',
  'x-goog-api-key',
  'cookie',
  'accept-encoding',
  'sec-websocket-key',
  'sec-websocket-version',
  'sec-websocket-extensions',
  'sec-websocket-protocol',
  'sec-websocket-accept',
  'session_id',
  'conversation_id',
  'x-codex-turn-state',
  'x-codex-turn-metadata',
  'chatgpt-account-id',
  'x-claude-code-session-id',
  'x-client-request-id',
  'x-grok-conv-id',
])

// HEADER_NAME_PATTERN accepts the RFC 7230 token characters used for valid
// HTTP header names.
const HEADER_NAME_PATTERN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/

// isValidHeaderOverrideName checks whether a name contains valid HTTP token
// characters.
function isValidHeaderOverrideName(name: string): boolean {
  return HEADER_NAME_PATTERN.test(name)
}

// HEADER_OVERRIDE_MAX_ENTRIES limits populated rows to the entry count
// accepted by backend header validation.
const HEADER_OVERRIDE_MAX_ENTRIES = 64

// HEADER_OVERRIDE_MAX_NAME_LENGTH limits header names to the character count
// accepted by backend validation.
const HEADER_OVERRIDE_MAX_NAME_LENGTH = 200

// HEADER_OVERRIDE_MAX_VALUE_LENGTH limits encoded values to the byte count
// accepted by backend validation.
const HEADER_OVERRIDE_MAX_VALUE_LENGTH = 8192

// HEADER_VALUE_INVALID_PATTERN rejects control characters that backend HTTP
// header validation cannot forward to providers.
// eslint-disable-next-line no-control-regex
const HEADER_VALUE_INVALID_PATTERN = /[\x00-\x08\x0a-\x1f\x7f]/

// HEADER_TEXT_ENCODER measures UTF-8 bytes so frontend value limits match
// the backend Go length check.
const HEADER_TEXT_ENCODER = new TextEncoder()

// utf8ByteLength returns the encoded byte count used by header validation.
function utf8ByteLength(value: string): number {
  return HEADER_TEXT_ENCODER.encode(value).length
}

// validateHeaderOverrideRows returns the first validation error key, or null
// when every populated row is valid. Blank names with blank values are
// ignored. Names are trimmed, checked for syntax and reserved meanings, then
// checked for case-insensitive duplicates before values are validated.
//
// The entry-count limit is evaluated after row validation, so an invalid row
// wins over too many entries. Values are trimmed before checking control
// characters and UTF-8 byte length, matching the values emitted by
// buildHeaderOverridesObject.
export function validateHeaderOverrideRows(
  rows: HeaderOverrideRow[],
): 'invalidName' | 'blockedName' | 'duplicateName' | 'invalidValue' | 'tooManyEntries' | null {
  const seen = new Set<string>()
  for (const row of rows) {
    const name = row.name.trim()
    const value = row.value.trim()
    if (!name) {
      if (value) return 'invalidName'
      continue
    }
    if (!isValidHeaderOverrideName(name) || name.length > HEADER_OVERRIDE_MAX_NAME_LENGTH) {
      return 'invalidName'
    }
    const lower = name.toLowerCase()
    if (HEADER_OVERRIDE_BLOCKED_NAMES.has(lower)) return 'blockedName'
    if (seen.has(lower)) return 'duplicateName'
    if (
      HEADER_VALUE_INVALID_PATTERN.test(value) ||
      utf8ByteLength(value) > HEADER_OVERRIDE_MAX_VALUE_LENGTH
    ) {
      return 'invalidValue'
    }
    seen.add(lower)
  }
  if (seen.size > HEADER_OVERRIDE_MAX_ENTRIES) return 'tooManyEntries'
  return null
}

// buildHeaderOverridesObject converts rows into credential entries with
// lowercase names and trimmed values. Empty names are skipped; callers
// validate first because duplicate normalized names overwrite earlier
// entries.
export function buildHeaderOverridesObject(rows: HeaderOverrideRow[]): Record<string, string> {
  const result: Record<string, string> = {}
  for (const row of rows) {
    const name = row.name.trim().toLowerCase()
    if (!name) continue
    result[name] = row.value.trim()
  }
  return result
}

// splitHeaderOverridesObject converts a stored object into alphabetically
// ordered editable rows. Invalid containers produce an empty list, and
// entries with non-string values are excluded from the result.
export function splitHeaderOverridesObject(record: unknown): HeaderOverrideRow[] {
  if (!record || typeof record !== 'object' || Array.isArray(record)) return []
  return Object.entries(record as Record<string, unknown>)
    .filter(([, value]) => typeof value === 'string')
    .map(([name, value]) => ({ name, value: value as string }))
    .sort((a, b) => a.name.localeCompare(b.name))
}

// parseHeaderOverridesJson returns editable rows from a flat JSON object, or
// null for malformed input. String, number, and boolean values become
// trimmed strings; blank names are skipped before value validation.
export function parseHeaderOverridesJson(text: string): HeaderOverrideRow[] | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    // Invalid JSON remains an import error so the editor can keep its
    // existing rows and show feedback.
    return null
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null
  const rows: HeaderOverrideRow[] = []
  for (const [rawName, rawValue] of Object.entries(parsed as Record<string, unknown>)) {
    const name = rawName.trim()
    if (!name) continue
    if (
      typeof rawValue !== 'string' &&
      typeof rawValue !== 'number' &&
      typeof rawValue !== 'boolean'
    ) {
      return null
    }
    rows.push({ name, value: String(rawValue).trim() })
  }
  return rows.sort((a, b) => a.name.localeCompare(b.name))
}

// serializeHeaderOverrideRows produces indented JSON for copying or backing
// up configured overrides. It skips blank names, trims values, and retains
// name casing; repeated names use the last row.
export function serializeHeaderOverrideRows(rows: HeaderOverrideRow[]): string {
  const record: Record<string, string> = {}
  for (const row of rows) {
    const name = row.name.trim()
    if (!name) continue
    record[name] = row.value.trim()
  }
  return JSON.stringify(record, null, 2)
}

// applyHeaderOverride mutates a credential draft with the override toggle
// and converted rows. Disabling overrides deletes both fields in edit mode;
// create mode leaves them untouched when disabled.
export function applyHeaderOverride(
  credentials: Record<string, unknown>,
  enabled: boolean,
  rows: HeaderOverrideRow[],
  mode: 'create' | 'edit',
): void {
  if (enabled) {
    credentials[HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY] = true
    credentials[HEADER_OVERRIDES_CREDENTIAL_KEY] = buildHeaderOverridesObject(rows)
  } else if (mode === 'edit') {
    delete credentials[HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY]
    delete credentials[HEADER_OVERRIDES_CREDENTIAL_KEY]
  }
}
