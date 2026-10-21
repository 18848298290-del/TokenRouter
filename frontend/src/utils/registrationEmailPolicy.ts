/**
 * registrationEmailPolicy.ts normalizes registration email domain restrictions
 * and formats their display summaries. Exact domains use a canonical leading
 * at-sign, while wildcard domains keep their star-dot prefix and match both
 * the base domain and its subdomains.
 *
 * Pasted input rejects tokens containing invalid characters, whereas
 * normalization of existing entries removes those characters before structural
 * validation. Deduplication preserves first occurrence order. The admission
 * helper normalizes the entire configured list before testing an email, so an
 * absent list and a list with no surviving valid entries both allow any email.
 * This module does not perform full email-address validation when restrictions
 * are empty.
 */

/**
 * Email suffix patterns distinguish pasted-token rejection from stored-value
 * cleanup, define exact and wildcard notation, and limit the displayed rule
 * summary.
 */
const EMAIL_SUFFIX_TOKEN_SPLIT_RE = /[\s,，]+/
const EMAIL_SUFFIX_INVALID_CHAR_RE = /[^a-z0-9.-]/g
const EMAIL_SUFFIX_INVALID_CHAR_CHECK_RE = /[^a-z0-9.-]/
const EMAIL_SUFFIX_PREFIX_RE = /^@+/
const EMAIL_SUFFIX_WILDCARD_PREFIX = '*.'
const EMAIL_SUFFIX_MESSAGE_VISIBLE_LIMIT = 5
const EMAIL_SUFFIX_DOMAIN_PATTERN =
  /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/

/**
 * normalizeRegistrationEmailSuffixDomain trims and lowercases an existing
 * domain entry before removing invalid characters. Leading at-signs are
 * removed, wildcard prefixes are retained, and structural validation remains a
 * separate operation for callers.
 */
export function normalizeRegistrationEmailSuffixDomain(raw: string): string {
  let value = String(raw || '')
    .trim()
    .toLowerCase()
  if (!value) {
    return ''
  }

  value = value.replace(EMAIL_SUFFIX_PREFIX_RE, '')
  return normalizeRegistrationEmailSuffixToken(value, false)
}

/**
 * normalizeRegistrationEmailSuffixDomains returns valid unique normalized
 * entries in first-occurrence order. Missing lists become empty, and invalid
 * or duplicate entries are discarded before canonical at-sign prefixes are
 * added elsewhere.
 */
export function normalizeRegistrationEmailSuffixDomains(
  items: string[] | null | undefined,
): string[] {
  if (!items || items.length === 0) {
    return []
  }

  const seen = new Set<string>()
  const normalized: string[] = []
  for (const item of items) {
    const domain = normalizeRegistrationEmailSuffixDomain(item)
    if (!isRegistrationEmailSuffixDomainValid(domain) || seen.has(domain)) {
      continue
    }
    seen.add(domain)
    normalized.push(domain)
  }
  return normalized
}

/**
 * parseRegistrationEmailSuffixWhitelistInput splits pasted text on whitespace
 * and supported commas. Tokens containing invalid characters or invalid domain
 * structure are discarded entirely, and valid normalized duplicates retain
 * their first occurrence only.
 */
export function parseRegistrationEmailSuffixWhitelistInput(input: string): string[] {
  if (!input || !input.trim()) {
    return []
  }

  const seen = new Set<string>()
  const normalized: string[] = []

  for (const token of input.split(EMAIL_SUFFIX_TOKEN_SPLIT_RE)) {
    const domain = normalizeRegistrationEmailSuffixDomainStrict(token)
    if (!isRegistrationEmailSuffixDomainValid(domain) || seen.has(domain)) {
      continue
    }
    seen.add(domain)
    normalized.push(domain)
  }

  return normalized
}

/**
 * normalizeRegistrationEmailSuffixWhitelist returns unique valid domains in
 * their stored canonical form. Exact domains receive a leading at-sign,
 * wildcard prefixes remain unchanged, and missing or wholly invalid lists
 * become empty.
 */
export function normalizeRegistrationEmailSuffixWhitelist(
  items: string[] | null | undefined,
): string[] {
  return normalizeRegistrationEmailSuffixDomains(items).map(toCanonicalRegistrationEmailSuffix)
}

/**
 * extractRegistrationEmailDomain extracts a lowercase domain from an address
 * containing exactly one at-sign. Empty local or domain portions return empty;
 * this limited extraction does not validate all email syntax.
 */
function extractRegistrationEmailDomain(email: string): string {
  const raw = String(email || '')
    .trim()
    .toLowerCase()
  if (!raw) {
    return ''
  }
  const atIndex = raw.indexOf('@')
  if (atIndex <= 0 || atIndex >= raw.length - 1) {
    return ''
  }
  if (raw.indexOf('@', atIndex + 1) !== -1) {
    return ''
  }
  return raw.slice(atIndex + 1)
}

/**
 * isRegistrationEmailSuffixAllowed evaluates an email against normalized exact
 * and wildcard domain restrictions. A normalized empty list allows every
 * input, including when all supplied rules were discarded as invalid.
 *
 * With surviving restrictions, the address must yield a nonempty domain. Exact
 * rules compare the full domain, while wildcard rules accept the base itself
 * or a suffix separated by a dot. That separator prevents a similarly named
 * unrelated domain from matching. Callers perform any additional email
 * validation independently of this restriction check.
 */
