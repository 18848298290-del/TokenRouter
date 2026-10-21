import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { directive, nodes, parseVue, variableInitializer } from '@/__tests__/helpers/sourceSyntax'

// groupsSessionIsolation.spec.ts checks how group forms carry session isolation
// through their controls and request payloads. Parsed declarations identify the
// field defaults, saved-group restoration, table column, and form spreads while
// allowing expression line breaks and quote styles to vary.

// The page and settings component jointly own the draft-to-request path inspected
// below; parsing both prevents a field name in an unrelated comment from passing.
const page = parseVue('views/admin/GroupsView.vue')
const settings = parseVue('components/admin/group/GroupSettingsForm.vue')

// member identifies a property access on the named form or group object.
function member(node: ts.Node, owner: string, field: string): boolean {
  return (
    ts.isPropertyAccessExpression(node) &&
    ts.isIdentifier(node.expression) &&
    node.expression.text === owner &&
    node.name.text === field
  )
}

describe('groups session isolation', () => {
  // Create and edit share the session-isolation control and retain its value in requests.
  // Missing saved values become false while the column continues exposing the setting.
  it('connects the switch, both form drafts, and their request spreads', () => {
    expect(
      settings.elements.some(
        (element) =>
          element.tag === 'SettingToggleRow' &&
          directive(element, 'model') === 'form.session_isolation_enabled',
      ),
    ).toBe(true)
    expect(
      page.elements
        .filter((element) => element.tag === 'GroupSettingsForm')
        .map((element) => directive(element, 'bind', 'model-value')),
    ).toEqual(['createForm', 'editForm'])
    for (const form of ['createForm', 'editForm']) {
      const field = nodes(variableInitializer(page.script, form), ts.isPropertyAssignment).find(
        (node) => ts.isIdentifier(node.name) && node.name.text === 'session_isolation_enabled',
      )!
      expect(field.initializer.kind).toBe(ts.SyntaxKind.FalseKeyword)
    }
    const restored = nodes(page.script, ts.isBinaryExpression).find(
      (node) =>
        node.operatorToken.kind === ts.SyntaxKind.EqualsToken &&
        member(node.left, 'editForm', 'session_isolation_enabled') &&
        ts.isBinaryExpression(node.right),
    )!
    expect(ts.isBinaryExpression(restored.right) && restored.right.operatorToken.kind).toBe(
      ts.SyntaxKind.QuestionQuestionToken,
    )
    if (!ts.isBinaryExpression(restored.right))
      throw new Error('Missing saved session isolation expression')
    expect(member(restored.right.left, 'group', 'session_isolation_enabled')).toBe(true)
    expect(restored.right.right.kind).toBe(ts.SyntaxKind.FalseKeyword)
    for (const [handler, payload, form] of [
      ['handleCreateGroup', 'requestData', 'createForm'],
      ['handleUpdateGroup', 'payload', 'editForm'],
    ]) {
      const data = variableInitializer(variableInitializer(page.script, handler), payload)
      expect(
        nodes(data, ts.isSpreadAssignment).some(
          (node) => ts.isIdentifier(node.expression) && node.expression.text === form,
        ),
      ).toBe(true)
    }
    expect(
      nodes(variableInitializer(page.script, 'allColumns'), ts.isPropertyAssignment).some(
        (node) =>
          ts.isIdentifier(node.name) &&
          node.name.text === 'key' &&
          ts.isStringLiteral(node.initializer) &&
          node.initializer.text === 'session_isolation_enabled',
      ),
    ).toBe(true)
  })
})
