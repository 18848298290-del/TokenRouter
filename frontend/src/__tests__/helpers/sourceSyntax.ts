import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import postcss, { type Container, type Root, type Rule } from 'postcss'
import ts from 'typescript'
import { parse, type SFCDescriptor } from 'vue/compiler-sfc'

// sourceSyntax.ts exposes parsed source structures for tests of templates,
// stylesheet rules, and script declarations that cannot be observed in jsdom.
// Paths resolve from src so callers identify the production file directly, and
// parser nodes retain ownership relationships while ignoring formatting choices.

// TemplateRoot represents the parsed template attached to a Vue component descriptor.
type TemplateRoot = NonNullable<NonNullable<SFCDescriptor['template']>['ast']>
// TemplateElement represents an element discovered in the untransformed Vue template tree.
export type TemplateElement = Extract<TemplateRoot['children'][number], { type: 1 }>

// ParsedVue groups independent syntax trees from one component for source assertions.
export interface ParsedVue {
  script: ts.SourceFile // Combined component script declarations.
  elements: TemplateElement[] // Elements in document order.
  template: TemplateRoot | undefined // Original template ownership tree.
  styles: Root // Combined component stylesheet rules.
}

// readSource reads a UTF-8 source file relative to the frontend src directory.
export function readSource(path: string): string {
  return readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../..', path), 'utf8')
}

// parseScript parses TypeScript declarations for structural assertions without executing application code.
export function parseScript(source: string): ts.SourceFile {
  return ts.createSourceFile('source.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
}

// nodes collects matching script nodes in traversal order, including the root itself.
export function nodes<T extends ts.Node>(
  root: ts.Node,
  predicate: (node: ts.Node) => node is T,
): T[] {
  const matches: T[] = []
  // visit traverses child syntax nodes and records matches for the caller.
  function visit(node: ts.Node): void {
    if (predicate(node)) matches.push(node)
    ts.forEachChild(node, visit)
  }
  visit(root)
  return matches
}

// variableInitializer returns the first named variable initializer in a script subtree.
// Missing declarations throw so an absent production variable cannot silently satisfy
// a negative assertion about its contents or callers.
export function variableInitializer(root: ts.Node, name: string): ts.Expression {
  const declaration = nodes(root, ts.isVariableDeclaration).find(
    (node) => ts.isIdentifier(node.name) && node.name.text === name,
  )
  if (!declaration?.initializer) throw new Error(`Missing initialized variable: ${name}`)
  return declaration.initializer
}

// parseVue reads one component and parses its script, template, and every style
// block. Reported SFC and template parse errors throw, while script parse
// diagnostics remain on the returned TypeScript tree for callers to inspect.
// Template elements retain document order and their original ownership tree.
// Template expressions enable TypeScript syntax, including type assertions and
// typed arrow parameters; PostCSS parses the combined styles and can also throw.
export function parseVue(path: string): ParsedVue {
  const { descriptor, errors } = parse(readSource(path), {
    filename: path,
    templateParseOptions: { expressionPlugins: ['typescript'] },
  })
  if (errors.length) throw new Error(`Invalid Vue source ${path}: ${errors.join(', ')}`)
  const elements: TemplateElement[] = []
  // visit records element descendants while preserving each element's original children.
  function visit(children: TemplateRoot['children']): void {
    for (const child of children) {
      if (child.type === 1) {
        elements.push(child)
        visit(child.children)
      }
    }
  }
  if (descriptor.template?.ast) visit(descriptor.template.ast.children)
  return {
    script: parseScript(
      [descriptor.script?.content, descriptor.scriptSetup?.content].filter(Boolean).join('\n'),
    ),
    elements,
    template: descriptor.template?.ast,
    styles: postcss.parse(descriptor.styles.map((style) => style.content).join('\n')),
  }
}

// attribute returns the first literal attribute matching the supplied name.
// A present attribute without a value returns an empty string; an absent literal
// attribute returns undefined, including when the name appears only in a directive.
export function attribute(element: TemplateElement, name: string): string | undefined {
  const prop = element.props.find((prop) => prop.type === 6 && prop.name === name)
  return prop?.type === 6 ? (prop.value?.content ?? '') : undefined
}

// directive returns the first matching directive's simple expression content.
// Omitting argument accepts any argument; supplying it requires a simple argument
// node with matching content. Missing directives or missing simple expressions
// return undefined, while a present empty expression retains its empty string.
export function directive(
  element: TemplateElement,
  name: string,
  argument?: string,
): string | undefined {
  const prop = element.props.find(
    (prop) =>
      prop.type === 7 &&
      prop.name === name &&
      (argument === undefined || (prop.arg?.type === 4 && prop.arg.content === argument)),
  )
  return prop?.type === 7 && prop.exp?.type === 4 ? prop.exp.content : undefined
}

// classes splits one literal class attribute into independently assertable utility names.
export function classes(element: TemplateElement): string[] {
  return (attribute(element, 'class') ?? '').split(/\s+/).filter(Boolean)
}

// cssRules collects rules that contain the exact selector among their selector list.
export function cssRules(root: Container, selector: string): Rule[] {
  const rules: Rule[] = []
  root.walkRules((rule) => {
    if (rule.selectors.includes(selector)) rules.push(rule)
  })
  return rules
}

// applyTokens collects Tailwind utility names from direct apply rules on one selector.
export function applyTokens(rule: Rule): string[] {
  return rule.nodes.flatMap((node) =>
    node.type === 'atrule' && node.name === 'apply' ? node.params.split(/\s+/).filter(Boolean) : [],
  )
}

// declarations collects direct property values while preserving duplicate declarations and order.
export function declarations(rule: Rule, property: string): string[] {
  return rule.nodes.flatMap((node) =>
    node.type === 'decl' && node.prop === property ? [node.value] : [],
  )
}
