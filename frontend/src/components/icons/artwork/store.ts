// 图形来自 Lucide；官方暂无对应动画，使用统一轻微缩放。
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/store.svg
import { createFallbackIcon } from '../createFallbackIcon'

// store.ts provides the Lucide storefront shape used for the model marketplace
// entry in the sidebar and header. Lucide Animated has no store animation, so
// createFallbackIcon supplies the shared single scale pulse on hover.

export default createFallbackIcon('store', [
  [
    'path',
    {
      d: 'M15 21v-5a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v5'
    }
  ],
  [
    'path',
    {
      d: 'M17.774 10.31a1.12 1.12 0 0 0-1.549 0 2.5 2.5 0 0 1-3.451 0 1.12 1.12 0 0 0-1.548 0 2.5 2.5 0 0 1-3.452 0 1.12 1.12 0 0 0-1.549 0 2.5 2.5 0 0 1-3.77-3.248l2.889-4.184A2 2 0 0 1 7 2h10a2 2 0 0 1 1.653.873l2.895 4.192a2.5 2.5 0 0 1-3.774 3.244'
    }
  ],
  [
    'path',
    {
      d: 'M4 10.95V19a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8.05'
    }
  ]
])
