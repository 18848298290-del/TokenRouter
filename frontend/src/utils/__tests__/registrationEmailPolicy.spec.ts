import { describe, expect, it } from 'vitest'
import {
  formatRegistrationEmailSuffixWhitelistForMessage,
  isRegistrationEmailSuffixAllowed,
  isRegistrationEmailSuffixDomainValid,
  normalizeRegistrationEmailSuffixDomain,
  normalizeRegistrationEmailSuffixDomains,
  normalizeRegistrationEmailSuffixWhitelist,
  parseRegistrationEmailSuffixWhitelistInput,
} from '@/utils/registrationEmailPolicy'

/**
 * registrationEmailPolicy.spec.ts checks domain normalization, pasted input,
 * admission rules, and display summaries for registration restrictions. Cases
 * distinguish exact domains from wildcard domains and compare permissive
 * stored-value cleanup with strict pasted-token rejection while preserving
 * canonical ordering and deduplication.
 */

describe('registrationEmailPolicy utils', () => {
  /**
   * Existing entries include uppercase letters, leading at-signs, and invalid
   * characters. Permissive normalization must lowercase and remove invalid
   * characters while preserving a valid wildcard prefix.
   */
  it('normalizeRegistrationEmailSuffixDomain lowercases, strips @, and ignores invalid chars', () => {
    expect(normalizeRegistrationEmailSuffixDomain(' @Exa!mple.COM ')).toBe('example.com')
    expect(normalizeRegistrationEmailSuffixDomain(' *.EDU!.CN ')).toBe('*.edu.cn')
  })

  /**
   * Configured entries include normalized duplicates and structurally invalid
   * domains. Normalization must retain valid first occurrences in order and
   * discard duplicates after their spelling is normalized.
   */
  it('normalizeRegistrationEmailSuffixDomains deduplicates normalized domains', () => {
    expect(
      normalizeRegistrationEmailSuffixDomains([
        '@example.com',
        'Example.com',
        '',
        '-invalid.com',
        'foo..bar.com',
        ' @foo.bar ',
        '@foo.bar',
        '*.EDU.CN',
        '*.edu.cn',
      ]),
    ).toEqual(['example.com', 'foo.bar', '*.edu.cn'])
  })

  /**
   * Pasted text mixes whitespace, ASCII commas, and full-width commas. The
   * parser must split those separators and deduplicate valid normalized exact
   * and wildcard domains.
   */
  it('parseRegistrationEmailSuffixWhitelistInput supports separators and deduplicates', () => {
    const input = '\n  @example.com,example.com，@foo.bar\t@FOO.bar *.EDU.CN  '
    expect(parseRegistrationEmailSuffixWhitelistInput(input)).toEqual([
      'example.com',
      'foo.bar',
      '*.edu.cn',
    ])
  })

  /**
   * Some pasted tokens contain punctuation that is invalid within domain text.
   * Strict parsing must discard those entire tokens while retaining the
   * unaffected valid neighboring domains.
   */
  it('parseRegistrationEmailSuffixWhitelistInput drops tokens containing invalid chars', () => {
    const input = '@exa!mple.com, @foo.bar, @bad#token.com, @ok-domain.com'
    expect(parseRegistrationEmailSuffixWhitelistInput(input)).toEqual(['foo.bar', 'ok-domain.com'])
  })

  /**
   * Pasted tokens include invalid label boundaries and incomplete wildcard
   * forms. The parser must discard structurally invalid domains while retaining
   * valid ordinary and punycode-style labels.
   */
  it('parseRegistrationEmailSuffixWhitelistInput drops structurally invalid domains', () => {
    const input = '@-bad.com, @foo..bar.com, @foo.bar, @xn--ok.com, *., *, *.@, *.foo'
    expect(parseRegistrationEmailSuffixWhitelistInput(input)).toEqual(['foo.bar', 'xn--ok.com'])
  })

  /**
   * The pasted input contains only spaces and line breaks. Parsing must return
   * an empty rule list instead of introducing a blank or malformed domain entry.
   */
  it('parseRegistrationEmailSuffixWhitelistInput returns empty list for blank input', () => {
    expect(parseRegistrationEmailSuffixWhitelistInput('   \n \n')).toEqual([])
  })

  /**
   * Configured exact and wildcard entries require their canonical stored
   * notation. Exact domains must receive an at-sign prefix, while wildcard
   * prefixes and first-occurrence ordering remain intact.
   */
  it('normalizeRegistrationEmailSuffixWhitelist returns canonical @domain list', () => {
    expect(
      normalizeRegistrationEmailSuffixWhitelist([
        '@Example.com',
        'foo.bar',
        '',
        '-invalid.com',
        ' @foo.bar ',
        '*.EDU.CN',
      ]),
    ).toEqual(['@example.com', '@foo.bar', '*.edu.cn'])
  })

  /**
   * Domain fixtures cover label separators, hyphens, wildcards, and single-label
   * names. Validation must accept the supported structures and reject malformed
   * labels or misplaced wildcard notation.
   */
  it('isRegistrationEmailSuffixDomainValid matches backend-compatible domain rules', () => {
    expect(isRegistrationEmailSuffixDomainValid('example.com')).toBe(true)
    expect(isRegistrationEmailSuffixDomainValid('foo-bar.example.com')).toBe(true)
    expect(isRegistrationEmailSuffixDomainValid('*.edu.cn')).toBe(true)
    expect(isRegistrationEmailSuffixDomainValid('-bad.com')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('foo..bar.com')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('localhost')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('*.foo')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('*')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('*.@')).toBe(false)
  })

  /**
   * The configured whitelist contains no restrictions for the supplied email.
   * The admission helper must return true rather than treating an empty list as
   * a denial rule.
   */
  it('isRegistrationEmailSuffixAllowed allows any email when whitelist is empty', () => {
    expect(isRegistrationEmailSuffixAllowed('user@example.com', [])).toBe(true)
  })

  /**
   * Exact domain restrictions are compared with base-domain and subdomain
   * addresses. Only the full matching domain may qualify, preventing exact rules
   * from silently permitting all subdomains.
   */
  it('isRegistrationEmailSuffixAllowed applies exact suffix matching', () => {
    expect(isRegistrationEmailSuffixAllowed('user@example.com', ['@example.com'])).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@sub.example.com', ['@example.com'])).toBe(false)
    expect(isRegistrationEmailSuffixAllowed('user@qq.com', ['@qq.com'])).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@sub.qq.com', ['@qq.com'])).toBe(false)
  })

  /**
   * A wildcard domain is compared with its base, a subdomain, and an unrelated
   * domain. Both base and subdomain must qualify while the unrelated domain
   * fails.
   */
  it('isRegistrationEmailSuffixAllowed applies wildcard suffix matching', () => {
    expect(isRegistrationEmailSuffixAllowed('student@cs.edu.cn', ['*.edu.cn'])).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('student@edu.cn', ['*.edu.cn'])).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('student@foo.cn', ['*.edu.cn'])).toBe(false)
  })

  /**
   * Exact and wildcard rules coexist in the same configured list. Admission must
   * accept either matching form and continue rejecting addresses outside both
   * rule sets.
   */
  it('isRegistrationEmailSuffixAllowed supports mixed exact and wildcard entries', () => {
    const whitelist = ['@a.com', '*.b.cn']
    expect(isRegistrationEmailSuffixAllowed('user@a.com', whitelist)).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@school.b.cn', whitelist)).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@b.cn', whitelist)).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@c.cn', whitelist)).toBe(false)
  })

  /**
   * Display summaries receive five rules and then more than five rules. The
   * first case lists all entries, while the second appends the caller-formatted
   * hidden count.
   */
  it('formatRegistrationEmailSuffixWhitelistForMessage lists up to five entries', () => {
    expect(
      formatRegistrationEmailSuffixWhitelistForMessage(
        ['@a.com', '@b.com', '@c.com', '@d.com', '@e.com'],
        { separator: ', ', more: (count) => `and ${count} more` },
      ),
    ).toBe('@a.com, @b.com, @c.com, @d.com, @e.com')
    expect(
      formatRegistrationEmailSuffixWhitelistForMessage(
        ['@a.com', '@b.com', '@c.com', '@d.com', '@e.com', '*.edu.cn', '@f.com'],
        { separator: ', ', more: (count) => `and ${count} more` },
      ),
    ).toBe('@a.com, @b.com, @c.com, @d.com, @e.com, and 2 more')
  })
})
