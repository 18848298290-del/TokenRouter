import { describe, expect, it } from 'vitest'
import ts from 'typescript'
import {
  attribute,
  classes,
  directive,
  nodes,
  parseScript,
  parseVue,
} from '@/__tests__/helpers/sourceSyntax'

// CreativeStudioView.layout.spec.ts checks the canvas page's viewport ownership
// through parsed template elements. The application layout supplies available
// height, while the stage keeps a shrinkable flex chain that supports narrow
// screens and the independent history overlay.

// The parsed component keeps class tokens attached to their owning elements so
// template indentation and attribute wrapping do not affect viewport assertions.
const view = parseVue('views/user/CreativeStudioView.vue')

describe('CreativeStudioView mobile viewport layout', () => {
  // The canvas page requests the full viewport from its application shell.
  // This keeps the shell responsible for header offsets and main content padding.
  it('requests full viewport mode from AppLayout', () => {
    const layout = view.elements.find((element) => element.tag === 'AppLayout')!
    expect(attribute(layout, 'full-viewport')).toBe('')
  })

  // The stage consumes height supplied by the application layout flex chain.
  // Negative margins and viewport-minus-header utilities would couple this page to shell dimensions.
  it('inherits stage height and keeps shell sizing out of utility classes', () => {
    const stage = view.elements.find((element) => attribute(element, 'ref') === 'stageRef')!
    expect(classes(stage)).toEqual(expect.arrayContaining(['relative', 'h-full', 'min-h-0']))
    const utilities = view.elements.flatMap((element) => [
      ...classes(element),
      ...nodes(parseScript(directive(element, 'bind', 'class') ?? ''), ts.isStringLiteral).flatMap(
        (node) => node.text.split(/\s+/),
      ),
    ])
    expect(utilities.filter((value) => /(?:^|:)-m[txyblr]?-/u.test(value))).toEqual([])
    expect(utilities.filter((value) => /^h-\[calc\(100d?vh-/u.test(value))).toEqual([])
  })
})
