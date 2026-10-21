import { describe, expect, it } from 'vitest'
import { baseCompile } from '@intlify/message-compiler'

import en from '../locales/en'
import zh from '../locales/zh'

// localesMessageCompile.spec.ts compiles every string in the English and Chinese
// message catalogs with the Vue I18n compiler. Recursive traversal records
// message paths so malformed placeholders fail this test before a rendered
// component encounters the invalid message.

// collectCompileErrors walks nested catalog values and appends compiler failures
// with their message paths. Arrays retain numeric path segments, non-string
// scalar values are skipped, and the supplied output array accumulates all
// discovered errors.
function collectCompileErrors(node: unknown, path: string, out: string[]): void {
  if (typeof node === 'string') {
    // Collect invalid placeholders with their catalog paths so traversal can
    // report failures from subsequent messages during the same catalog check.
    baseCompile(node, {
      onError: (err) => {
        out.push(`${path}: ${err.message}`)
      },
    })
    return
  }
  if (Array.isArray(node)) {
    node.forEach((item, index) => collectCompileErrors(item, `${path}[${index}]`, out))
    return
  }
  if (node && typeof node === 'object') {
    for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
      collectCompileErrors(value, path ? `${path}.${key}` : key, out)
    }
  }
}

describe('locale messages compile', () => {
  // Each catalog supplies nested strings that Vue I18n compiles when used.
  // Compiling every string collects no placeholder errors, protecting messages
  // containing literal braces and interpolation syntax.
  it.each([
    ['zh', zh],
    ['en', en],
  ] as const)('%s messages all compile without placeholder errors', (locale, messages) => {
    const errors: string[] = []
    collectCompileErrors(messages, locale, errors)
    expect(errors).toEqual([])
  })
})
