import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { nodes, parseVue, variableInitializer } from '@/__tests__/helpers/sourceSyntax'

// ProxiesView.ipv6.spec.ts exercises the URL expression used by proxy batch
// input. The test obtains the regular expression from its parsed declaration,
// then checks address and credential captures across bracketed IPv6, IPv4, and
// hostnames, including malformed inputs that must remain invalid.

// The initializer is selected inside parseProxyUrl so other expressions in the
// component cannot accidentally satisfy the proxy input format assertions below.
const parser = variableInitializer(parseVue('views/admin/ProxiesView.vue').script, 'parseProxyUrl')
const literal = variableInitializer(parser, 'regex')
if (!ts.isRegularExpressionLiteral(literal))
  throw new Error('Expected proxy URL regular expression')
const delimiter = literal.text.lastIndexOf('/')
const regex = new RegExp(literal.text.slice(1, delimiter), literal.text.slice(delimiter + 1))

describe('proxy batch URL parsing with IPv6', () => {
  // Valid proxy URLs include bracketed IPv6 addresses, IPv4 addresses, and hostnames.
  // Ambiguous bare IPv6 addresses and unsupported protocol or port syntax are rejected.
  it.each([
    ['socks5://[2001:db8::1]:1080', true],
    ['socks5h://[2001:db8::1]:1080', true],
    ['http://[::1]:8080', true],
    ['socks5://user:pass@[2001:db8::1]:1080', true],
    ['socks5://proxy.example.com:1080', true],
    ['http://192.168.1.1:8080', true],
    ['socks5://user:pass@proxy.example.com:1080', true],
    ['socks5://2001:db8::1:1080', false],
    ['ftp://example.com:21', false],
    ['socks5://example.com:port', false],
  ])('%s => %s', (line, expected) => {
    expect(regex.test(line)).toBe(expected)
  })

  // IPv6 brackets delimit the host while credential and port captures retain their positions.
  // The production host cleanup expression strips those brackets before the parsed proxy is submitted.
  it('captures credentials and removes IPv6 host brackets', () => {
    const match = 'socks5://user:pass@[2001:db8::1]:1080'.match(regex)!
    expect(match).not.toBeNull()
    const [, protocol, username, password, rawHost, port] = match
    expect({ protocol, username, password, rawHost, port }).toEqual({
      protocol: 'socks5', // captured transport protocol
      username: 'user', // captured authentication username
      password: 'pass', // captured authentication password
      rawHost: '[2001:db8::1]', // bracketed IPv6 host
      port: '1080', // captured decimal port
    })
    const cleanup = nodes(variableInitializer(parser, 'host'), ts.isRegularExpressionLiteral)[0]
    const end = cleanup.text.lastIndexOf('/')
    const brackets = new RegExp(cleanup.text.slice(1, end), cleanup.text.slice(end + 1))
    expect(rawHost.replace(brackets, '').trim()).toBe('2001:db8::1')
  })
})
