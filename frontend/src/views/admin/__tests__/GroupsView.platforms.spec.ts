import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { nodes, parseVue, variableInitializer } from '@/__tests__/helpers/sourceSyntax'

// GroupsView.platforms.spec.ts checks the fields represented by general group
// drafts and their submit payloads. Groups apply across provider platforms, so
// create and edit state omit platform selection and the retired default-group
// flag while preserving their own request construction.

// The page syntax tree identifies draft fields and submitted object properties
// directly, so comments and quote formatting do not determine this regression.
const script = parseVue('views/admin/GroupsView.vue').script

describe('general group management', () => {
  // Creating or editing a general group must not submit platform or default-group fields.
  // Both drafts and their request objects are inspected because payloads spread the drafts.
  it('omits platform and default-group fields from drafts and payloads', () => {
    const objects = [
      variableInitializer(script, 'createForm'),
      variableInitializer(script, 'editForm'),
      variableInitializer(variableInitializer(script, 'handleCreateGroup'), 'requestData'),
      variableInitializer(variableInitializer(script, 'handleUpdateGroup'), 'payload'),
    ]
    for (const object of objects) {
      const names = nodes(object, ts.isPropertyAssignment).map((node) =>
        ts.isIdentifier(node.name) || ts.isStringLiteral(node.name) ? node.name.text : '',
      )
      expect(names).not.toContain('platform')
      expect(names).not.toContain('is_default')
    }
    expect(
      nodes(script, ts.isPropertyAccessExpression).some(
        (node) =>
          ts.isIdentifier(node.expression) &&
          ['createForm', 'editForm'].includes(node.expression.text) &&
          node.name.text === 'platform',
      ),
    ).toBe(false)
  })
})
