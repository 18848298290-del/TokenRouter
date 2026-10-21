import { afterEach, describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { defineComponent, nextTick, ref } from 'vue'
import LocalizedEditor from '../LocalizedEditor.vue'
import type { LocalizedUpdate } from '@/i18n/content'

/**
 * LocalizedEditor.spec.ts checks source and translation edits through a controlled
 * parent and the rendered dialog. Cases verify parent publication, cancellation, stale
 * translations, and review acknowledgement; DOM cleanup removes teleported elements
 * after each mounted editor is unmounted.
 */

/**
 * wrapper owns the mounted editor instance for dialog helpers and teardown, which
 * unmounts it after each interaction case.
 */
let wrapper: VueWrapper | undefined

/**
 * editor mounts a controlled parent containing source and translated content. It
 * attaches to document.body for the teleported dialog and returns the mounted wrapper
 * and shared content for assertions.
 */
function editor() {
  const content = ref<LocalizedUpdate<string>>({
    source: '原文',
    source_locale: 'zh-Hans',
    revision: 3,
    source_revision: 2,
    translations: { en: { value: 'English', source_revision: 2 } },
  })
  const parent = defineComponent({
    components: { LocalizedEditor },
    setup: () => ({ content }),
    template: '<LocalizedEditor v-model="content" label="站点名称" />',
  })
  wrapper = mount(parent, {
    attachTo: document.body,
    global: {
      plugins: [
        createI18n({
          legacy: false,
          locale: 'zh-Hans',
          missingWarn: false,
          fallbackWarn: false,
          messages: {},
        }),
      ],
    },
  })
  return { wrapper, content }
}

/**
 * dialog retrieves the current teleported dialog element from the document.
 * Callers open the translation dialog first; the cast assumes a matching element
 * exists and leaves missing element handling to the test.
 */
function dialog(): HTMLElement {
  return document.body.querySelector('[role="dialog"]') as HTMLElement
}

/**
 * button finds a dialog button by contained text or accessible label. It casts the
 * match to HTMLButtonElement, so callers supply a label known to exist in the mounted
 * dialog.
 */
function button(root: ParentNode, text: string): HTMLButtonElement {
  return [...root.querySelectorAll('button')].find(
    (item) => item.textContent?.includes(text) || item.getAttribute('aria-label') === text,
  ) as HTMLButtonElement
}

/**
 * openDialog clicks the current editor translation button and waits for rendering.
 * Callers initialize the shared wrapper with editor before invoking this helper so its
 * nonnull assertion has a mounted target.
 */
async function openDialog(): Promise<void> {
  await wrapper!.get('button').trigger('click')
  await nextTick()
}

/**
 * input replaces an editor value, dispatches input, and waits for Vue updates.
 */
async function input(element: HTMLInputElement, value: string): Promise<void> {
  element.value = value
  element.dispatchEvent(new Event('input'))
  await nextTick()
}

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined

  /**
   * Teleported dialog nodes can outlive their closing transition during teardown,
   * so clear the document before another case mounts an editor.
   */
  document.body.innerHTML = ''
})

describe('翻译编辑', () => {
  /**
   * The source field is edited while an English translation already exists. The parent
   * source updates, the translation text remains intact, and the summary marks its
   * revision stale.
   */
  it('表单里的输入只修改原文，译文随之过期', async () => {
    const { wrapper, content } = editor()
    await wrapper.get('input').setValue('修改后')
    expect(content.value.source).toBe('修改后')
    expect(content.value.translations.en.value).toBe('English')
    expect(wrapper.get('button').text()).toContain('localization.needsUpdate')
  })

  /**
   * A translation is edited inside the open dialog draft. The parent remains unchanged
   * until Done publishes the translated value and records the reviewed language.
   */
  it('弹窗里编辑译文，点完成后写回并记为已核对', async () => {
    const { content } = editor()
    await openDialog()
    const inputs = dialog().querySelectorAll('input')
    await input(inputs[inputs.length - 1], 'Updated')
    expect(content.value.translations.en.value).toBe('English')
    button(dialog(), 'localization.done').click()
    await nextTick()
    expect(content.value.translations.en.value).toBe('Updated')
    expect(content.value.reviewed_locales).toEqual(['en'])
  })

  /**
   * A translation is removed in the temporary dialog and the dialog is cancelled. The
   * parent retains the translation and receives no deleted language record.
   */
  it('取消后丢弃弹窗里的修改', async () => {
    const { content } = editor()
    await openDialog()
    button(dialog(), 'localization.removeTranslation').click()
    await nextTick()
    button(dialog(), 'common.cancel').click()
    await nextTick()
    expect(content.value.translations.en.value).toBe('English')
    expect(content.value.deleted_locales).toBeUndefined()
  })

  /**
   * The source changes while an existing translation remains usable. Confirming
   * validity and completing the dialog marks that language reviewed and clears the
   * stale summary warning.
   */
  it('原文修改后可以确认旧译文仍然适用', async () => {
    const { wrapper, content } = editor()
    await wrapper.get('input').setValue('修改后')
    await openDialog()
    expect(dialog().textContent).toContain('localization.staleNotice')
    button(dialog(), 'localization.stillValid').click()
    await nextTick()
    button(dialog(), 'localization.done').click()
    await nextTick()
    expect(content.value.reviewed_locales).toEqual(['en'])
    expect(wrapper.get('button').text()).not.toContain('localization.needsUpdate')
  })
})
