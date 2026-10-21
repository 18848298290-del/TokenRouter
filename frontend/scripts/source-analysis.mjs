import { readdirSync, readFileSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import * as vueParser from 'vue-eslint-parser'
import tseslint from 'typescript-eslint'
import postcss from 'postcss'

// source-analysis.mjs parses frontend files into comments and source ranges used
// by repository checks. Vue components contain three languages, so each block
// uses its language parser and retains offsets into the complete file. Checks
// can then distinguish comment prose from strings and attach a local exception
// to the syntax that owns it, including syntax spread across several lines.
//
// HTML elements contribute their opening tags as exception ranges. Descendant
// elements remain independent, while JavaScript statements and CSS declarations
// retain their complete parsed ranges. Parse failures propagate to the command
// using this module because skipping a malformed block would hide violations.

// frontendRoot and SOURCE_EXTENSIONS delimit handwritten languages understood by
// the parsers; data and dependency artifacts use their own validation commands.
export const frontendRoot = fileURLToPath(new URL('..', import.meta.url))
export const SOURCE_EXTENSIONS = new Set([
  '.vue',
  '.ts',
  '.tsx',
  '.js',
  '.jsx',
  '.mjs',
  '.cjs',
  '.css',
  '.html',
])
const EXCLUDED_DIRECTORIES = new Set([
  'node_modules',
  'dist',
  'coverage',
  '.cache',
  '.vite',
  '.vitest',
])
const GENERATED_FILES = new Set(['vite.config.js', 'vite.config.d.ts'])

/**
 * collectSourceFiles returns sorted handwritten source paths below a directory.
 * Symbolic links, compiler products, caches, and bundled public resources are
 * excluded. Callers may pass a smaller directory to limit a check's scope.
 * resourceRoot identifies root-only resources and compiler output, so a handwritten
 * nested public folder or same-named configuration module remains in scope.
 */
export function collectSourceFiles(directory = frontendRoot, resourceRoot = frontendRoot) {
  const files = []
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (entry.isSymbolicLink() || EXCLUDED_DIRECTORIES.has(entry.name)) continue
    const path = join(directory, entry.name)
    if (path === join(resourceRoot, 'public')) continue
    if (entry.isDirectory()) files.push(...collectSourceFiles(path, resourceRoot))
    else if (
      SOURCE_EXTENSIONS.has(extname(path)) &&
      !GENERATED_FILES.has(relative(resourceRoot, path))
    )
      files.push(path)
  }
  return files.sort()
}

/** walkSyntax visits parser nodes while avoiding their parent back references. */
export function walkSyntax(node, visit) {
  if (!node || typeof node !== 'object') return
  if (typeof node.type === 'string') visit(node)
  for (const [key, value] of Object.entries(node)) {
    if (
      [
        'parent',
        'loc',
        'range',
        'tokens',
        'comments',
        'errors',
        'references',
        'variables',
      ].includes(key)
    )
      continue
    if (Array.isArray(value)) value.forEach((child) => walkSyntax(child, visit))
    else if (value && typeof value === 'object') walkSyntax(value, visit)
  }
}

/**
 * parseScript collects comments, statements, and static module references.
 * The supplied offset locates a Vue or HTML script within its containing file;
 * all returned ranges address that complete file, including nested statements.
 */
function parseScript(text, offset, filename, result) {
  const { ast } = tseslint.parser.parseForESLint(text, {
    sourceType: 'module',
    filePath: filename,
    range: true,
    loc: true,
    comment: true,
    ecmaFeatures: { jsx: /\.[jt]sx$/.test(filename) },
  })
  for (const comment of ast.comments ?? []) {
    result.comments.push({
      ...comment,
      range: comment.range.map((value) => value + offset),
      kind: 'script',
    })
  }
  walkSyntax(ast, (node) => {
    if (node.type.endsWith('Statement') || node.type.endsWith('Declaration')) {
      result.units.push({ kind: 'script', range: node.range.map((value) => value + offset) })
    }
    const source =
      node.source ??
      (node.type === 'TSImportType' ? node.argument : undefined) ??
      (node.type === 'TSExternalModuleReference' ? node.expression : undefined) ??
      (node.type === 'CallExpression' &&
      node.callee.type === 'Identifier' &&
      node.callee.name === 'require'
        ? node.arguments[0]
        : undefined)
    const value = source?.value ?? source?.literal?.value
    if (typeof value === 'string') result.imports.push({ value, offset: node.range[0] + offset })
  })
}

