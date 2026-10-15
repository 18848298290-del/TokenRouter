import { ref, watch, type Ref } from 'vue'

/** 浮层跟随折叠中的触发器，并在所属区域退出时同步关闭 Teleport 内容。 */
export function useFloatingMotion(
  trigger: Readonly<Ref<HTMLElement | null>>,
  visible: () => boolean,
  close: () => void,
  reposition: () => void,
) {
  const blocked = ref(false)
  watch([trigger, visible], ([element, open], _previous, onCleanup) => {
    blocked.value = false
    if (!element || !open) return
    let frame = 0
    let stopped = false
    const cancelFrame = () => {
      if (frame) cancelAnimationFrame(frame)
      frame = 0
    }
    const follow = () => {
      frame = 0
      if (stopped || blocked.value) return
      reposition()
      if (element.closest('.motion-collapse-moving')) frame = requestAnimationFrame(follow)
    }
    const update = () => {
      // v-show 通过内联样式隐藏祖先，class 也可能让整个区域退出布局。
      let next = !!element.closest('[inert], [hidden]')
      let current: HTMLElement | null = element
      while (current && !next) {
        next = getComputedStyle(current).display === 'none'
        current = current.parentElement
      }
      if (next && !blocked.value) close()
      blocked.value = next
      cancelFrame()
      if (!next) follow()
    }
    const observer = new MutationObserver(update)
    // 观察当前触发器的祖先节点，节点属性变化时重新测量布局。
    let ancestor: HTMLElement | null = element
    while (ancestor) {
      observer.observe(ancestor, { attributes: true, attributeFilter: ['inert', 'hidden', 'class', 'style'] })
      ancestor = ancestor.parentElement
    }
    onCleanup(() => {
      stopped = true
      observer.disconnect()
      cancelFrame()
    })
    update()
  }, { immediate: true, flush: 'post' })
  return blocked
}
