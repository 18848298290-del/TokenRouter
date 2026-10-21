import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import {
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createRequire } from 'node:module'
import test from 'node:test'
import { format, resolveConfig } from 'prettier'
import { parse } from 'vue/compiler-sfc'
import { frontendRoot } from './source-analysis.mjs'

// format.test.mjs verifies the frontend formatter's checked configuration and
// repository exclusions in temporary directories. Check mode must leave input
// bytes unchanged, while write mode must retain string payloads and produce a
// stable second result for supported handwritten frontend source formats.

// prettierCLI locates the installed pinned executable for subprocess fixtures;
// invoking it through process.execPath uses the test runner's selected Node version.
const prettierCLI = createRequire(import.meta.url).resolve('prettier/bin/prettier.cjs')

// Vue formatting changes layout while preserving embedded source strings and imports.
// Repeating formatting returns identical output under the checked project configuration.
test('formatting is idempotent and preserves embedded payload and import order', async () => {
  const source = `<script setup lang="ts">import 'z-first'; import 'a-second'; const payload = /* html */ \`<div   class="x" >text</div>\`; const item={first:1};</script><template><div>{{ payload }}</div></template><style>.sample{color:red}</style>`
  const options = { ...(await resolveConfig(join(frontendRoot, 'src/fixture.vue'))), parser: 'vue' }
  const formatted = await format(source, options)
  assert.equal(await format(formatted, options), formatted)
  assert.ok(formatted.includes('<div   class="x" >text</div>'))
  assert.ok(formatted.indexOf("import 'z-first'") < formatted.indexOf("import 'a-second'"))
  assert.ok(formatted.includes('const item = { first: 1 }'))
  assert.ok(formatted.includes('.sample {\n  color: red;\n}'))
  assert.ok(!formatted.includes("import 'z-first';"))
})

// Vue uses literal text boundaries for labels inside adjacent template elements.
// Its override retains those boundaries while the HTML default remains CSS-aware.
test('Vue formatting retains compact label boundaries under the project override', async () => {
  const options = { ...(await resolveConfig(join(frontendRoot, 'src/fixture.vue'))), parser: 'vue' }
  const source =
    '<template><label class="long-descriptive-label-with-a-deliberately-wide-attribute-value">ID</label></template>'
  const output = await format(source, options)
  assert.equal(options.htmlWhitespaceSensitivity, 'strict')
  assert.match(output, />ID<\/label/)
})

// Vue identifies multi-statement event handlers by their semicolon separators.
// Formatting retains that syntax while ordinary script declarations remain semicolon-free.
test('Vue event statements retain required separators after formatting', async () => {
  const options = { ...(await resolveConfig(join(frontendRoot, 'src/fixture.vue'))), parser: 'vue' }
  const source =
    '<template><button @click="opened = false; selected = null" @focus="opened = true; if (selected) { selected = null }">Open</button></template>'
  const output = await format(source, options)
  assert.deepEqual(parse(output).errors, [])
  assert.ok(output.includes('opened = false;'))
  assert.ok(output.includes('selected = null;'))
  assert.equal(await format(output, options), output)
})

// A temporary frontend contains malformed source and excluded asset and lock files.
// Check mode reports the source without writing; write mode repairs only included files.
test('CLI check mode is read-only and write mode respects repository exclusions', (context) => {
  const directory = mkdtempSync(join(tmpdir(), 'tokenrouter-format-'))
  context.after(() => rmSync(directory, { recursive: true, force: true }))
  copyFileSync(join(frontendRoot, '.prettierignore'), join(directory, '.prettierignore'))
  copyFileSync(join(frontendRoot, 'prettier.config.mjs'), join(directory, 'prettier.config.mjs'))
  symlinkSync(join(frontendRoot, 'scripts'), join(directory, 'scripts'))
  mkdirSync(join(directory, 'src/assets'), { recursive: true })
  const files = new Map([
    ['src/example.ts', 'const value={first:1,second:2};'],
    ['src/assets/data.json', '{ "unterminated":'],
    ['pnpm-lock.yaml', 'invalid: ['],
    ['vite.config.js', 'const ='],
    ['src/handwritten.d.ts', 'declare const value:{first:number};'],
  ])
  files.set('public/resource.js', 'const =')
  files.set(
    'src/views/public/PublicView.vue',
    '<template><div>{{ value }}</div></template><script setup>const value={a:1};</script>',
  )
  files.set('src/config/vite.config.js', 'export const nested={a:1};')
  mkdirSync(join(directory, 'public'), { recursive: true })
  mkdirSync(join(directory, 'src/views/public'), { recursive: true })
  mkdirSync(join(directory, 'src/config'), { recursive: true })
  for (const [file, source] of files) writeFileSync(join(directory, file), source)
  const check = spawnSync(
    process.execPath,
    [prettierCLI, '--check', '**/*.{vue,ts,js,json,yaml}'],
    {
      cwd: directory,
      encoding: 'utf8',
    },
  )
  assert.equal(check.status, 1, check.stderr)
  for (const [file, source] of files)
    assert.equal(readFileSync(join(directory, file), 'utf8'), source)
  const write = spawnSync(
    process.execPath,
    [prettierCLI, '--write', '**/*.{vue,ts,js,json,yaml}'],
    {
      cwd: directory,
      encoding: 'utf8',
    },
  )
  assert.equal(write.status, 0, write.stderr)
  assert.notEqual(
    readFileSync(join(directory, 'src/example.ts'), 'utf8'),
    files.get('src/example.ts'),
  )
  assert.notEqual(
    readFileSync(join(directory, 'src/handwritten.d.ts'), 'utf8'),
    files.get('src/handwritten.d.ts'),
  )
  for (const file of ['src/views/public/PublicView.vue', 'src/config/vite.config.js']) {
    assert.notEqual(readFileSync(join(directory, file), 'utf8'), files.get(file))
  }
  for (const file of [
    'src/assets/data.json',
    'pnpm-lock.yaml',
    'vite.config.js',
    'public/resource.js',
  ]) {
    assert.equal(readFileSync(join(directory, file), 'utf8'), files.get(file))
  }
})
