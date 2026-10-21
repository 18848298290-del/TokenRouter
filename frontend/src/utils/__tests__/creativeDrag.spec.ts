import { describe, expect, it } from 'vitest'
import {
  isSupportedCreativeImageFile,
  parseCreativeOutputDrag,
  serializeCreativeOutputDrag,
} from '@/utils/creativeDrag'

/**
 * creativeDrag.spec.ts checks image-file admission and the private history
 * output drag encoding. The tests preserve the distinction between supplied
 * MIME types and missing MIME metadata, then exercise valid round trips and
 * malformed payload rejection through the production parser. File bytes remain
 * trivial fixtures because admission uses metadata.
 */

describe('creativeDrag', () => {
  /**
   * Dropped files include supported MIME types, missing MIME metadata, and
   * conflicting extensions. Admission must use supplied content types first and
   * consult filename extensions only when that type is empty.
   */
  it('识别支持的图片 MIME 与无 MIME 文件扩展名', () => {
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.png', { type: 'image/png' }))).toBe(true)
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.jpg', { type: 'image/jpeg' }))).toBe(
      true,
    )
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.webp', { type: '' }))).toBe(true)
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.gif', { type: 'image/gif' }))).toBe(
      false,
    )
    expect(isSupportedCreativeImageFile(new File(['x'], 'a.png', { type: 'text/plain' }))).toBe(
      false,
    )
  })

  /**
   * A history output reference contains a valid run identifier and integer output
   * index. Serializing and parsing it must return the same identity for local
   * asset lookup.
   */
  it('往返序列化历史输出拖放载荷', () => {
    const payload = { runId: 'crun_0123456789abcdef', outputIndex: 2 }
    expect(parseCreativeOutputDrag(serializeCreativeOutputDrag(payload))).toEqual(payload)
  })

  /**
   * Malformed and incomplete drag strings include invalid JSON, identifiers, and
   * output indexes. Every supplied case must return null rather than passing an
   * unusable identity to canvas lookup.
   */
  it.each([
    '',
    '{bad json',
    JSON.stringify(null),
    JSON.stringify({ runId: '', outputIndex: 0 }),
    JSON.stringify({ runId: 'crun 1', outputIndex: 0 }),
    JSON.stringify({ runId: 'crun_1', outputIndex: -1 }),
    JSON.stringify({ runId: 'crun_1', outputIndex: 1.5 }),
    JSON.stringify({ runId: 'crun_1', outputIndex: '1' }),
  ])('拒绝非法拖放载荷 %s', (value) => {
    expect(parseCreativeOutputDrag(value)).toBeNull()
  })
})
