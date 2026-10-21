import { defineComponent, type VNode } from 'vue'

// RetainedContent.ts retains the last rendered slot nodes while a containing
// transition freezes its content. Collapse uses this component when a region will
// unmount after leaving, allowing its outgoing frame to remain visible after parent
// business data changes.

export default defineComponent({
  name: 'RetainedContent',
  // freeze retains the previous slot nodes while the parent owns an outgoing
  // transition; an initially frozen renderer has no prior content.
  props: { freeze: Boolean },
  // setup returns a renderer that retains the last unfrozen default-slot nodes. The
  // initial retained value is empty, and freezing before the first slot render
  // therefore returns an empty node list.
  setup(props, { slots }) {
    // content stores the last unfrozen default-slot result for this component instance
    // and is released with the component.
    let content: VNode[] = []
    return () => {
      // Read the slot only while unfrozen so parent data clearing during exit cannot
      // replace the outgoing tree before its containing transition finishes.
      if (!props.freeze) content = slots.default?.() ?? []
      return content
    }
  },
})