/**
 * parseStyle records CSS comment and declaration ranges in their containing file.
 * At-rule parameters also form a unit because Tailwind's apply directives carry
 * utility classes. Container rules themselves never exempt every nested declaration.
 */
function parseStyle(text, offset, filename, result) {
  const root = postcss.parse(text, { from: filename })
  root.walk((node) => {
    // PostCSS offsets already use an exclusive end, so extending the range would
    // mask the first following source character when a comment touches a declaration.
    const range = [node.source.start.offset + offset, node.source.end.offset + offset]
    if (node.type === 'comment') result.comments.push({ value: node.text, range, kind: 'style' })
    else if (node.type === 'decl' || (node.type === 'atrule' && !node.nodes)) {
      result.units.push({ kind: 'style', range })
    }
  })
}

/**
 * parseSource returns actual comments, exception units, and static imports.
 * It accepts JavaScript, TypeScript, CSS, HTML, and Vue source. Parser errors
 * propagate to callers so a malformed source block cannot silently pass checks.
 *
 * Vue and HTML first supply a document fragment whose ranges address the full
 * input. Script and style blocks are parsed separately with their opening-tag
 * offsets added back. HTML exception units cover opening tags, making a parent
 * exception independent from classes on descendants. Script statements retain
 * nested ranges so the smallest owning statement determines an inline exception.
 */
export function parseSource(text, filename) {
  const result = { comments: [], units: [], imports: [] }
  const extension = extname(filename)
  if (extension === '.css') parseStyle(text, 0, filename, result)
  else if (extension === '.vue' || extension === '.html') {
    const parsed = vueParser.parseForESLint(text, {
      parser: tseslint.parser,
      sourceType: 'module',
      filePath: filename,
      range: true,
      loc: true,
    })
    const fragment = parsed.services.getDocumentFragment()
    // Vue accepts HTML extensions such as self-closing component tags. Expression
    // syntax failures still abort because their missing tokens could conceal comments.
    const expressionError = fragment.errors.find((error) => !error.code)
    if (expressionError) throw expressionError
    for (const comment of fragment.comments) result.comments.push({ ...comment, kind: 'html' })
    walkSyntax(fragment, (node) => {
      if (node.type !== 'VElement') return
      if (node.name === 'script' || node.name === 'style') {
        const start = node.startTag.range[1]
        const end = node.endTag?.range[0] ?? start
        if (node.name === 'style') parseStyle(text.slice(start, end), start, filename, result)
        else {
          const language = node.startTag.attributes.find(
            (attribute) => attribute.key.name === 'lang',
          )?.value?.value
          const type = node.startTag.attributes.find((attribute) => attribute.key.name === 'type')
            ?.value?.value
          if (!type || ['module', 'text/javascript', 'application/javascript'].includes(type)) {
            parseScript(
              text.slice(start, end),
              start,
              `${filename}.${language === 'tsx' ? 'tsx' : 'ts'}`,
              result,
            )
          }
        }
      } else
        result.units.push({ kind: 'html', range: node.startTag.range, elementRange: node.range })
    })
  } else parseScript(text, 0, filename, result)
  result.comments.sort((left, right) => left.range[0] - right.range[0])
  return result
}

/**
 * maskComments replaces comment bytes with spaces while retaining newline offsets.
 * Rule matches therefore refer to the original source and cannot be triggered by
 * examples written in documentation or by the exception marker's own explanation.
 */
export function maskComments(text, comments) {
  let masked = text
  for (const comment of [...comments].sort((left, right) => right.range[0] - left.range[0])) {
    const [start, end] = comment.range
    masked =
      masked.slice(0, start) + masked.slice(start, end).replace(/[^\n\r]/g, ' ') + masked.slice(end)
  }
  return masked
}

