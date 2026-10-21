import { defineComponent, h, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import CreativeComposer from '@/components/creative/CreativeComposer.vue'
import { useCreativeStudio } from '@/composables/useCreativeStudio'

/**
 * CreativeComposer.prompt.spec.ts checks prompt textarea sizing through the
 * real studio and mounted composer. Reactive reuse of saved prompts must resize
 * after DOM updates just as manual input does. A deterministic scrollHeight
 * fixture substitutes for absent DOM layout while preserving the component's
 * own height calculation and configured cap.
 */

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: null }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number) => String(value),
    balanceUnitSymbol: '$',
  }),
}))

describe('CreativeComposer 提示词高度', () => {
  /**
   * Saved prompts replace the composer text without native input events. The
   * mounted textarea must grow to its cap, shrink for shorter reuse, and continue
   * sizing after manual edits.
   */
  it('复用历史提示词后按内容伸缩，手动输入继续调整高度', async () => {
    let studio!: ReturnType<typeof useCreativeStudio>
    const wrapper = mount(
      defineComponent({
        /**
         * setup creates the studio within a mounted component and renders the composer.
         */
        setup() {
          studio = useCreativeStudio()
          return () => h(CreativeComposer, { studio })
        },
      }),
      {
        global: {
          plugins: [createPinia()],
          stubs: { Icon: true, ModelIcon: true, Collapse: true, 'i18n-t': true },
        },
      },
    )
    try {
      const textarea = wrapper.get('textarea')
      /**
       * The DOM test environment does not compute layout, so derive scrollHeight from
       * current textarea lines to observe post-update measurement ordering.
       */
      Object.defineProperty(textarea.element, 'scrollHeight', {
        get: () => textarea.element.value.split('\n').length * 24 + 24,
      })
      const longPrompt = Array.from({ length: 10 }, (_, index) => `第 ${index + 1} 行`).join('\n')
      for (const [runId, prompt] of [
        ['long', longPrompt],
        ['short', '一只猫'],
      ]) {
        studio.runParamsMap.value.set(runId, {
          runId,
          optionKey: '',
          groupName: '',
          operation: 'generate',
          prompt,
          imageSize: '',
          aspectRatio: '',
          quality: '',
          background: '',
          thinkingLevel: '',
          referenceCount: 0,
          createdAt: 1,
        })
      }

      expect(studio.applyRunParams('long')).toBe(true)
      await nextTick()
      expect(textarea.element.value).toBe(longPrompt)
      expect(textarea.element.style.height).toBe('160px')

      expect(studio.applyRunParams('short')).toBe(true)
      await nextTick()
      expect(textarea.element.value).toBe('一只猫')
      expect(textarea.element.style.height).toBe('48px')

      await textarea.setValue('第一行\n第二行\n第三行')
      expect(textarea.element.style.height).toBe('96px')
    } finally {
      wrapper.unmount()
    }
  })
})
