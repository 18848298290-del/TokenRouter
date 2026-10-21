import { afterEach, describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'

import BaseDialog from '../BaseDialog.vue'

// BaseDialog.spec.ts tests responsive dialog widths and content scrolling across
// reopening. Mounted shells expose their overlay, body, and footer constraints, while
// the scroll case distinguishes a contained embedded form from the default scrollable
// body.

// wrappers tracks mounted dialog instances so teardown can unmount them and release
// their shared lifecycle resources.
const wrappers: VueWrapper[] = []

// Release the resources retained by this case before the next one, keeping browser
// state and component lifecycle observations isolated across assertions.
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  document.body.classList.remove('modal-open')
  document.body.innerHTML = ''
})

describe('BaseDialog 移动端视口约束', () => {
  // A dialog is mounted for each responsive width recipe with body and footer content.
  // The assertions protect viewport constraints, retained layout classes, and the
  // shared modal scroll lock.
  it.each([
    ['narrow', 'sm:max-w-md'],
    ['normal', 'sm:max-w-lg'],
    ['wide', 'sm:max-w-2xl'],
    ['extra-wide', 'sm:max-w-3xl'],
    ['full', 'sm:max-w-4xl'],
  ] as const)('为 %s 尺寸保留移动端视口边距', async (width, desktopWidthClass) => {
    const wrapper = mount(BaseDialog, {
      attachTo: document.body,
      props: {
        show: true,
        title: '移动端弹窗',
        width,
      },
      slots: {
        default: '<div>弹窗内容</div>',
        footer: '<button type="button">确认</button>',
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })
    wrappers.push(wrapper)
    await nextTick()

    const overlay = document.body.querySelector<HTMLElement>('.modal-overlay')
    const panel = document.body.querySelector<HTMLElement>('.modal-content')
    const body = document.body.querySelector<HTMLElement>('.modal-body')
    const footer = document.body.querySelector<HTMLElement>('.modal-footer')

    expect(overlay).not.toBeNull()
    expect(panel).not.toBeNull()
    expect(body).not.toBeNull()
    expect(footer).not.toBeNull()
    expect(overlay!.classList).toContain('h-[100dvh]')
    expect(overlay!.classList).toContain('w-[100dvw]')
    expect(overlay!.classList).toContain('overflow-hidden')
    expect(panel!.classList).toContain('min-h-0')
    expect(panel!.classList).toContain('min-w-0')
    expect(panel!.classList).toContain('max-h-[95dvh]')
    expect(panel!.classList).toContain('sm:max-h-[90dvh]')
    expect(panel!.classList).toContain('max-w-[calc(100vw-1rem)]')
    expect(panel!.classList).toContain(desktopWidthClass)
    expect(body!.classList).toContain('min-h-0')
    expect(body!.classList).toContain('min-w-0')
    expect(body!.classList).toContain('max-w-full')
    expect(footer!.classList).toContain('min-w-0')
    expect(document.body.classList).toContain('modal-open')
  })
})

// The dialog switches between body-owned scrolling and a contained form layout before
// closing and reopening. The assertions protect contained styling and reset the
// default body scroll position on reopening.
it('支持由内嵌表单管理滚动，并在重新打开时复位默认内容区', async () => {
  const wrapper = mount(BaseDialog, {
    props: { show: true, title: '设置' },
    slots: { default: '<input />' },
  })
  wrappers.push(wrapper)
  await nextTick()
  const body = document.body.querySelector<HTMLElement>('.modal-body')!
  expect(body.classList.contains('modal-body-contained')).toBe(false)
  await wrapper.setProps({ bodyScroll: false })
  expect(body.classList.contains('modal-body-contained')).toBe(true)
  await wrapper.setProps({ bodyScroll: true })
  body.scrollTop = 120
  await wrapper.setProps({ show: false })
  await wrapper.setProps({ show: true })
  await nextTick()
  expect(document.body.querySelector<HTMLElement>('.modal-body')!.scrollTop).toBe(0)
})