/**
 * exceptionRanges binds each check-ui-allow comment to one parsed source unit.
 * Adjacent statements take precedence over enclosing function and block ranges.
 * Comments inside an expression select its smallest enclosing statement.
 * HTML comments immediately inside an element select that element's opening tag.
 * At statement boundaries, HTML markers and script block markers prefer the
 * following unit; script line comments retain their trailing-statement convention.
 *
 * The text between candidates and a marker must contain whitespace or closing
 * punctuation. This keeps a marker from jumping over executable siblings. The
 * resulting exemption uses the chosen syntax range, so wrapping attributes or
 * expressions cannot enlarge the range to unrelated source sharing a line.
 */
export function exceptionRanges(text, parsed) {
  const ranges = []
  const masked = maskComments(text, parsed.comments)
  for (const comment of parsed.comments.filter((entry) =>
    /^\s*check-ui-allow(?:\s*:|$)/.test(entry.value),
  )) {
    const units = parsed.units.filter((unit) => unit.kind === comment.kind)
    const containing = units
      .filter((unit) => unit.range[0] <= comment.range[0] && unit.range[1] >= comment.range[1])
      .sort((left, right) => left.range[1] - left.range[0] - (right.range[1] - right.range[0]))
    if (comment.kind === 'html') {
      const parents = units
        .filter(
          (unit) =>
            unit.range[1] <= comment.range[0] &&
            unit.elementRange[1] >= comment.range[1] &&
            /^\s*$/.test(masked.slice(unit.range[1], comment.range[0])),
        )
        .sort((left, right) => right.range[0] - left.range[0])
      if (parents.length) {
        ranges.push(parents[0].range)
        continue
      }
    }
    const previous = units
      .filter((unit) => (unit.elementRange ?? unit.range)[1] <= comment.range[0])
      .sort(
        (left, right) =>
          (right.elementRange ?? right.range)[1] - (left.elementRange ?? left.range)[1],
      )[0]
    const next = units
      .filter((unit) => unit.range[0] >= comment.range[1])
      .sort((left, right) => left.range[0] - right.range[0])[0]
    const gap = previous
      ? masked.slice((previous.elementRange ?? previous.range)[1], comment.range[0])
      : undefined
    const nextIsAdjacent = next && /^\s*$/.test(masked.slice(comment.range[1], next.range[0]))
    // Formatters can move boundary block comments off the preceding statement's
    // line. Bind them to an adjacent following unit in both layouts, preventing
    // that cosmetic move from transferring the exception between sibling units.
    if (
      nextIsAdjacent &&
      (comment.kind === 'html' || (comment.kind === 'script' && comment.type === 'Block'))
    ) {
      ranges.push(next.range)
      continue
    }
    // Adjacent syntax takes precedence because enclosing function and block ranges
    // also contain local comments and would otherwise exempt unrelated sibling statements.
    if (previous && /^[\s},;]*$/.test(gap) && !gap.includes('\n')) ranges.push(previous.range)
    else if (nextIsAdjacent) ranges.push(next.range)
    else if (previous && /^[\s},;]*$/.test(gap)) ranges.push(previous.range)
    else if (containing.length) ranges.push(containing[0].range)
  }
  return ranges
}

/** sourceLocation returns the one-based line and column for a source offset. */
export function sourceLocation(text, offset) {
  return {
    line: text.slice(0, offset).split('\n').length,
    column: offset - text.lastIndexOf('\n', offset - 1),
  }
}

/**
 * runSourceCheck prints diagnostics and returns a command exit status.
 * Report mode permits policy violations during an active migration. Parse and
 * filesystem failures still fail, since their skipped files have no trustworthy result.
 */
export function runSourceCheck(name, check, paths = collectSourceFiles()) {
  let violations = 0
  let failures = 0
  for (const file of paths) {
    try {
      const text = readFileSync(file, 'utf8')
      for (const issue of check(text, relative(frontendRoot, file))) {
        violations += 1
        console.error(
          `${relative(frontendRoot, file)}:${issue.line ?? 1}:${issue.column ?? 1} ${issue.message}`,
        )
      }
    } catch (error) {
      // Continue collecting file failures so a single run identifies every unreadable
      // or malformed input; the final status still rejects the incomplete check.
      failures += 1
      console.error(`${relative(frontendRoot, file)}: ${error.message}`)
    }
  }
  console.log(
    `${name}: ${paths.length} files, ${violations} violations, ${failures} parse/read failures`,
  )
  return failures || (violations && !process.argv.includes('--report')) ? 1 : 0
}
