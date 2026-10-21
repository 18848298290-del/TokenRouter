/**
 * creativeDrag.ts encodes history output references and validates image drops
 * for the creative canvas. Drag data carries a run identifier and output index
 * so the receiving canvas can resolve the image from its browser asset store.
 * External files are accepted by their supplied MIME type, with a filename
 * extension check when that type is empty.
 */

/**
 * CREATIVE_OUTPUT_DRAG_MIME identifies the private history payload that the
 * canvas resolves through browser-local assets.
 */
export const CREATIVE_OUTPUT_DRAG_MIME = 'application/x-tokenrouter-creative-output'

/**
 * CreativeOutputDragPayload identifies one browser-local output by its server
 * run and index.
 */
export interface CreativeOutputDragPayload {
  runId: string // server run identifier
  outputIndex: number // zero-based output index
}

/**
 * CREATIVE_OUTPUT_RUN_ID_RE bounds run identifiers accepted from arbitrary drag
 * payloads before local asset lookup.
 */
const CREATIVE_OUTPUT_RUN_ID_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/
/**
 * SUPPORTED_IMAGE_MIMES lists explicit file content types accepted by the
 * external image drop handler.
 */
const SUPPORTED_IMAGE_MIMES = new Set(['image/png', 'image/jpeg', 'image/webp'])
/**
 * SUPPORTED_IMAGE_EXTENSIONS supplies the filename check when a dropped file
 * omits its MIME type.
 */
const SUPPORTED_IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'webp'])

/**
 * isSupportedCreativeImageFile reports whether a dropped file uses a supported
 * image format. A supplied MIME type takes precedence over the filename;
 * extensions are considered only when the MIME type is empty.
 */
export function isSupportedCreativeImageFile(file: File): boolean {
  const mime = file.type.trim().toLowerCase()
  if (mime) return SUPPORTED_IMAGE_MIMES.has(mime)
  /**
   * Operating-system file drags can omit MIME metadata, so filenames supply the
   * format check in that specific case.
   */
  const extension = file.name.split('.').pop()?.trim().toLowerCase() ?? ''
  return SUPPORTED_IMAGE_EXTENSIONS.has(extension)
}

/**
 * serializeCreativeOutputDrag serializes a history output reference for
 * DataTransfer storage. The caller supplies a validated run identifier and
 * output index; this encoder performs no additional validation before producing
 * JSON.
 */
export function serializeCreativeOutputDrag(payload: CreativeOutputDragPayload): string {
  return JSON.stringify(payload)
}

/**
 * parseCreativeOutputDrag decodes and validates an untrusted history output
 * reference, trimming its run identifier. Empty input, malformed JSON, invalid
 * identifiers, and negative or fractional output indexes return null so drop
 * handlers can display their invalid-drop message.
 */
export function parseCreativeOutputDrag(
  value: string | null | undefined,
): CreativeOutputDragPayload | null {
  if (!value) return null
  try {
    const parsed: unknown = JSON.parse(value)
    if (!parsed || typeof parsed !== 'object') return null
    const candidate = parsed as {
      runId?: unknown // server run identifier
      outputIndex?: unknown // zero-based output index
    }
    if (typeof candidate.runId !== 'string') return null
    const runId = candidate.runId.trim()
    if (!CREATIVE_OUTPUT_RUN_ID_RE.test(runId)) return null
    if (
      typeof candidate.outputIndex !== 'number' ||
      !Number.isInteger(candidate.outputIndex) ||
      candidate.outputIndex < 0
    ) {
      return null
    }
    return { runId, outputIndex: candidate.outputIndex }
  } catch {
    /**
     * Malformed JSON is treated as an invalid drag reference so arbitrary text
     * drops cannot escape the canvas validation path.
     */
    return null
  }
}