export function isRegistrationEmailSuffixAllowed(
  email: string,
  whitelist: string[] | null | undefined,
): boolean {
  const normalizedWhitelist = normalizeRegistrationEmailSuffixWhitelist(whitelist)
  /**
   * Admission uses the surviving normalized list, so an absent configuration and
   * a wholly invalid configuration both follow the unrestricted path implemented
   * here.
   */
  if (normalizedWhitelist.length === 0) {
    return true
  }
  const emailDomain = extractRegistrationEmailDomain(email)
  if (!emailDomain) {
    return false
  }
  const emailSuffix = `@${emailDomain}`
  return normalizedWhitelist.some((allowed) => {
    if (allowed.startsWith('@')) {
      return allowed === emailSuffix
    }
    if (allowed.startsWith(EMAIL_SUFFIX_WILDCARD_PREFIX)) {
      const base = allowed.slice(EMAIL_SUFFIX_WILDCARD_PREFIX.length)
      return emailDomain === base || emailDomain.endsWith(`.${base}`)
    }
    return false
  })
}

/**
 * formatRegistrationEmailSuffixWhitelistForMessage joins up to five normalized
 * rules for display. Additional rules become one caller-formatted count entry,
 * and the supplied separator controls both ordinary entries and that final
 * summary.
 */
export function formatRegistrationEmailSuffixWhitelistForMessage(
  whitelist: string[] | null | undefined,
  options: {
    separator: string // display entry separator
    more: (count: number) => string // hidden-count message formatter
  },
): string {
  const normalizedWhitelist = normalizeRegistrationEmailSuffixWhitelist(whitelist)
  /**
   * Limit the displayed list before appending the hidden-count message.
   */
  const visible = normalizedWhitelist.slice(0, EMAIL_SUFFIX_MESSAGE_VISIBLE_LIMIT)
  const hiddenCount = normalizedWhitelist.length - visible.length
  if (hiddenCount > 0) {
    visible.push(options.more(hiddenCount))
  }
  return visible.join(options.separator)
}

/**
 * normalizeRegistrationEmailSuffixDomainStrict trims and lowercases pasted
 * input before strict token normalization. Invalid characters cause an empty
 * result rather than repair, while final domain structure is checked by the
 * parser caller.
 */
function normalizeRegistrationEmailSuffixDomainStrict(raw: string): string {
  let value = String(raw || '')
    .trim()
    .toLowerCase()
  if (!value) {
    return ''
  }
  value = value.replace(EMAIL_SUFFIX_PREFIX_RE, '')
  return normalizeRegistrationEmailSuffixToken(value, true)
}

/**
 * isRegistrationEmailSuffixDomainValid checks domain structure with an
 * optional leading wildcard prefix. Bare stars, single labels, misplaced
 * stars, and malformed label boundaries fail; accepted values remain
 * unmodified for the caller.
 */
export function isRegistrationEmailSuffixDomainValid(domain: string): boolean {
  if (!domain) {
    return false
  }
  if (domain.startsWith(EMAIL_SUFFIX_WILDCARD_PREFIX)) {
    return EMAIL_SUFFIX_DOMAIN_PATTERN.test(domain.slice(EMAIL_SUFFIX_WILDCARD_PREFIX.length))
  }
  return !domain.includes('*') && EMAIL_SUFFIX_DOMAIN_PATTERN.test(domain)
}

/**
 * normalizeRegistrationEmailSuffixToken normalizes exact or wildcard domain
 * text according to strictness. Strict mode rejects invalid characters, while
 * permissive mode removes them; neither path replaces the caller structural
 * domain check.
 */
function normalizeRegistrationEmailSuffixToken(value: string, strict: boolean): string {
  if (value.startsWith(EMAIL_SUFFIX_WILDCARD_PREFIX)) {
    const domain = value.slice(EMAIL_SUFFIX_WILDCARD_PREFIX.length)
    if (strict && (!domain || EMAIL_SUFFIX_INVALID_CHAR_CHECK_RE.test(domain))) {
      return ''
    }
    /**
     * Preserve the wildcard prefix separately from domain cleanup so permissive
     * normalization does not turn a wildcard rule into an exact-domain rule.
     */
    return `${EMAIL_SUFFIX_WILDCARD_PREFIX}${domain.replace(EMAIL_SUFFIX_INVALID_CHAR_RE, '')}`
  }

  if (value === '*') {
    return strict ? '' : value
  }

  if (strict && EMAIL_SUFFIX_INVALID_CHAR_CHECK_RE.test(value)) {
    return ''
  }
  return value.replace(/[*]/g, '').replace(EMAIL_SUFFIX_INVALID_CHAR_RE, '')
}

/**
 * toCanonicalRegistrationEmailSuffix prefixes exact domains while retaining
 * wildcard domain notation.
 */
function toCanonicalRegistrationEmailSuffix(domain: string): string {
  return domain.startsWith(EMAIL_SUFFIX_WILDCARD_PREFIX) ? domain : `@${domain}`
}
