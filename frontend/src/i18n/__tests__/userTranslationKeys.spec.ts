import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import en from '../locales/en'
import zh from '../locales/zh'
import manifest from '../../../../backend/internal/pkg/locale/manifest.json'
import ts from 'typescript'
import { compileTemplate, parse } from 'vue/compiler-sfc'

// userTranslationKeys.spec.ts checks translation availability for user-facing
// pages and their local dependencies. Parsed module references select reachable
// source files, and parsed calls select literal keys from scripts and compiled
// Vue templates. Bilingual catalogs also share named interpolation inputs.

// SourceReferences records dependencies and literal translation keys from parsed source.
interface SourceReferences {
  imports: string[] // Referenced module specifiers.
  keys: string[] // Literal translation arguments.
}

// sourceReferences extracts imports and literal translation calls from one source.
// Vue scripts and compiled template expressions share the TypeScript visitor, so
// quoted examples and comments remain data. Vue parse or compilation errors fail
// the test immediately so malformed components remain visible as failures.
function sourceReferences(source: string, filename: string): SourceReferences {
  const result: SourceReferences = { imports: [], keys: [] }
  const scripts: string[] = []
  if (filename.endsWith('.vue')) {
    const { descriptor, errors } = parse(source, { filename })
    if (errors.length) throw new Error(`Invalid Vue source ${filename}: ${errors.join(', ')}`)
    for (const block of [descriptor.script, descriptor.scriptSetup]) {
      if (block) scripts.push(block.content)
    }
    if (descriptor.template) {
      // Compile directives and interpolations before visiting calls, so Vue
      // expression syntax and plain template text retain their distinct meanings.
      const template = compileTemplate({
        source: descriptor.template.content,
        filename,
        id: filename,
        compilerOptions: { expressionPlugins: ['typescript'] },
      })
      if (template.errors.length)
        throw new Error(`Invalid Vue template ${filename}: ${template.errors.join(', ')}`)
      scripts.push(template.code)
    }
  } else {
    scripts.push(source)
  }

  // visit records module references and dotted literal keys in syntax order.
  // Identifier and property calls cover script helpers and Vue's generated
  // context accesses; dynamic arguments remain outside this literal-key check.
  function visit(node: ts.Node): void {
    if (
      (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) &&
      node.moduleSpecifier &&
      ts.isStringLiteral(node.moduleSpecifier)
    ) {
      result.imports.push(node.moduleSpecifier.text)
    }
    if (ts.isCallExpression(node)) {
      const argument = node.arguments[0]
      if (argument && ts.isStringLiteralLike(argument)) {
        if (node.expression.kind === ts.SyntaxKind.ImportKeyword) {
          result.imports.push(argument.text)
        } else {
          const name = ts.isIdentifier(node.expression)
            ? node.expression.text
            : ts.isPropertyAccessExpression(node.expression)
              ? node.expression.name.text
              : ''
          if ((name === 't' || name === '$t') && /^[A-Za-z]\w*(?:\.\w+)+$/.test(argument.text))
            result.keys.push(argument.text)
        }
      }
    }
    ts.forEachChild(node, visit)
  }
  for (const script of scripts)
    visit(ts.createSourceFile(filename, script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS))
  return result
}

// root locates frontend source files when the suite is launched from either the
// repository or frontend directory.
const root = existsSync(resolve(process.cwd(), 'src/views'))
  ? resolve(process.cwd(), 'src')
  : resolve(process.cwd(), 'frontend/src')
// filesUnder recursively lists TypeScript and Vue sources beneath the supplied
// directory. Test directories and test files are excluded so resource checks
// follow production consumers; filesystem errors propagate to the test.
function filesUnder(path: string): string[] {
  return readdirSync(path, { withFileTypes: true }).flatMap((entry) => {
    if (entry.name === '__tests__') return []
    const file = join(path, entry.name)
    return entry.isDirectory()
      ? filesUnder(file)
      : /\.(vue|ts)$/.test(file) && !/\.(spec|test)\.ts$/.test(file)
        ? [file]
        : []
  })
}

// userSources finds user-facing entry pages and the local modules reachable
// through their parsed module references. It skips catalog and router files,
// resolves known source extensions, deduplicates paths, and returns sorted filenames for
// deterministic diagnostics.
function userSources(): string[] {
  const queue = [
    ...['views/user', 'views/public', 'views/auth'].flatMap((path) => filesUnder(join(root, path))),
    ...[
      'views/HomeView.vue',
      'views/ModelMarketplaceView.vue',
      'views/KeyUsageView.vue',
      'views/NotFoundView.vue',
      'components/common/LocalizedEditor.vue',
      'components/common/LocalizedFieldsEditor.vue',
    ].map((path) => join(root, path)),
  ]
  const visited = new Set<string>()
  while (queue.length) {
    const file = queue.pop()!
    if (visited.has(file) || file.includes('/i18n/locales/') || file.includes('/router/')) continue
    visited.add(file)
    const source = readFileSync(file, 'utf8')
    for (const reference of sourceReferences(source, file).imports) {
      if (!reference.startsWith('@/') && !reference.startsWith('.')) continue
      const base = reference.startsWith('@/')
        ? join(root, reference.slice(2))
        : resolve(dirname(file), reference)
      // Try the supported source filenames in import resolution order so
      // extensionless references reach their actual TypeScript or Vue module
      // before traversal continues.
      const target = [base, `${base}.ts`, `${base}.vue`, join(base, 'index.ts')].find(
        (candidate) => /\.(vue|ts)$/.test(candidate) && existsSync(candidate),
      )
      if (target && target.startsWith(root)) queue.push(target)
    }
  }
  return [...visited].sort()
}

