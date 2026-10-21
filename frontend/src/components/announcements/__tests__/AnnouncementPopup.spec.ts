import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import postcss from 'postcss'
import { cssRules, readSource } from '@/__tests__/helpers/sourceSyntax'

import AnnouncementPopup from '../AnnouncementPopup.vue'
import { useAnnouncementStore } from '@/stores/announcements'

// AnnouncementPopup.spec.ts checks queued announcement dialogs and controlled
// previews with local store state. Mounted content verifies sanitized Markdown
// and HTML, read metadata, dismissal ownership, and scroll restoration while the
// shared stylesheet supplies mixed-content element rules.

// announcementMarkdownStyles supplies parsed shared CSS rules for assertions
// covering Markdown and embedded HTML selectors independently of source wrapping.
const announcementMarkdownStyles = postcss.parse(readSource('styles/announcement-markdown.css'))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/utils/format', () => ({
  formatDateTime: () => '2026/07/24 15:30:00',
  formatRelativeTime: () => '3 hours ago',
}))

// announcement supplies deterministic draft content and metadata, including an
// unsafe script used to verify sanitization in controlled previews.
const announcement = {
  id: 1,
  title: 'Preview announcement',
  content: '## Preview heading\n\n<div>HTML content</div><script>window.__xss = true</script>',
  status: 'draft' as const,
  notify_mode: 'popup' as const,
  targeting: { any_of: [] },
  created_at: '2026-07-24T07:30:00Z',
  updated_at: '2026-07-24T07:30:00Z',
}

describe('AnnouncementPopup', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    document.body.innerHTML = ''
    document.body.style.overflow = ''
  })

  // Queued content combines Markdown, HTML, a table, and a script tag. The popup
  // must render supported content inside its shared body and remove the
  // executable script.
  it('renders mixed Markdown and HTML inside the shared styled container', async () => {
    const store = useAnnouncementStore()
    store.currentPopup = {
      id: 1,
      title: 'Mixed content announcement',
      content: [
        '## Markdown heading',
        '',
        '<div><h3>HTML heading</h3><ul><li>HTML list item</li></ul></div>',
        '',
        '<table><thead><tr><th>Status</th></tr></thead><tbody><tr><td>OK</td></tr></tbody></table>',
        '<script>window.__announcementXss = true</script>',
      ].join('\n'),
      notify_mode: 'popup',
      created_at: '2026-07-24T07:30:00Z',
      updated_at: '2026-07-24T07:30:00Z',
    }

    const wrapper = mount(AnnouncementPopup)
    await wrapper.vm.$nextTick()

    const content = document.body.querySelector('.markdown-body')
    expect(content?.querySelector('h2')?.textContent).toBe('Markdown heading')
    expect(content?.querySelector('h3')?.textContent).toBe('HTML heading')
    expect(content?.querySelector('li')?.textContent).toBe('HTML list item')
    expect(content?.querySelector('table td')?.textContent).toBe('OK')
    expect(content?.querySelector('script')).toBeNull()

    wrapper.unmount()
  })

  // Mixed announcement content uses several Markdown and HTML element kinds.
  // Each parameterized element must have a matching shared style selector in the
  // announcement stylesheet.
  it.each(['h2', 'h3', 'ul', 'li', 'blockquote', 'table', 'th', 'td', 'code'])(
    'loads a shared style rule for mixed-content <%s> elements',
    (element) => {
      expect(
        cssRules(announcementMarkdownStyles, `.markdown-body ${element}`).length,
      ).toBeGreaterThan(0)
    },
  )

  // An administrator preview receives draft content with a script tag. Closing
  // it must notify the parent, leave the store queue untouched, and restore
  // scrolling when content clears.
  it('previews an admin announcement without marking it as read', async () => {
    const store = useAnnouncementStore()
    const dismissPopup = vi.spyOn(store, 'dismissPopup')
    const wrapper = mount(AnnouncementPopup, {
      props: {
        announcement,
        preview: true,
      },
    })

    expect(document.body.textContent).toContain('Preview announcement')
    expect(document.body.querySelector('.markdown-body h2')?.textContent).toBe('Preview heading')
    expect(document.body.querySelector('.markdown-body script')).toBeNull()
    expect(document.body.textContent).toContain('common.close')
    expect(document.body.querySelector('[data-testid="announcement-popup-status"]')).toBeNull()

    const dismissButton = document.body.querySelector<HTMLButtonElement>(
      '[data-testid="announcement-popup-dismiss"]',
    )
    dismissButton?.click()
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(dismissPopup).not.toHaveBeenCalled()

    await wrapper.setProps({ announcement: null })
    expect(document.body.style.overflow).toBe('')
    wrapper.unmount()
  })

  // A controlled user announcement includes a read timestamp and enables status
  // display. Its dialog must show compact metadata once and retain the shared
  // close control.
  it('shows compact read metadata for a controlled user announcement', async () => {
    const wrapper = mount(AnnouncementPopup, {
      props: {
        announcement: {
          ...announcement,
          read_at: '2026-07-24T08:00:00Z',
        },
        preview: true,
        showReadStatus: true,
      },
    })

    const dialog = document.body.querySelector('[role="dialog"]')
    const status = document.body.querySelector('[data-testid="announcement-popup-status"]')

    expect(dialog?.classList).toContain('max-w-[640px]')
    expect(dialog?.classList).toContain('rounded-surface')
    expect(dialog?.classList).toContain('sm:rounded-dialog')
    expect(document.body.textContent).toContain('announcements.title')
    expect(document.body.textContent).toContain('announcements.read')
    expect(document.body.textContent?.match(/announcements\.read/g)).toHaveLength(1)
    expect(status?.textContent).toContain('announcements.read')
    expect(document.body.querySelector('[data-testid="announcement-popup-close"]')).not.toBeNull()

    wrapper.unmount()
  })

  // A queued user popup is dismissed through its footer action. The store
  // dismissal method must run once, while the preview-only close event remains
  // absent.
  it('keeps the existing user popup dismissal behavior', async () => {
    const store = useAnnouncementStore()
    store.currentPopup = announcement
    const dismissPopup = vi.spyOn(store, 'dismissPopup').mockResolvedValue()
    const wrapper = mount(AnnouncementPopup)

    const dismissButton = document.body.querySelector<HTMLButtonElement>(
      '[data-testid="announcement-popup-dismiss"]',
    )
    dismissButton?.click()
    await wrapper.vm.$nextTick()

    expect(dismissPopup).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('close')).toBeUndefined()
    wrapper.unmount()
  })
})
