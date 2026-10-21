import { describe, expect, it } from 'vitest'
import type { LocalizedUpdate } from '../content'
import {
  addTranslation,
  adoptSourceLocale,
  markStillValid,
  promoteToOriginal,
  removeTranslation,
  setSourceLocale,
  translationStatus,
  translationSummary,
  updateSource,
  updateTranslation,
  withDefaultSource,
} from '../contentEdit'

// contentEdit.spec.ts checks localized draft editing and source-language
// transitions. Tests observe review and deletion requests alongside visible
// content, including legacy unknown-language sources and repeated promotion, so
// saved translation identity survives the editor operations exercised here.

// saved constructs a saved Chinese source with a current English translation.
function saved(): LocalizedUpdate<string> {
  return {
    source: '原文',
    source_locale: 'zh-Hans',
    revision: 3,
    source_revision: 2,
    translations: { en: { value: 'English', source_revision: 2 } },
  }
}

describe('译文编辑', () => {
  // An editor replaces an existing translation with revised text. The resulting
  // draft records that locale as reviewed and immediately reports the
  // translation as usable.
  it('编辑译文时记为已核对', () => {
    const content = updateTranslation(saved(), 'en', 'Updated')
    expect(content.translations.en.value).toBe('Updated')
    expect(content.reviewed_locales).toEqual(['en'])
    expect(translationStatus(content, 'en')).toBe('translated')
  })

  // A source edit changes the reference text for an existing translation. The
  // draft marks it stale until review is recorded, then restores usability while
  // retaining the translated value.
  it('修改原文后译文过期，确认仍然适用后恢复展示', () => {
    const changed = updateSource(saved(), '新原文')
    expect(changed.reviewed_locales).toEqual([])
    expect(translationStatus(changed, 'en')).toBe('stale')
    expect(translationSummary(changed)).toEqual({
      locales: ['en'],
      stale: true,
      sourceLocaleMissing: false,
    })
    const confirmed = markStillValid(changed, 'en')
    expect(translationStatus(confirmed, 'en')).toBe('translated')
    expect(confirmed.translations.en.value).toBe('English')
  })

  // Editing helpers receive a saved content object that remains owned by their
  // caller. Changing either source or translation returns separate drafts and
  // leaves the supplied object equal to its fixture.
  it('编辑函数返回新对象，传入的内容保持原值', () => {
    const content = saved()
    updateTranslation(content, 'en', 'Updated')
    updateSource(content, '新原文')
    expect(content).toEqual(saved())
  })

  // Legacy content has no original-language label and a current interface
  // default is available. Editing adopts that language, while default
  // initialization labels only content with zero revision.
  it('原文语言未知时，修改原文填入当前语言', () => {
    const legacy: LocalizedUpdate<string> = {
      source: 'TokenRouter',
      source_locale: null,
      revision: 1,
      source_revision: 1,
      translations: {},
    }
    expect(updateSource(legacy, 'Token Router', 'en').source_locale).toBe('en')
    expect(withDefaultSource(legacy, 'en').source_locale).toBeNull()
    expect(withDefaultSource({ ...legacy, revision: 0 }, 'en').source_locale).toBe('en')
  })

  // An unknown-language original already has a translation in the proposed
  // default language. Source editing leaves the language unknown so the
  // conflicting roles remain for administrator resolution.
  it('当前语言已有译文时，原文语言保持未知', () => {
    const legacy: LocalizedUpdate<string> = {
      source: '站点',
      source_locale: null,
      revision: 1,
      source_revision: 1,
      translations: { en: { value: 'Site', source_revision: 1 } },
    }
    expect(updateSource(legacy, '新站点', 'en').source_locale).toBeNull()
  })

  // A saved translation is removed and then recreated in one draft. Deletion
  // requests clear when recreation copies the original into a reviewed
  // translation slot.
  it('添加译文用原文预填，删除译文记录到 deleted_locales', () => {
    const removed = removeTranslation(saved(), 'en')
    expect(removed.translations).toEqual({})
    expect(removed.deleted_locales).toEqual(['en'])
    expect(translationStatus(removed, 'en')).toBe('missing')
    const added = addTranslation(removed, 'en')
    expect(added.translations.en.value).toBe('原文')
    expect(added.deleted_locales).toEqual([])
    expect(translationStatus(added, 'en')).toBe('translated')
  })

  // The requested original language already occupies a translation slot. The
  // source-language operation reports a conflict and retains the previous
  // original-language label for the caller.
  it('改原文语言遇到同语言译文时报告冲突', () => {
    const result = setSourceLocale(saved(), 'en')
    expect(result.conflict).toBe(true)
    expect(result.content.source_locale).toBe('zh-Hans')
  })

  // Legacy content receives a source-language label that already has a
  // translation. Adoption removes that conflicting translation and keeps the
  // other previously current translation usable in the draft.
  it('历史内容定下原文语言时，删除同语言的旧译文', () => {
    const legacy: LocalizedUpdate<string> = {
      source: 'TokenRouter',
      source_locale: null,
      revision: 1,
      source_revision: 1,
      translations: {
        en: { value: 'TokenRouter', source_revision: 1 },
        'zh-Hans': { value: '中转站', source_revision: 1 },
      },
    }
    const content = adoptSourceLocale(legacy, 'en')
    expect(content.source_locale).toBe('en')
    expect(content.translations.en).toBeUndefined()
    expect(content.deleted_locales).toEqual(['en'])
    expect(translationStatus(content, 'zh-Hans')).toBe('translated')
  })

  // A known original language changes to a different language with no
  // translation conflict. Existing translated content becomes stale and requires
  // a new review before display.
  it('已知原文语言改成其他语言时，译文需要重新核对', () => {
    const result = setSourceLocale(saved(), 'ja')
    expect(result.conflict).toBe(false)
    expect(translationStatus(result.content, 'en')).toBe('stale')
  })

  // An existing translation is promoted to the original language. The previous
  // original moves into a translation slot, the promoted slot is deleted, and
  // retained content requires review.
  it('译文设为原文后，旧原文转成译文', () => {
    const content = promoteToOriginal(saved(), 'en')
    expect(content.source).toBe('English')
    expect(content.source_locale).toBe('en')
    expect(content.translations['zh-Hans'].value).toBe('原文')
    expect(content.deleted_locales).toEqual(['en'])
    expect(translationStatus(content, 'zh-Hans')).toBe('stale')
  })

  // A draft promotes a translation and then promotes the previous original back
  // again. The restored translation is present and its locale is removed from
  // pending deletion requests.
  it('同一草稿切回原文语言后，恢复的译文可以提交', () => {
    const english = promoteToOriginal(saved(), 'en')
    const restored = promoteToOriginal(english, 'zh-Hans')
    expect(restored.source).toBe('原文')
    expect(restored.translations.en.value).toBe('English')
    expect(restored.deleted_locales).not.toContain('en')
    expect(restored.deleted_locales).toEqual(['zh-Hans'])
  })
})