// hasKey reports whether a dotted catalog path resolves to a string value.
// Missing segments and non-object intermediate values return false, allowing the
// caller to report unresolved translation calls.
function hasKey(value: unknown, key: string): boolean {
  let current = value
  for (const segment of key.split('.')) {
    if (!current || typeof current !== 'object' || !(segment in current)) return false
    current = (current as Record<string, unknown>)[segment]
  }
  return typeof current === 'string'
}

// flatStrings collects nested string values under dotted paths into the supplied
// result object. Optional arguments start a fresh root traversal, while
// recursive calls share the accumulator and skip non-string primitive values.
function flatStrings(
  value: unknown,
  prefix = '',
  result: Record<string, string> = {},
): Record<string, string> {
  if (typeof value === 'string') result[prefix] = value
  else if (value && typeof value === 'object') {
    for (const [key, item] of Object.entries(value))
      flatStrings(item, prefix ? `${prefix}.${key}` : key, result)
  }
  return result
}

describe('translation source parsing', () => {
  // Real module declarations and multiline calls appear beside quoted and
  // commented lookalikes. Collection retains executable references and literal
  // keys while excluding examples and dynamic translation arguments.
  it('collects TypeScript module references and calls from syntax', () => {
    const references = sourceReferences(
      `
      import value from '@/real/static'
      import '@/real/side-effect'
      export { value } from './real/export'
      const lazy = import('./real/dynamic')
      // import ignored from '@/fake/comment'; t('fake.comment')
      const example = "import ignored from '@/fake/string'; t('fake.string')"
      t /* formatting */ (
        'real.multiline'
      )
      i18n.t('real.member')
      $t('real.dollar')
      t(dynamicKey)
    `,
      'fixture.ts',
    )
    expect(references.imports).toEqual([
      '@/real/static',
      '@/real/side-effect',
      './real/export',
      './real/dynamic',
    ])
    expect(references.keys).toEqual(['real.multiline', 'real.member', 'real.dollar'])
  })

  // Vue directives and interpolations contain executable translations beside
  // literal attributes, text, and comments. Both script blocks and template
  // calls contribute keys while those literal examples stay excluded.
  it('collects Vue script and template calls while ignoring lookalikes', () => {
    const references = sourceReferences(
      `
      <script lang="ts">
        import regular from './real/regular'
        const label = t('real.regular')
      </script>
      <script setup lang="ts">
        import setup from './real/setup'
        const title = t('real.setup')
        const example = "t('fake.script')"
      </script>
      <template>
        <!-- t('fake.comment') import fake from './fake/comment' -->
        <div :title="t('real.title')" @click="$t('real.event')" title="t('fake.attribute')">
          {{ $t('real.interpolation') }}
          {{ "t('fake.quoted')" }}
          t('fake.text')
        </div>
      </template>
    `,
      'Fixture.vue',
    )
    expect(references.imports.filter((reference) => reference.startsWith('.'))).toEqual([
      './real/regular',
      './real/setup',
    ])
    expect(references.keys.sort()).toEqual([
      'real.event',
      'real.interpolation',
      'real.regular',
      'real.setup',
      'real.title',
    ])
  })
})

describe('user translation resources', () => {
  // User-facing pages and reachable local modules call translations with literal
  // keys. Both runtime catalogs resolve every collected key to a string,
  // reporting missing paths with their source file.
  it('resolves literal keys referenced by user pages and their dependencies', () => {
    const missing = new Set<string>()
    for (const file of userSources()) {
      const source = readFileSync(file, 'utf8')
      for (const key of sourceReferences(source, file).keys) {
        for (const [code, messages] of [
          ['en', en],
          ['zh-Hans', zh],
        ] as const) {
          if (!hasKey(messages, key)) missing.add(`${code}: ${key} (${file.slice(root.length)})`)
        }
      }
    }
    expect([...missing]).toEqual([])
  })

  // English and Chinese catalogs describe the same interface with matching
  // interpolation inputs. Their flattened keys and named placeholder sets agree
  // so language changes retain required variables.
  it('keeps keys and named placeholders consistent', () => {
    const english = flatStrings(en)
    const chinese = flatStrings(zh)
    expect(Object.keys(english).sort()).toEqual(Object.keys(chinese).sort())
    // variables extracts unique named interpolation placeholders and returns
    // them in sorted order.
    const variables = (message: string) =>
      [...new Set([...message.matchAll(/\{([a-zA-Z_]\w*)\}/g)].map((match) => match[1]))].sort()
    const mismatches = Object.keys(english).filter(
      (key) => JSON.stringify(variables(english[key])) !== JSON.stringify(variables(chinese[key])),
    )
    expect(mismatches).toEqual([])
  })

  // The shared production manifest maps each language to a catalog directory.
  // Every mapped directory contains an entry file so the frontend can discover
  // the catalog for loading.
  it('has a loadable catalog for every production language', () => {
    for (const language of manifest.locales)
      expect(existsSync(join(root, 'i18n/locales', language.catalog, 'index.ts'))).toBe(true)
  })
})
