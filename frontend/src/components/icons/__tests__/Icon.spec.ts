import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import Icon from '../Icon.vue'
import { icons, type IconName } from '../registry'

// Icon.spec.ts verifies registry coverage and the rendered SVG wrapper shared
// by interface icons. The cases exercise each registered artwork plus caller
// sizing, event forwarding, accessibility attributes, custom stroke weights,
// and root identity during artwork replacement.

// existingNames lists semantic names used by callers whose registry entries
// must remain available across artwork changes.
const existingNames: IconName[] = [
  'play',
  'refresh',
  'edit',
  'trash',
  'plus',
  'search',
  'more',
  'chart',
  'clock',
  'link',
  'sync',
  'chevronDown',
  'chevronRight',
  'chevronLeft',
  'check',
  'x',
  'eye',
  'eyeOff',
  'cog',
  'grid',
  'chat',
  'lightbulb',
  'arrowRight',
  'arrowLeft',
  'arrowUp',
  'arrowDown',
  'arrowsUpDown',
  'chevronUp',
  'externalLink',
  'checkCircle',
  'xCircle',
  'exclamationCircle',
  'exclamationTriangle',
  'infoCircle',
  'questionCircle',
  'user',
  'userCircle',
  'userPlus',
  'users',
  'document',
  'clipboard',
  'copy',
  'inbox',
  'modalityText',
  'modalityImage',
  'modalityAudio',
  'modalityVideo',
  'moveRight',
  'download',
  'upload',
  'filter',
  'globe',
  'sort',
  'key',
  'lock',
  'shield',
  'menu',
  'calendar',
  'home',
  'terminal',
  'gift',
  'creditCard',
  'mail',
  'chartBar',
  'trendingUp',
  'database',
  'cube',
  'bell',
  'bolt',
  'sparkles',
  'cloud',
  'server',
  'sun',
  'moon',
  'book',
  'power',
  'dollar',
  'ban',
  'login',
  'swap',
  'beaker',
  'cpu',
  'chatBubble',
  'calculator',
  'fire',
  'badge',
  'brain',
]

describe('统一图标入口', () => {
  // Every registered artwork is mounted through the shared wrapper. Each name
  // must produce one SVG with shape nodes, no NaN output, and
  // decorative children excluded from sequential focus.
  it('保留所有既有语义名称，每种图形都有可见 SVG 节点', () => {
    expect(existingNames.every((name) => name in icons)).toBe(true)
    for (const name of Object.keys(icons) as IconName[]) {
      const wrapper = mount(Icon, { props: { name } })
      expect(wrapper.element.tagName.toLowerCase(), name).toBe('svg')
      expect(wrapper.findAll('svg'), name).toHaveLength(1)
      expect(
        wrapper.find('path, circle, rect, line, polyline, polygon, ellipse').exists(),
        name,
      ).toBe(true)
      expect(wrapper.html(), name).not.toContain('NaN')
      // Motion focus handlers can make child shapes keyboard targets, so
      // inspect every shape after mounting each registered renderer.
      for (const node of wrapper.findAll(
        'g, path, circle, rect, line, polyline, polygon, ellipse',
      )) {
        expect(node.attributes('tabindex'), name).toBe('-1')
        expect(node.attributes('focusable'), name).toBe('false')
      }
      wrapper.unmount()
    }
  })

  // A caller supplies a size preset, stroke width, custom classes, styles,
  // label, and click listener. The SVG must retain these inputs and expose the
  // labeled image role.
  it('保留尺寸、描边、样式、事件及标签透传', async () => {
    const click = vi.fn()
    const wrapper = mount(Icon, {
      props: { name: 'cog', size: 'sm', strokeWidth: 1.5 },
      attrs: {
        class: 'text-red-500 rotate-180',
        style: 'opacity: 0.5',
        'aria-label': '设置',
        onClick: click,
      },
    })
    expect(wrapper.classes()).toEqual(expect.arrayContaining(['h-4', 'w-4', 'rotate-180']))
    expect(wrapper.attributes('stroke-width')).toBe('1.5')
    expect(wrapper.attributes('aria-hidden')).toBeUndefined()
    expect(wrapper.attributes('role')).toBe('img')
    expect(wrapper.attributes('style')).toContain('opacity: 0.5')
    await wrapper.trigger('click')
    expect(click).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  // Brain and document artwork are rendered with a nondefault stroke width. Any
  // stroke explicitly supplied by their internal nodes must agree with the
  // wrapper's requested value.
  it.each(['brain', 'document'] as const)('内部图形也继承自定义描边：%s', (name) => {
    const wrapper = mount(Icon, { props: { name, strokeWidth: 1.5 } })
    for (const node of wrapper.element.querySelectorAll<SVGElement>('*')) {
      const stroke = node.getAttribute('stroke-width') || node.style.strokeWidth
      if (stroke) expect(Number(stroke)).toBe(1.5)
    }
    wrapper.unmount()
  })

  // An unlabeled icon receives dimensions through caller utility classes. The
  // wrapper must remain decorative and omit competing preset dimension classes
  // while retaining its default stroke width.
  it('默认装饰图标不参与键盘焦点，调用点尺寸不会与默认类冲突', () => {
    const wrapper = mount(Icon, {
      props: { name: 'key' },
      attrs: { class: 'h-12 w-12' },
    })
    expect(wrapper.attributes('aria-hidden')).toBe('true')
    expect(wrapper.attributes('focusable')).toBe('false')
    expect(wrapper.attributes('tabindex')).toBe('-1')
    expect(wrapper.attributes('stroke-width')).toBe('1.75')
    expect(wrapper.classes()).not.toContain('h-[18px]')
    expect(wrapper.classes()).not.toContain('w-[18px]')
    wrapper.unmount()
  })

  // The selected artwork changes from copy to check during a component update.
  // Its root SVG must keep its identity and rotation while replacement paths
  // remain outside sequential focus.
  it('切换业务状态只替换内部图形，保持根节点和外层旋转', async () => {
    const wrapper = mount(Icon, {
      props: { name: 'copy' },
      attrs: { class: 'rotate-180' },
    })
    const svg = wrapper.element
    await wrapper.setProps({ name: 'check', animateOnHover: false })
    expect(wrapper.element).toBe(svg)
    expect(wrapper.attributes('data-animated-icon')).toBe('check')
    expect(wrapper.classes()).toContain('rotate-180')
    expect(wrapper.find('path').attributes('tabindex')).toBe('-1')
    wrapper.unmount()
  })
})
