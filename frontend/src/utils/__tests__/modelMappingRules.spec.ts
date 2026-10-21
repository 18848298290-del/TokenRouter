import { describe, expect, it } from 'vitest'

import {
  KEY_REDIRECT_RULES,
  WILDCARD_ONLY_RULES,
  firstModelMappingIssue,
  mappingRowsToRecord,
  recordToMappingRows,
  validateModelMappingRows,
} from '../modelMappingRules'

/**
 * modelMappingRules.spec.ts tests row validation and mapping representation
 * conversion. Assertions cover optional rule activation, source and target issue
 * selection, Unicode code-point limits, wildcard placement, trimmed duplicate
 * sources, first-issue order, and record entry ordering.
 */

describe('validateModelMappingRows', () => {
  /**
   * A mapping row contains blank source text and a target wildcard with default
   * options. No checks are enabled, so the returned row issue object stays empty.
   */
  it('未开启任何检查时不报告问题', () => {
    expect(validateModelMappingRows([{ from: '', to: 'a*b' }])).toEqual([{}])
  })

  /**
   * Source rows exercise blank text, excess length, invalid wildcard placement, and
   * trimmed duplicates. The result identifies each source problem under the configured
   * Key redirect rules.
   */
  it('按必填、长度、通配符、重复的顺序检查来源', () => {
    const tooLong = 'a'.repeat(101)
    const issues = validateModelMappingRows(
      [
        { from: '  ', to: 'x' },
        { from: tooLong, to: 'x' },
        { from: 'a*b', to: 'x' },
        { from: 'dup', to: 'x' },
        { from: ' dup ', to: 'y' },
      ],
      KEY_REDIRECT_RULES,
    )

    expect(issues.map((issue) => issue.from)).toEqual([
      'sourceRequired',
      'nameTooLong',
      'sourceWildcardInvalid',
      'duplicateSource',
      'duplicateSource',
    ])
  })

  /**
   * Target rows exercise missing text, excess length, a wildcard, and trimmed
   * self-mapping. The corresponding first target issue is returned for every supplied
   * mapping row.
   */
  it('按必填、长度、通配符、自映射的顺序检查目标', () => {
    const issues = validateModelMappingRows(
      [
        { from: 'a', to: '' },
        { from: 'b', to: 'x'.repeat(101) },
        { from: 'c', to: 'gpt-*' },
        { from: 'd', to: ' d ' },
      ],
      KEY_REDIRECT_RULES,
    )

    expect(issues.map((issue) => issue.to)).toEqual([
      'targetRequired',
      'nameTooLong',
      'targetWildcardInvalid',
      'selfMapping',
    ])
  })

  /**
   * A source consists of one hundred supplementary-plane emoji characters. Code-point
   * length validation accepts the name, protecting the distinction between Unicode
   * characters and UTF-16 units.
   */
  it('长度按码点计算', () => {
    const emoji = '😀'.repeat(100)
    expect(validateModelMappingRows([{ from: emoji, to: 'x' }], KEY_REDIRECT_RULES)).toEqual([{}])
  })

  /**
   * A source consists of one trailing wildcard, then a second source repeats that
   * wildcard. Validation accepts the first and reports invalid placement for the
   * repeated-star form.
   */
  it('允许单独的末尾通配符', () => {
    expect(validateModelMappingRows([{ from: '*', to: 'x' }], KEY_REDIRECT_RULES)).toEqual([{}])
    expect(
      validateModelMappingRows([{ from: 'gpt-**', to: 'x' }], KEY_REDIRECT_RULES)[0].from,
    ).toBe('sourceWildcardInvalid')
  })

  /**
   * Wildcard-only options receive invalid patterns on both sides and a completely blank
   * row. Both wildcard issues appear together, while blank names remain allowed under
   * those options.
   */
  it('仅通配符规则可以同时报告来源和目标', () => {
    expect(validateModelMappingRows([{ from: 'a*b', to: 'c*' }], WILDCARD_ONLY_RULES)).toEqual([
      { from: 'sourceWildcardInvalid', to: 'targetWildcardInvalid' },
    ])
    expect(validateModelMappingRows([{ from: '', to: '' }], WILDCARD_ONLY_RULES)).toEqual([{}])
  })
})

describe('firstModelMappingIssue', () => {
  /**
   * An issue list has an empty row followed by both source and target failures.
   * First-issue selection chooses the source failure, and an entirely clear list
   * returns null.
   */
  it('按行序返回第一个问题并优先报告来源', () => {
    expect(firstModelMappingIssue([{}, { to: 'selfMapping', from: 'duplicateSource' }])).toBe(
      'duplicateSource',
    )
    expect(firstModelMappingIssue([{}, {}])).toBeNull()
  })
})

describe('映射对象转换', () => {
  /**
   * Editable source and target strings contain surrounding spaces. Submission
   * conversion trims both strings into the record key and value, matching the expected
   * mapping payload shape.
   */
  it('提交时去掉首尾空白', () => {
    expect(mappingRowsToRecord([{ from: ' a ', to: ' b ' }])).toEqual({ a: 'b' })
  })

  /**
   * A mapping object has a deliberate key order and is followed by a null input.
   * Expansion preserves the entry order and converts null into an empty row list.
   */
  it('展开时保持键顺序并兼容空值', () => {
    expect(recordToMappingRows({ b: '1', a: '2' })).toEqual([
      { from: 'b', to: '1' },
      { from: 'a', to: '2' },
    ])
    expect(recordToMappingRows(null)).toEqual([])
  })
})
