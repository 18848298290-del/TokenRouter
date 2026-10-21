import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { directive, nodes, parseVue, variableInitializer } from '@/__tests__/helpers/sourceSyntax'

// groupsSchedulerType.spec.ts checks scheduler selection in group drafts and
// settings controls. Both forms begin with the basic scheduler, saved groups
// restore their selection, and advanced configuration remains available through
// the page's shared override dialog.

// These parsed components cover page state and the shared form control together,
// so selectors and defaults are checked at the declarations that use them.
const page = parseVue('views/admin/GroupsView.vue')
const settings = parseVue('components/admin/group/GroupSettingsForm.vue')

describe('groups scheduler type', () => {
  // Both group forms use the shared scheduler selector and begin with basic scheduling.
  // Editing a group restores its value with a basic default for missing metadata.
  it('connects the shared selector and scheduler defaults in both forms', () => {
    const selector = settings.elements.find(
      (element) =>
        element.tag === 'Select' && directive(element, 'model') === 'form.scheduler_type',
    )!
    expect(directive(selector, 'bind', 'options')).toBe('schedulerOptions')
    expect(
      page.elements
        .filter((element) => element.tag === 'GroupSettingsForm')
        .map((element) => directive(element, 'bind', 'model-value')),
    ).toEqual(['createForm', 'editForm'])
    for (const form of ['createForm', 'editForm']) {
      const field = nodes(variableInitializer(page.script, form), ts.isPropertyAssignment).find(
        (node) => ts.isIdentifier(node.name) && node.name.text === 'scheduler_type',
      )!
      const value = ts.isAsExpression(field.initializer)
        ? field.initializer.expression
        : field.initializer
      expect(ts.isStringLiteral(value) && value.text).toBe('basic')
      expect(
        nodes(variableInitializer(page.script, form), ts.isPropertyAssignment).some(
          (node) => ts.isIdentifier(node.name) && node.name.text === 'advanced_scheduler_overrides',
        ),
      ).toBe(true)
    }
    const defaults = nodes(page.script, ts.isBinaryExpression).filter(
      (node) =>
        node.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken &&
        ts.isPropertyAccessExpression(node.left) &&
        node.left.name.text === 'scheduler_type',
    )
    expect(defaults).toHaveLength(1)
    expect(ts.isStringLiteral(defaults[0].right) && defaults[0].right.text).toBe('basic')
    expect(
      page.elements.some((element) => element.tag === 'GroupAdvancedSchedulerOverridesModal'),
    ).toBe(true)
    expect(
      [...page.elements, ...settings.elements].some((element) => element.tag === 'select'),
    ).toBe(false)
  })
})
