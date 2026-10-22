import type { MCPOperation } from '@/api/admin/mcp'

// permissions.ts expands convenience presets into the current operation directory.
// Saved tokens retain explicit operation identifiers, so newly registered features
// require another deliberate selection. Presets affect the editor draft only and
// leave final permission checks with the management API.

// gatewayModules identifies configuration and diagnostic categories included in the gateway preset.
const gatewayModules = new Set([
  'providers',
  'groups',
  'proxies',
  'pricing',
  'model-attributes',
  'ops',
  'requests',
  'usage',
  'scheduled-test-plans',
])
// accountModules identifies account and entitlement operations, including their existing confirmation requirements.
const accountModules = new Set([
  'users',
  'teams',
  'subscriptions',
  'plans',
  'redeem-codes',
  'promo-codes',
  'user-attributes',
])

// PermissionPreset contains one transient selection expanded from mounted operations.
export interface PermissionPreset {
  id: string // Stable editor preset identifier.
  permissions: string[] // Exact current operation identifiers.
}

// permissionPresets builds explicit grant sets from current metadata. Read-only
// presets omit sensitive reads and interactive browser workflows; the gateway
// preset adds ordinary configuration actions, while account management retains
// the selected operations' normal confirmation policy.
export function permissionPresets(operations: MCPOperation[]): PermissionPreset[] {
  // safeRead excludes protected exports and interactive flows from routine observation grants.
  const safeRead = (operation: MCPOperation) =>
    operation.read_only && !operation.sensitive && operation.response_mode !== 'browser'
  return [
    { id: 'readOnly', permissions: operations.filter(safeRead).map((op) => op.operation_id) },
    {
      id: 'gateway',
      permissions: operations
        .filter(
          (op) =>
            safeRead(op) ||
            (gatewayModules.has(op.module) && !op.sensitive && op.response_mode !== 'browser'),
        )
        .map((op) => op.operation_id),
    },
    {
      id: 'accounts',
      permissions: operations
        .filter((op) => safeRead(op) || accountModules.has(op.module))
        .map((op) => op.operation_id),
    },
    { id: 'all', permissions: operations.map((op) => op.operation_id) },
  ]
}
