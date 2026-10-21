/**
 * codexImageToolMode.ts maps provider image-tool overrides to one form mode
 * and writes that mode back into extra settings. Explicit stripping outranks
 * bridge enablement, and the current boolean bridge key outranks its legacy
 * alias.
 *
 * Saving clears all participating keys before setting the selected override,
 * preventing an old strip policy or legacy boolean from overriding the new
 * choice. Full-object saves delete cleared keys; merge-style saves use null
 * values so persisted JSONB fields are cleared. The supplied extra object is
 * mutated and returned, so callers own the draft and select cleanup behavior
 * appropriate to their persistence operation.
 */

/**
 * CodexImageToolMode represents inherited, enabled, disabled, or stripped
 * image-tool handling.
 */
export type CodexImageToolMode = 'inherit' | 'enabled' | 'disabled' | 'block'

/**
 * These current bridge, legacy bridge, and explicit-tool policy keys
 * participate in one mode read and are cleared together before a mode write.
 */
export const CODEX_IMAGE_GENERATION_BRIDGE_KEY = 'codex_image_generation_bridge'
export const LEGACY_CODEX_IMAGE_GENERATION_BRIDGE_KEY = 'codex_image_generation_bridge_enabled'
export const CODEX_IMAGE_GENERATION_POLICY_KEY = 'codex_image_generation_explicit_tool_policy'

/**
 * CodexImageToolCleanupMode selects deletion or null clearing for the caller
 * persistence strategy.
 */
type CodexImageToolCleanupMode = 'delete' | 'null'

/**
 * readCodexImageToolMode selects one form mode from current and legacy
 * provider overrides. The explicit strip policy returns block before either
 * bridge flag is considered.
 *
 * Without strip, an actual boolean in the current bridge key takes precedence
 * over the legacy key, including an explicit false value. Only when the
 * current value is not boolean does the legacy value participate. True enables
 * the bridge, false disables it, and unrecognized or absent values return
 * inherit so the surrounding configuration remains authoritative.
 */
export function readCodexImageToolMode(
  extra: Record<string, unknown> | null | undefined,
): CodexImageToolMode {
  if (extra?.[CODEX_IMAGE_GENERATION_POLICY_KEY] === 'strip') {
    return 'block'
  }

  const bridgeValue =
    typeof extra?.[CODEX_IMAGE_GENERATION_BRIDGE_KEY] === 'boolean'
      ? extra[CODEX_IMAGE_GENERATION_BRIDGE_KEY]
      : extra?.[LEGACY_CODEX_IMAGE_GENERATION_BRIDGE_KEY]
  if (bridgeValue === true) return 'enabled'
  if (bridgeValue === false) return 'disabled'
  return 'inherit'
}

/**
 * applyCodexImageToolMode writes one image-tool mode into the supplied mutable
 * extra object and returns it. Choose delete cleanup for complete objects or
 * null cleanup for merge updates that must clear persisted fields.
 *
 * All current and legacy override keys are cleared before the selected mode is
 * installed. This prevents a stale strip policy from masking bridge enablement
 * and keeps inherit free of active overrides. Unrelated fields retain their
 * values, and callers remain responsible for submitting the resulting object
 * with the matching persistence behavior.
 */
export function applyCodexImageToolMode(
  extra: Record<string, unknown>,
  mode: CodexImageToolMode,
  cleanupMode: CodexImageToolCleanupMode = 'delete',
): Record<string, unknown> {
  /**
   * clear removes one override or writes null according to the chosen cleanup
   * mode. Null marks a persisted field for clearing in merge updates, while
   * deletion suits complete replacement objects.
   */
  const clear = (key: string) => {
    if (cleanupMode === 'null') {
      extra[key] = null
      return
    }
    delete extra[key]
  }

  /**
   * Clear all current and legacy overrides before writing the selected mode so
   * stale strip policy or bridge values cannot mask the new form selection.
   */
  clear(CODEX_IMAGE_GENERATION_BRIDGE_KEY)
  clear(LEGACY_CODEX_IMAGE_GENERATION_BRIDGE_KEY)
  clear(CODEX_IMAGE_GENERATION_POLICY_KEY)

  if (mode === 'enabled' || mode === 'disabled') {
    extra[CODEX_IMAGE_GENERATION_BRIDGE_KEY] = mode === 'enabled'
  } else if (mode === 'block') {
    extra[CODEX_IMAGE_GENERATION_POLICY_KEY] = 'strip'
  }

  return extra
}
