import { mount } from '@vue/test-utils'
import UserAvatar from '../UserAvatar.vue'

/**
 * UserAvatar.spec.ts checks uploaded image precedence, deterministic identicons, and
 * caller dimension classes. The cases compare generated image sources for matching and
 * differing identities, including the stable seed path used when no user identifier is
 * supplied.
 */

describe('UserAvatar', () => {
  /**
   * A user supplies an uploaded avatar URL together with their identifier. The image
   * uses that URL and retains the caller supplied alternative text.
   */
  it('有 avatarUrl 时优先展示上传头像', () => {
    const wrapper = mount(UserAvatar, {
      props: {
        avatarUrl: 'https://cdn.example.com/a.png',
        userId: 7,
        alt: 'alice',
      },
    })

    const img = wrapper.get('img')
    expect(img.attributes('src')).toBe('https://cdn.example.com/a.png')
    expect(img.attributes('alt')).toBe('alice')
  })

  /**
   * A user has an identifier but no uploaded avatar URL. The component displays a
   * generated SVG data URI through the shared deterministic identicon path.
   */
  it('无 avatarUrl 时按用户 ID 生成 identicon', () => {
    const wrapper = mount(UserAvatar, {
      props: { userId: 7 },
    })

    const src = wrapper.get('img').attributes('src') || ''
    expect(src.startsWith('data:image/svg+xml,')).toBe(true)
  })

  /**
   * Two avatar instances receive the same user identifier and another receives a
   * different identifier. The matching instances share an image while the other image
   * differs.
   */
  it('相同用户 ID 渲染相同默认头像，不同 ID 不同', () => {
    const first = mount(UserAvatar, { props: { userId: 7 } })
    const second = mount(UserAvatar, { props: { userId: 7 } })
    const other = mount(UserAvatar, { props: { userId: 8 } })

    expect(first.get('img').attributes('src')).toBe(second.get('img').attributes('src'))
    expect(first.get('img').attributes('src')).not.toBe(other.get('img').attributes('src'))
  })

  /**
   * The user identifier is unavailable and callers provide the same stable seed. Both
   * avatar instances render identical generated image sources for that seed.
   */
  it('用户 ID 缺失时使用兜底种子', () => {
    const bySeed = mount(UserAvatar, { props: { seed: 'user-42' } })
    const sameSeed = mount(UserAvatar, { props: { seed: 'user-42' } })

    expect(bySeed.get('img').attributes('src')).toBe(sameSeed.get('img').attributes('src'))
  })

  /**
   * A caller supplies custom avatar size classes for its layout. The image retains
   * those classes alongside the rounded avatar shape supplied by the component.
   */
  it('透传尺寸 class', () => {
    const wrapper = mount(UserAvatar, {
      props: { userId: 7, sizeClass: 'h-20 w-20 shrink-0' },
    })

    const classes = wrapper.get('img').classes()
    expect(classes).toContain('h-20')
    expect(classes).toContain('w-20')
    expect(classes).toContain('rounded-full')
  })
})
