import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import ts from 'typescript'
import { parse } from 'vue/compiler-sfc'

// providerSource.ts reads provider Vue components for structural regression
// assertions about template bindings and script handlers. The helpers use
// Vue's template tree and TypeScript's printer so expressions from both
// sources have a comparable spelling. Expression printing removes comments,
// and Vue parsing separates template whitespace from attribute records. Named
// initializer lookup fails when a required declaration is missing instead of
// producing an empty set of calls.
//
// Template records contain element tags, static attributes, and directive keys
// paired with normalized expressions. They are a structural view rather than a
// rendered component: unsupported argument or expression shapes use empty
// text, and repeated attribute keys overwrite earlier entries. Behavior tests
// exercise the events and requests that these source checks only locate.

// printNode prints a syntax node without comments for structural expression
// checks.
export function printNode(node: ts.Node): string {
  return ts
    .createPrinter({ removeComments: true })
    .printNode(ts.EmitHint.Unspecified, node, node.getSourceFile())
}

// descendants collects matching nodes from the supplied root in preorder. The
// root itself is tested before its children, and the predicate determines
// which node type is returned to the structural assertion.
export function descendants<T extends ts.Node>(
  root: ts.Node,
  predicate: (node: ts.Node) => node is T,
): T[] {
  const matches: T[] = []
  // visit traverses each child once and records nodes accepted by the
  // predicate.
  const visit = (node: ts.Node) => {
    if (predicate(node)) matches.push(node)
    ts.forEachChild(node, visit)
  }
  visit(root)
  return matches
}

// expression parses a template expression into the same representation as
// script code.
export function expression(value: string): string {
  const source = ts.createSourceFile('expression.ts', value, ts.ScriptTarget.Latest, true)
  return source.statements.map(printNode).join('\n')
}

// loadProviderSource reads a Vue component relative to the current working
// directory and returns its script tree and flattened template records.
// Callers run from the frontend checkout root. Vue parse errors, missing
// script setup, or a missing template AST throw before assertions inspect the
// component.
//
// Static attributes retain their values; directive expressions use the same
// printer as script expressions. This gives wiring checks comparable strings
// without promising a rendered DOM or TypeScript semantic validation. Records
// follow element preorder and retain only the last value for a repeated key.
export function loadProviderSource(path: string) {
  const filename = resolve(process.cwd(), path)
  const { descriptor, errors } = parse(readFileSync(filename, 'utf8'), { filename })
  if (errors.length || !descriptor.scriptSetup || !descriptor.template?.ast) {
    throw new Error(`Cannot parse provider component: ${path}`)
  }
  const script = ts.createSourceFile(
    filename,
    descriptor.scriptSetup.content,
    ts.ScriptTarget.Latest,
    true,
  )
  const elements: Record<string, string>[] = []
  // visit appends an element record before walking its child elements.
  // Non-element nodes contribute no record, and unsupported directive argument
  // or expression shapes become empty strings in this structural fixture view.
  const visit = (node: (typeof descriptor.template.ast.children)[number]) => {
    if (node.type !== 1) return
    const attributes: Record<string, string> = { tag: node.tag }
    for (const prop of node.props) {
      if (prop.type === 6) attributes[prop.name] = prop.value?.content ?? ''
      // Directive keys include their argument to distinguish event and prop
      // bindings; unsupported shapes remain empty rather than being evaluated.
      if (prop.type === 7) {
        const argument = prop.arg?.type === 4 ? prop.arg.content : ''
        attributes[`${prop.name}:${argument}`] = expression(
          prop.exp?.type === 4 ? prop.exp.content : '',
        )
      }
    }
    elements.push(attributes)
    node.children.forEach(visit)
  }
  descriptor.template.ast.children.forEach(visit)
  return { script, elements }
}

// initializer locates a named declaration and requires an executable
// initializer. Missing declarations fail the test at their lookup, preventing
// empty AST queries from accidentally satisfying negative assertions about a
// removed handler.
export function initializer(script: ts.SourceFile, name: string): ts.Expression {
  const declaration = descendants(script, ts.isVariableDeclaration).find(
    (node) => ts.isIdentifier(node.name) && node.name.text === name,
  )
  if (!declaration?.initializer) throw new Error(`Missing declaration: ${name}`)
  return declaration.initializer
}

// calls lists actual call expressions beneath the selected handler or
// expression.
export function calls(root: ts.Node): string[] {
  return descendants(root, ts.isCallExpression).map((node) => printNode(node.expression))
}
